package imgix

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/imgproxy/imgproxy/v4/cookies"
	"github.com/imgproxy/imgproxy/v4/errctx"
	"github.com/imgproxy/imgproxy/v4/fetcher"
	"github.com/imgproxy/imgproxy/v4/handlers"
	processinghandler "github.com/imgproxy/imgproxy/v4/handlers/processing"
	"github.com/imgproxy/imgproxy/v4/imagedata"
	"github.com/imgproxy/imgproxy/v4/options"
	"github.com/imgproxy/imgproxy/v4/security"
	"github.com/imgproxy/imgproxy/v4/server"
	"github.com/imgproxy/imgproxy/v4/workers"
)

var warned sync.Map

type HandlerContext interface {
	ImageDataFactory() imagedata.Factory
	Security() *security.Checker
	Cookies() *cookies.Cookies
	Workers() *workers.Workers
	Fetcher() *fetcher.Fetcher
}

type PathExecutor interface {
	ExecutePath(string, string, server.ResponseWriter, *http.Request, processinghandler.PreparePath) *server.Error
}

type Handler struct {
	ctx      HandlerContext
	executor PathExecutor
	config   Config
	load     MetadataLoader
}

func New(ctx HandlerContext, executor PathExecutor, config *Config, loader MetadataLoader) (*Handler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if loader == nil {
		loader = loadMetadata
	}
	return &Handler{ctx, executor, *config, loader}, nil
}

func warnOnce(warnings []Warning) {
	for _, warning := range warnings {
		if _, seen := warned.LoadOrStore(warning, struct{}{}); !seen {
			slog.Warn("Ignoring unsupported imgix option", "kind", warning.Kind, "value", warning.Value)
		}
	}
}

func (h *Handler) Execute(reqID string, rw server.ResponseWriter, req *http.Request) *server.Error {
	if !VerifySignature(*req.URL, h.config.SignKey) {
		return publicError(http.StatusForbidden, "Forbidden", handlers.ErrCategorySecurity)
	}
	result, err := Translate(*req.URL)
	if err != nil {
		// Translate errors only describe query parameters, never sources.
		return publicError(http.StatusBadRequest, "Invalid parameters: "+err.Error(), handlers.ErrCategoryPathParsing)
	}
	warnOnce(result.Warnings)
	source, err := result.SourceURL(h.config.SourcePrefix)
	if err != nil {
		return publicError(http.StatusBadRequest, "Invalid source", handlers.ErrCategoryPathParsing)
	}
	// The fetcher must resolve the source to exactly this object. This
	// rejects keys it would cut at the source URL query separator.
	if f := h.ctx.Fetcher(); f != nil && !isHTTP(source) &&
		f.SourcePath(source) != f.SourcePath(h.config.SourcePrefix)+result.Key {
		return publicError(http.StatusBadRequest, "Invalid parameters: object key can't be fetched from storage", handlers.ErrCategoryPathParsing)
	}
	if err := h.ctx.Security().VerifySourceURL(source); err != nil {
		return publicError(http.StatusForbidden, "Forbidden", handlers.ErrCategorySecurity)
	}
	if result.JSON {
		return h.metadata(reqID, rw, req, source)
	}

	path := result.Path(source)
	prepare := func(data imagedata.ImageData) (string, error) {
		pdf := isPDF(data)
		refined := result
		// imgproxy can't save PDF, so a PDF with no fm renders as PNG. Images
		// with no fm keep imgproxy's choice (source format, or JPEG for
		// formats it doesn't prefer, such as HEIC).
		if pdf && refined.Format == "" {
			refined.Format = "png"
		}
		// Raster images without rect already obey el:0 in the normal pipeline.
		if refined.Rect == nil && !data.Format().IsVector() && !pdf {
			return refined.Path(source), nil
		}
		// A page past the end fails here with PageOutOfRangeError (422),
		// which is passed through as-is.
		md, err := h.load(data, refined.Page)
		if err != nil {
			return "", err
		}
		if err := h.ctx.Security().CheckDimensions(options.New(), md.Width, md.Height, 1); err != nil {
			return "", err
		}
		resolved := refined.Resolve(md.Width, md.Height)
		if pdf {
			// PDFs are vectors and scale up to the requested w/h box, as imgix
			// does ("Vectors will scale before rasterizing"). large page renders
			// 2012 page renders depend on this. Other sources keep fit=max.
			resolved.Width, resolved.Height = refined.Width, refined.Height
		}
		return resolved.Path(source), nil
	}
	return sanitize(h.executor.ExecutePath(reqID, path, rw, req, prepare))
}

func isHTTP(source string) bool {
	return strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")
}

func (h *Handler) metadata(reqID string, rw server.ResponseWriter, req *http.Request, source string) *server.Error {
	release, err := h.ctx.Workers().Acquire(req.Context())
	if err != nil {
		// Same classification as the processing handler's acquireWorker:
		// a timeout or a canceled request, not an internal error.
		if terr := server.CheckTimeout(req.Context()); terr != nil {
			return sanitize(server.NewError(terr, handlers.ErrCategoryQueue))
		}
		return sanitize(server.NewError(errctx.Wrap(err), handlers.ErrCategoryQueue))
	}
	defer release()
	jar, err := h.ctx.Cookies().JarFromRequest(req)
	if err != nil {
		return sanitize(server.NewError(errctx.Wrap(err), handlers.ErrCategoryDownload))
	}
	data, _, err := h.ctx.ImageDataFactory().DownloadAsync(req.Context(), source, "source image", imagedata.DownloadOptions{
		Header: make(http.Header), CookieJar: jar, MaxSrcFileSize: h.ctx.Security().MaxSrcFileSize(options.New()),
	})
	if data != nil {
		defer data.Close()
	}
	if err != nil {
		return sanitize(server.NewError(errctx.Wrap(err), handlers.ErrCategoryDownload))
	}
	// Metadata always describes page zero, regardless of page/rect/resize params.
	md, err := h.load(data, 0)
	if err != nil {
		return sanitize(server.NewError(errctx.Wrap(err), handlers.ErrCategoryProcessing))
	}
	// Metadata loading can stop at the header; finish the download so late
	// transport/size-limit errors cannot become a successful JSON response.
	if _, err := data.Size(); err != nil {
		return sanitize(server.NewError(errctx.Wrap(err), handlers.ErrCategoryDownload))
	}
	if err := h.ctx.Security().CheckDimensions(options.New(), md.Width, md.Height, 1); err != nil {
		return sanitize(server.NewError(errctx.Wrap(err), handlers.ErrCategorySecurity))
	}
	if err := server.CheckTimeout(req.Context()); err != nil {
		return sanitize(server.NewError(err, handlers.ErrCategoryTimeout))
	}
	var payload any
	if md.PDF {
		payload = struct{ PDF struct{ PageCount int } }{PDF: struct{ PageCount int }{md.Pages}}
	} else {
		// Key names follow imgix's documented fm=json example.
		payload = struct {
			PixelWidth  int
			PixelHeight int
		}{md.Width, md.Height}
	}
	rw.SetContentType("application/json")
	rw.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(rw).Encode(payload); err != nil {
		return sanitize(server.NewError(errctx.Wrap(err), handlers.ErrCategoryIO))
	}
	// Errors are logged by the router middleware; success is logged here,
	// as the processing handler does.
	server.LogResponse(reqID, req, http.StatusOK, nil)
	return nil
}

// responseError intentionally has no cause or stack, including in development mode.
type responseError struct {
	*errctx.ErrorContext
	message string
}

func (e responseError) Error() string { return e.message }

func publicError(status int, message, category string) *server.Error {
	ec := (&errctx.ErrorContext{}).CloneErrorContext(
		errctx.WithStatusCode(status), errctx.WithPublicMessage(message), errctx.WithShouldReport(false))
	return server.NewError(responseError{ec, message}, category)
}

// Drop internal error chains, not just PublicMessage, so development error
// pages cannot expose source URLs, object keys or internal filesystem paths.
func sanitize(err *server.Error) *server.Error {
	if err == nil {
		return nil
	}
	status := err.Err.StatusCode()
	message := http.StatusText(status)
	if message == "" {
		message = "Request failed"
	}
	return publicError(status, message, err.Category)
}
