package imgix_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	proxy "github.com/imgproxy/imgproxy/v4"
	"github.com/imgproxy/imgproxy/v4/handlers/imgix"
	processinghandler "github.com/imgproxy/imgproxy/v4/handlers/processing"
	"github.com/imgproxy/imgproxy/v4/imagedata"
	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/server"
	"github.com/imgproxy/imgproxy/v4/vips"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	cfg := vips.NewDefaultConfig()
	if err := vips.Init(&cfg); err != nil {
		panic(err)
	}
	code := m.Run()
	vips.Shutdown()
	os.Exit(code)
}

func newApp(t *testing.T, config *proxy.Config) *proxy.Imgproxy {
	t.Helper()
	app, err := proxy.New(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(func() { app.Close(context.Background()) })
	return app
}

func router(t *testing.T, app *proxy.Imgproxy) *server.Router {
	t.Helper()
	r, err := app.BuildRouter()
	require.NoError(t, err)
	return r
}

func response(r http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func fixturePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 100, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 100; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func sourceServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	data := fixturePNG(t)
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/missing" {
			http.Error(w, "private source missing /internal/path", 404)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Last-Modified", "Mon, 12 Oct 2026 12:00:00 GMT")
		w.Write(data)
	}))
	t.Cleanup(s.Close)
	return s, &calls
}

func compatConfig(prefix string) proxy.Config {
	cfg := proxy.NewDefaultConfig()
	cfg.Handlers.Imgix = imgix.Config{Compat: true, SourcePrefix: prefix}
	cfg.Fetcher.Transport.HTTP.AllowLoopbackSourceAddresses = true
	return cfg
}

func TestRenderedRectAndResize(t *testing.T) {
	src, calls := sourceServer(t)
	cfg := compatConfig(src.URL + "/")
	cfg.Handlers.Processing.EnableDebugHeaders = true
	r := router(t, newApp(t, &cfg))
	for _, tc := range []struct {
		name, query string
		w, h, x, y  int
	}{
		{"unchanged", "", 100, 80, 0, 0},
		{"width", "w=50", 50, 40, -1, -1},
		{"height", "h=40", 50, 40, -1, -1},
		{"no enlarge", "w=1000&h=1000", 100, 80, 0, 0},
		{"normal crop", "rect=11,21,30,40", 30, 40, 11, 21},
		{"overflow clips 1px large", "rect=81,71,40,50", 20, 10, 80, 70},
		{"off-page x", "rect=100,0,20,20", 100, 80, 0, 0},
		{"off-page y", "rect=0,80,20,20", 100, 80, 0, 0},
		{"oversized", "rect=0,0,999,999", 100, 80, 0, 0},
		{"crop before resize", "rect=10,20,40,40&w=20", 20, 20, -1, -1},
		{"crop no enlarge", "rect=81,71,50,50&w=1000", 20, 10, 80, 70},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			rec := response(r, "/image.png?fm=png&"+tc.query)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Equal(t, int32(1), calls.Load()-before, "crop must reuse the same download")
			out, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
			require.NoError(t, err)
			require.Equal(t, image.Rect(0, 0, tc.w, tc.h), out.Bounds())
			if tc.x >= 0 {
				require.Equal(t, color.NRGBA{uint8(tc.x), uint8(tc.y), 100, 255}, color.NRGBAModel.Convert(out.At(0, 0)))
			}
		})
	}
}

func TestJSONRealImageAndErrors(t *testing.T) {
	src, _ := sourceServer(t)
	cfg := compatConfig(src.URL + "/")
	r := router(t, newApp(t, &cfg))
	rec := response(r, "/image.png?fm=json&page=2&w=1&rect=2,2,3,3")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, `{"PixelWidth":100,"PixelHeight":80}`, rec.Body.String())
	for _, path := range []string{"/missing?fm=json", "/missing?fm=png"} {
		rec = response(r, path)
		require.Equal(t, 404, rec.Code)
		require.NotContains(t, rec.Body.String(), src.URL)
		require.NotContains(t, rec.Body.String(), "/internal/path")
	}
	for _, query := range []string{"w=0", "q=101", "fm=pdf", "page=0", "rect=1,2,3", "rect=-1,0,20,20", "rect=left,top,full,full"} {
		rec = response(r, "/image.png?"+query)
		require.Equal(t, 400, rec.Code)
		require.Contains(t, rec.Body.String(), "Invalid parameters: ")
	}
}

type mockFactory struct {
	imagedata.Factory
	data  imagedata.ImageData
	calls int
}

func (f *mockFactory) DownloadAsync(_ context.Context, _ string, _ string, _ imagedata.DownloadOptions) (imagedata.ImageData, http.Header, error) {
	f.calls++
	return f.data.Ref(), make(http.Header), nil
}

type mockContext struct {
	*proxy.Imgproxy
	factory imagedata.Factory
}

func (c mockContext) ImageDataFactory() imagedata.Factory { return c.factory }

func TestJSONMockMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		md   imgix.Metadata
		body string
	}{
		{"pdf", imgix.Metadata{Width: 612, Height: 792, Pages: 3, PDF: true, ContentType: "application/pdf"}, `{"PDF":{"PageCount":3}}`},
		{"image", imgix.Metadata{Width: 100, Height: 80, Pages: 1, ContentType: "image/png"}, `{"PixelWidth":100,"PixelHeight":80}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := compatConfig("s3://private-bucket/")
			app := newApp(t, &cfg)
			data := imagedata.NewFromBytesWithFormat(imagetype.Unknown, []byte("%PDF-mocked"))
			defer data.Close()
			factory := &mockFactory{data: data}
			count := 0
			h, err := imgix.New(mockContext{app, factory}, nil, &cfg.Handlers.Imgix, func(got imagedata.ImageData, page int) (imgix.Metadata, error) {
				count++
				require.Same(t, data, got)
				require.Zero(t, page)
				return tc.md, nil
			})
			require.NoError(t, err)
			r, err := server.NewRouter(&cfg.Server, app.Monitoring(), app.ErrorReporter())
			require.NoError(t, err)
			r.GET("/*", h.Execute, r.WithReportError)
			rec := response(r, "/document?fm=json&page=9")
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			require.JSONEq(t, tc.body, rec.Body.String())
			require.Equal(t, 1, count)
			require.Equal(t, 1, factory.calls)
		})
	}
}

func TestRoutingAndSignatureIsolation(t *testing.T) {
	src, calls := sourceServer(t)
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "upstream", true: "imgix"}[enabled], func(t *testing.T) {
			cfg := compatConfig(src.URL + "/")
			cfg.Handlers.Imgix.Compat = enabled
			cfg.Handlers.Imgix.SignKey = "secret"
			// With no upstream HMAC keys, this exact bare-key request proves
			// which catch-all handler was selected, even without the pg option.
			routeOnly := router(t, newApp(t, &cfg))
			bare := response(routeOnly, "/some/key.pdf?page=2&w=300")
			if enabled {
				require.Equal(t, 403, bare.Code)
				require.Equal(t, "Forbidden", bare.Body.String())
			} else {
				// Upstream rejects the bare key as an imgproxy path (404 before
				// pg, 422 with it), never with the imgix handler's 403.
				require.GreaterOrEqual(t, bare.Code, 400)
				require.NotEqual(t, 403, bare.Code)
				require.NotEqual(t, "Forbidden", bare.Body.String())
			}

			cfg.Security.Keys = [][]byte{[]byte("upstream-key")}
			cfg.Security.Salts = [][]byte{[]byte("upstream-salt")}
			r := router(t, newApp(t, &cfg))
			before := calls.Load()
			rec := response(r, "/some/key.pdf?page=2&w=300")
			require.Equal(t, 403, rec.Code)
			require.Equal(t, "Forbidden", rec.Body.String())
			require.Equal(t, before, calls.Load())
			require.Equal(t, 200, response(r, "/health").Code)
			query := "w=50&fm=png"
			rec = response(r, "/image.png?"+query+"&s="+sign("/image.png", query))
			if enabled {
				require.Equal(t, 200, rec.Code, rec.Body.String())
			} else {
				require.NotEqual(t, 200, rec.Code)
			}
		})
	}
	// With compat disabled, an ordinary insecure imgproxy URL still renders.
	cfg := compatConfig(src.URL + "/")
	cfg.Handlers.Imgix.Compat = false
	r := router(t, newApp(t, &cfg))
	tr := translated(t, "/image.png?w=50&fm=png")
	rec := response(r, "/insecure/"+tr.Path(src.URL+"/image.png"))
	require.Equal(t, 200, rec.Code, rec.Body.String())
}

func TestSourceSecurityAndPrivateErrors(t *testing.T) {
	src, calls := sourceServer(t)
	cfg := compatConfig(src.URL + "/")
	cfg.Security.AllowedSources = []*regexp.Regexp{regexp.MustCompile(`^https://allowed\.invalid/`)}
	cfg.Server.DevelopmentErrorsMode = true
	r := router(t, newApp(t, &cfg))
	for _, query := range []string{"fm=json", "fm=png"} {
		rec := response(r, "/private-key?"+query)
		require.Equal(t, 403, rec.Code)
		require.NotContains(t, rec.Body.String(), src.URL)
		require.NotContains(t, rec.Body.String(), "private-key")
		require.NotContains(t, rec.Body.String(), "/app/")
	}
	require.Zero(t, calls.Load())
	// The processing entry point independently verifies the source too.
	app := newApp(t, &cfg)
	// Exercise via a normal adapter request above; direct ExecutePath below uses
	// the actual processing handler constructed with the same security checker.
	ph, err := processinghandler.New(app, nil, &cfg.Handlers.Processing)
	require.NoError(t, err)
	direct, err := server.NewRouter(&cfg.Server, app.Monitoring(), app.ErrorReporter())
	require.NoError(t, err)
	direct.GET("/*", func(id string, rw server.ResponseWriter, req *http.Request) *server.Error {
		return ph.ExecutePath(id, translated(t, "/private-key?fm=png").Path(src.URL+"/private-key"), rw, req, nil)
	}, direct.WithReportError)
	require.Equal(t, 404, response(direct, "/ignored").Code)
	require.Zero(t, calls.Load())
}

func TestWarningOnce(t *testing.T) {
	src, _ := sourceServer(t)
	cfg := compatConfig(src.URL + "/")
	r := router(t, newApp(t, &cfg))
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)
	for range 2 {
		require.Equal(t, 200, response(r, "/image.png?fm=json&x-unknown=one&fit=x-fit").Code)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "Ignoring unsupported imgix option" {
			require.Equal(t, "WARN", record["level"])
			counts[record["value"].(string)]++
		}
	}
	require.Equal(t, map[string]int{"x-unknown": 1, "x-fit": 1}, counts)
}

func TestConfigEnv(t *testing.T) {
	t.Setenv("IMGPROXY_IMGIX_COMPAT", "true")
	t.Setenv("IMGPROXY_IMGIX_SOURCE_PREFIX", "s3://bucket/")
	t.Setenv("IMGPROXY_IMGIX_SIGN_KEY", "secret")
	cfg, err := proxy.LoadConfigFromEnv(nil)
	require.NoError(t, err)
	require.Equal(t, imgix.Config{Compat: true, SourcePrefix: "s3://bucket/", SignKey: "secret"}, cfg.Handlers.Imgix)
	require.NoError(t, cfg.Validate())
	cfg.Handlers.Imgix.SourcePrefix = ""
	require.Error(t, cfg.Validate())
	cfg.Handlers.Imgix.Compat = false
	require.NoError(t, cfg.Validate())
	require.False(t, proxy.NewDefaultConfig().Handlers.Imgix.Compat)
	t.Setenv("IMGPROXY_IMGIX_COMPAT", "not-bool")
	_, err = imgix.LoadConfigFromEnv(nil)
	require.Error(t, err)
}

func TestVectorFitMax(t *testing.T) {
	data, err := os.ReadFile("testdata/vector.svg")
	require.NoError(t, err)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(data)
	}))
	defer src.Close()
	cfg := compatConfig(src.URL + "/")
	r := router(t, newApp(t, &cfg))
	for _, tc := range []struct {
		query string
		w, h  int
	}{
		{"w=1000", 100, 80}, {"h=1000", 100, 80}, {"w=1000&h=1000", 100, 80}, {"rect=90,70,40,40&w=1000", 11, 11},
	} {
		t.Run(tc.query, func(t *testing.T) {
			rec := response(r, "/vector.svg?fm=png&"+tc.query)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			out, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
			require.NoError(t, err)
			require.Equal(t, image.Rect(0, 0, tc.w, tc.h), out.Bounds())
		})
	}
}

func TestPDFIntegration(t *testing.T) {
	data, err := os.ReadFile("testdata/pages.pdf")
	require.NoError(t, err)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(data)
	}))
	defer src.Close()
	cfg := compatConfig(src.URL + "/")
	app := newApp(t, &cfg)
	_, _, err = app.OptionsParser().ParsePath(t.Context(), translated(t, "/pages.pdf?page=1").Path(src.URL+"/pages.pdf"), nil)
	if err != nil && strings.Contains(err.Error(), "pg") && strings.Contains(strings.ToLower(err.Error()), "unknown") {
		t.Skip("pg processing option is not available")
	}
	require.NoError(t, err)
	detected, err := app.ImageDataFactory().NewFromBytes(data)
	require.NoError(t, err)
	defer detected.Close()
	if !vips.SupportsLoad(detected.Format()) {
		t.Skip("libvips has no pdfload; run in the vips-pdf image")
	}
	r := router(t, app)
	require.JSONEq(t, `{"PDF":{"PageCount":3}}`, response(r, "/pages.pdf?fm=json").Body.String())
	for _, tc := range []struct {
		query string
		w, h  int
		c     color.NRGBA
	}{
		{"page=1", 612, 792, color.NRGBA{255, 0, 0, 255}},
		{"page=1&w=1224", 1224, 1584, color.NRGBA{255, 0, 0, 255}},
		// Observed imgix behavior on a Letter page: 1px-larger clip, off-page origin.
		{"page=1&rect=600,700,200,200", 13, 93, color.NRGBA{255, 0, 0, 255}},
		{"page=1&rect=700,900,50,50", 612, 792, color.NRGBA{255, 0, 0, 255}},
		{"page=2&w=300", 300, 150, color.NRGBA{0, 255, 0, 255}},
		{"page=2&rect=390,190,40,40", 11, 11, color.NRGBA{0, 255, 0, 255}},
		{"page=2&rect=400,0,40,40", 400, 200, color.NRGBA{0, 255, 0, 255}},
		{"page=3", 200, 400, color.NRGBA{0, 0, 255, 255}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			rec := response(r, "/pages.pdf?fm=png&"+tc.query)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			out, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
			require.NoError(t, err)
			require.Equal(t, image.Rect(0, 0, tc.w, tc.h), out.Bounds())
			require.Equal(t, tc.c, color.NRGBAModel.Convert(out.At(0, 0)))
		})
	}
	// A PDF with no fm renders as PNG (imgproxy can't save PDF).
	rec := response(r, "/pages.pdf?page=1&w=100")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	// A page past the end is PageOutOfRangeError, passed through.
	rec = response(r, "/pages.pdf?page=4&fm=png")
	require.Equal(t, 422, rec.Code, rec.Body.String())
}

func TestKeyTraversalAndUnfetchableKeys(t *testing.T) {
	// A local:// prefix is cleaned by http.Dir, so ".." segments would escape it.
	cfg := compatConfig("local:///public/")
	r := router(t, newApp(t, &cfg))
	for _, path := range []string{"/%2e%2e/private.png?fm=json", "/a/%2E%2E/%2e%2e/private.png?fm=png", "//private.png?fm=json"} {
		rec := response(r, path)
		require.Equal(t, 400, rec.Code, path)
		require.Contains(t, rec.Body.String(), "Invalid parameters: ")
	}
	// With the default "?" query separator the fetcher would cut this key.
	cfg = compatConfig("s3://bucket/")
	r = router(t, newApp(t, &cfg))
	rec := response(r, "/a%3Fb.png?fm=json")
	require.Equal(t, 400, rec.Code)
	require.Contains(t, rec.Body.String(), "can't be fetched")
}

func TestMetadataWorkerCanceled(t *testing.T) {
	src, calls := sourceServer(t)
	cfg := compatConfig(src.URL + "/")
	r := router(t, newApp(t, &cfg))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/image.png?fm=json", nil).WithContext(ctx))
	require.Equal(t, 499, rec.Code, rec.Body.String())
	require.Zero(t, calls.Load())
}

func TestMetadataLogsCompletion(t *testing.T) {
	src, _ := sourceServer(t)
	cfg := compatConfig(src.URL + "/")
	r := router(t, newApp(t, &cfg))
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)
	require.Equal(t, 200, response(r, "/image.png?fm=json").Code)
	var started, completed int
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		msg, _ := record["msg"].(string)
		switch {
		case strings.HasPrefix(msg, "Started /image.png?fm=json"):
			started++
		case strings.HasPrefix(msg, "Completed in ") && strings.HasSuffix(msg, "/image.png?fm=json"):
			completed++
			require.Equal(t, float64(200), record["status"])
		}
	}
	require.Equal(t, 1, started)
	require.Equal(t, 1, completed)
}
