package processing

import (
	"net/http"

	"github.com/imgproxy/imgproxy/v4/auximageprovider"
	"github.com/imgproxy/imgproxy/v4/clientfeatures"
	"github.com/imgproxy/imgproxy/v4/cookies"
	"github.com/imgproxy/imgproxy/v4/errctx"
	"github.com/imgproxy/imgproxy/v4/errorreport"
	"github.com/imgproxy/imgproxy/v4/fetcher"
	"github.com/imgproxy/imgproxy/v4/handlers"
	"github.com/imgproxy/imgproxy/v4/handlers/stream"
	"github.com/imgproxy/imgproxy/v4/httpheaders/conditionalheaders"
	"github.com/imgproxy/imgproxy/v4/imagedata"
	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/monitoring"
	"github.com/imgproxy/imgproxy/v4/options"
	"github.com/imgproxy/imgproxy/v4/options/keys"
	optionsparser "github.com/imgproxy/imgproxy/v4/options/parser"
	"github.com/imgproxy/imgproxy/v4/processing"
	"github.com/imgproxy/imgproxy/v4/security"
	"github.com/imgproxy/imgproxy/v4/server"
	"github.com/imgproxy/imgproxy/v4/server/meta"
	"github.com/imgproxy/imgproxy/v4/workers"
)

// HandlerContext provides access to shared handler dependencies
//
//nolint:interfacebloat
type HandlerContext interface {
	Workers() *workers.Workers
	ClientFeaturesDetector() *clientfeatures.Detector
	FallbackImage() auximageprovider.Provider
	ImageDataFactory() imagedata.Factory
	Security() *security.Checker
	OptionsParser() *optionsparser.Parser
	Processor() *processing.Processor
	Cookies() *cookies.Cookies
	Monitoring() *monitoring.Monitoring
	ErrorReporter() *errorreport.Reporter
	ConditionalHeaders() *conditionalheaders.Factory
	Fetcher() *fetcher.Fetcher
}

// Handler handles image processing requests
type Handler struct {
	HandlerContext

	stream *stream.Handler // Stream handler for raw image streaming
	config *Config         // Handler configuration
}

// New creates new handler object
func New(
	hCtx HandlerContext,
	stream *stream.Handler,
	config *Config,
) (*Handler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	return &Handler{
		HandlerContext: hCtx,
		config:         config,
		stream:         stream,
	}, nil
}

// PreparePath can refine a trusted internal path after downloading the source.
// It runs under the processing worker and must retain the source URL and any
// output format the original path set.
type PreparePath func(imagedata.ImageData) (string, error)

// ExecutePath executes a trusted, signature-free processing path (options/source,
// without an /insecure/ prefix). Only internal adapters that authenticate their
// own requests may call it. Source URL verification is NEVER bypassed.
func (h *Handler) ExecutePath(reqID, path string, rw server.ResponseWriter, req *http.Request, prepare PreparePath) *server.Error {
	h.Monitoring().Stats().IncRequestsInProgress()
	defer h.Monitoring().Stats().DecRequestsInProgress()
	r, err := h.newRequestPath(req, path)
	if err != nil {
		return err
	}
	if prepare != nil {
		r.prepare = func(data imagedata.ImageData) *server.Error {
			refined, err := prepare(data)
			if err != nil {
				return server.NewError(errctx.Wrap(err), handlers.ErrCategoryProcessing)
			}
			next, serr := h.newRequestPath(req, refined)
			if serr != nil {
				return serr
			}
			// The source must not change. The output format may only be filled
			// in when the original path left it unset.
			format := options.Get(r.opts, keys.Format, imagetype.Unknown)
			nextFormat := options.Get(next.opts, keys.Format, imagetype.Unknown)
			if next.imageURL != r.imageURL || (format != imagetype.Unknown && nextFormat != format) {
				return server.NewError(handlers.NewInvalidPathError(req.Context(), ""), handlers.ErrCategoryPathParsing)
			}
			r.opts = next.opts
			return nil
		}
	}
	return h.executeRequest(reqID, rw, req, r)
}

// Execute handles the image processing request
func (h *Handler) Execute(
	reqID string,
	rw server.ResponseWriter,
	req *http.Request,
) *server.Error {
	// Increment the number of requests in progress
	h.Monitoring().Stats().IncRequestsInProgress()
	defer h.Monitoring().Stats().DecRequestsInProgress()

	// Verify URL signature and extract image url and processing options
	r, err := h.newRequest(req)
	if err != nil {
		return err
	}

	return h.executeRequest(reqID, rw, req, r)
}

func (h *Handler) executeRequest(reqID string, rw server.ResponseWriter, req *http.Request, r *request) *server.Error {
	// if processing options indicate raw image streaming, stream it and return
	if r.opts.GetBool(keys.Raw, false) {
		return h.stream.Execute(req, r.imageURL, reqID, r.opts, rw)
	}

	r.reqID = reqID
	r.req = req
	r.rw = rw
	r.config = h.config
	r.ch = h.ConditionalHeaders().NewRequest(r.req)

	return r.execute()
}

// newRequest extracts image url and processing options from request URL and verifies them
func (h *Handler) newRequest(req *http.Request) (*request, *server.Error) {
	// let's extract signature and valid request path from a request
	path, signature, err := handlers.SplitPathSignature(req)
	if err != nil {
		return nil, server.NewError(errctx.Wrap(err), handlers.ErrCategoryPathParsing)
	}

	// verify the signature (if any)
	if err = h.Security().VerifySignature(req.Context(), signature, path); err != nil {
		return nil, server.NewError(errctx.Wrap(err), handlers.ErrCategorySecurity)
	}

	return h.newRequestPath(req, path)
}

func (h *Handler) newRequestPath(req *http.Request, path string) (*request, *server.Error) {
	// parse image url and processing options
	features := h.ClientFeaturesDetector().Features(req.Header)
	o, imageURL, err := h.OptionsParser().ParsePath(req.Context(), path, &features)
	if err != nil {
		return nil, server.NewError(errctx.Wrap(err), handlers.ErrCategoryPathParsing)
	}

	// get image origin and store it, along with the image URL and options, in the
	// request-scoped meta so error reporting and monitoring can pick them up
	imageOrigin := meta.URLOrigin(imageURL)

	meta.Set(req.Context(), meta.KeyImageURL, imageURL)
	meta.Set(req.Context(), meta.KeySourceImageOrigin, imageOrigin)
	meta.Set(req.Context(), meta.KeyOptions, o)

	// verify that image URL came from the valid source
	err = h.Security().VerifySourceURL(imageURL)
	if err != nil {
		return nil, server.NewError(errctx.Wrap(err), handlers.ErrCategorySecurity)
	}

	return &request{
		HandlerContext: h,

		imageURL: imageURL,
		path:     path,
		opts:     o,
		features: &features,
		req:      req,
	}, nil
}
