package imgix_test

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/imgproxy/imgproxy/v4/fetcher/transport"
	"github.com/imgproxy/imgproxy/v4/handlers/imgix"
	optionsparser "github.com/imgproxy/imgproxy/v4/options/parser"
	"github.com/stretchr/testify/require"
)

func translated(t *testing.T, raw string) imgix.Result {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	r, err := imgix.Translate(*u)
	require.NoError(t, err)
	return r
}

func TestTranslate(t *testing.T) {
	for _, tc := range []struct {
		name, query, path string
		warnings          int
		json              bool
	}{
		{"neither", "", "rs:fit:0:0/el:0", 0, false},
		{"width", "w=300", "rs:fit:300:0/el:0", 0, false},
		{"height", "h=400", "rs:fit:0:400/el:0", 0, false},
		{"max", "fit=max&w=300&h=400", "rs:fit:300:400/el:0", 0, false},
		{"unknown fit", "fit=localized", "rs:fit:0:0/el:0", 1, false},
		{"clip", "fit=clip", "rs:fit:0:0/el:0", 1, false},
		{"crop", "fit=crop", "rs:fit:0:0/el:0", 1, false},
		{"quality", "q=75", "rs:fit:0:0/el:0/q:75", 0, false},
		{"quality min", "q=1", "rs:fit:0:0/el:0/q:1", 0, false},
		{"quality max", "q=100", "rs:fit:0:0/el:0/q:100", 0, false},
		{"jpg", "fm=jpg", "rs:fit:0:0/el:0/f:jpg", 0, false},
		{"jpeg", "fm=jpeg", "rs:fit:0:0/el:0/f:jpg", 0, false},
		{"png", "fm=png", "rs:fit:0:0/el:0/f:png", 0, false},
		{"webp", "fm=webp", "rs:fit:0:0/el:0/f:webp", 0, false},
		{"avif", "fm=avif", "rs:fit:0:0/el:0/f:avif", 0, false},
		{"gif", "fm=gif", "rs:fit:0:0/el:0/f:gif", 0, false},
		{"json", "fm=json", "rs:fit:0:0/el:0", 0, true},
		{"page one", "page=1", "rs:fit:0:0/el:0/pg:0", 0, false},
		{"page two", "page=2", "rs:fit:0:0/el:0/pg:1", 0, false},
		{"rect", "rect=10,20,30,40", "rs:fit:0:0/el:0/c:30:40:nowe:10:20", 0, false},
		{"ignored", "s=x&expires=1&ixlib=php", "rs:fit:0:0/el:0", 0, false},
		{"unknown", "auto=format&foo=a&foo=b", "rs:fit:0:0/el:0", 2, false},
		{"f alias max", "f=max&w=10", "rs:fit:10:0/el:0", 0, false},
		{"f alias clip", "f=clip", "rs:fit:0:0/el:0", 1, false},
		{"dpi ignored silently", "dpi=300&w=10", "rs:fit:10:0/el:0", 0, false},
		{"all", "fit=max&w=300&h=400&q=75&fm=jpg&page=2&rect=10,20,30,40", "rs:fit:300:400/el:0/q:75/f:jpg/pg:1/c:30:40:nowe:10:20", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := translated(t, "/some/key.pdf?"+tc.query)
			require.Equal(t, tc.path+"/"+base64.RawURLEncoding.EncodeToString([]byte("s3://bucket/some/key.pdf")), r.Path("s3://bucket/some/key.pdf"))
			require.Len(t, r.Warnings, tc.warnings)
			require.Equal(t, tc.json, r.JSON)
			require.Equal(t, "some/key.pdf", r.Key)
		})
	}
}

func TestBadParameters(t *testing.T) {
	for _, query := range []string{
		"w=0", "w=-1", "w=.5", "w=1.5", "w=", "w=2147483648", "h=0", "h=0.5",
		"w=%2B1", "h=nan", "q=0", "q=101", "q=1.1", "q=", "page=0", "page=-1", "page=1.1", "page=bad", "page=",
		"fm=pdf", "fm=tiff", "fm=", "w=1&w=2", "page=1&page=2", "rect=0,0,2,2&rect=0,0,1,1",
		"rect=1,2,3", "rect=1,2,3,4,5", "rect=-1,0,10,10", "rect=0,-1,10,10", "fit=max&f=max", "rect=1,2,0,4", "rect=1,2,3,-4", "rect=0.5,0,2,2",
		"rect=left,top,full,full", "rect=0,0,1.5,2", "rect=0,,1,2", "rect=0, 0,1,2", "rect=0,0,2147483648,2",
		"w=%xx", "w=1;h=2",
	} {
		t.Run(query, func(t *testing.T) {
			_, err := imgix.Translate(url.URL{Path: "/key", RawQuery: query})
			require.Error(t, err)
		})
	}
}

func TestResolveRect(t *testing.T) {
	for _, tc := range []struct {
		rect string
		want *imgix.Rect
	}{
		{"10,20,30,40", &imgix.Rect{10, 20, 30, 40}},
		{"80,70,20,10", &imgix.Rect{80, 70, 20, 10}},
		// Overflowing edges come back 1px larger than naive clipping, as
		// observed on real imgix.
		{"80,70,40,50", &imgix.Rect{79, 69, 21, 11}},
		{"0,0,999,999", &imgix.Rect{0, 0, 100, 80}},
		{"99,79,10,10", &imgix.Rect{98, 78, 2, 2}},
		{"100,0,10,10", nil}, {"0,80,10,10", nil}, {"101,81,10,10", nil},
	} {
		t.Run(tc.rect, func(t *testing.T) {
			r := translated(t, "/key?rect="+tc.rect)
			original := *r.Rect
			resolved := r.Resolve(100, 80)
			require.Equal(t, tc.want, resolved.Rect)
			require.Equal(t, original, *r.Rect, "Resolve must not mutate the original")
			if tc.want == nil {
				require.NotContains(t, resolved.Path("source"), "/c:")
			}
		})
	}
	for _, query := range []string{"w=1000", "h=1000", "w=1000&h=1000", "rect=80,70,100,100&w=1000&h=1000"} {
		t.Run(query, func(t *testing.T) {
			r := translated(t, "/key?"+query).Resolve(100, 80)
			w, h := 100, 80
			if r.Rect != nil {
				w, h = 21, 11
			}
			if r.Width != 0 {
				require.Equal(t, w, r.Width)
			}
			if r.Height != 0 {
				require.Equal(t, h, r.Height)
			}
		})
	}
}

func TestEncodedKeys(t *testing.T) {
	cfg := optionsparser.NewDefaultConfig()
	parser, err := optionsparser.New(t.Context(), &cfg)
	require.NoError(t, err)
	for _, key := range []string{"folder/a b+雪?.png", "a b.pdf", "50%.pdf", "a+b.png", "100%/#?.png", "literal%20name.png", "雪.pdf"} {
		t.Run(key, func(t *testing.T) {
			u := url.URL{Path: "/" + key}
			r := translated(t, u.String())
			require.Equal(t, key, r.Key)

			// Storage schemes: the bucket/key the fetcher derives must be the
			// decoded key exactly. With no query separator every key round-trips.
			src, err := r.SourceURL("s3://bucket/prefix/")
			require.NoError(t, err)
			_, decoded, err := parser.ParsePath(t.Context(), r.Path(src), nil)
			require.NoError(t, err)
			require.Equal(t, src, decoded)
			parsed, err := url.Parse(transport.EscapeURL(decoded))
			require.NoError(t, err)
			bucket, objectKey, _ := transport.GetBucketAndKey(parsed, "")
			require.Equal(t, "bucket", bucket)
			require.Equal(t, "prefix/"+key, objectKey)
			// With the default "?" separator, keys without "?" still match.
			if !strings.Contains(key, "?") {
				require.Equal(t, "prefix/"+key, transport.SourcePath(decoded, "?"))
			}

			// http(s) prefixes keep normal URL escaping.
			src, err = r.SourceURL("https://origin.example/prefix/")
			require.NoError(t, err)
			parsed, err = url.Parse(src)
			require.NoError(t, err)
			require.Equal(t, "/prefix/"+key, parsed.Path)
			require.Empty(t, parsed.RawQuery)
			require.Empty(t, parsed.Fragment)
		})
	}
}

func TestBadKeys(t *testing.T) {
	for _, path := range []string{
		"/", "/%2e%2e/private.png", "/../private.png", "/a/../b.png", "/a/%2E%2E/b.png",
		"/./a.png", "/a/./b.png", "//a.png", "/a//b.png", "/a/", "/%2Fa.png",
	} {
		t.Run(path, func(t *testing.T) {
			u, err := url.Parse(path)
			require.NoError(t, err)
			_, err = imgix.Translate(*u)
			require.Error(t, err)
		})
	}
}

func TestPageOptionCompatibility(t *testing.T) {
	cfg := optionsparser.NewDefaultConfig()
	p, err := optionsparser.New(t.Context(), &cfg)
	require.NoError(t, err)
	r := translated(t, "/key?page=1")
	_, _, err = p.ParsePath(t.Context(), r.Path("https://example.com/key"), nil)
	if err != nil && strings.Contains(err.Error(), "pg") && strings.Contains(strings.ToLower(err.Error()), "unknown") {
		t.Skip("pg processing option is not available")
	}
	require.NoError(t, err)
}

// Examples observed on real imgix with a 612x792 Letter page.
func TestResolveRectImgixExamples(t *testing.T) {
	r := translated(t, "/key.pdf?rect=600,700,200,200").Resolve(612, 792)
	require.Equal(t, &imgix.Rect{599, 699, 13, 93}, r.Rect)
	r = translated(t, "/key.pdf?rect=700,900,50,50").Resolve(612, 792)
	require.Nil(t, r.Rect)
}

// Typical imgix query strings sent by a client library (ksorted, fit=max
// default, expires and s appended), from the current app code.
func TestTranslateAppURLs(t *testing.T) {
	const sig = "&expires=1760457600&s=0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name, url, path string
		json            bool
	}{
		{"page count",
			"/123.pdf?fit=max&fm=json", "rs:fit:0:0/el:0", true},
		{"page thumbnail",
			"/123.pdf?fit=max&fm=jpg&h=640&page=1&q=75&w=640", "rs:fit:640:640/el:0/q:75/f:jpg/pg:0", false},
		{"flattened page thumbnail",
			"/123-flattened-4.pdf?fit=max&fm=jpg&h=640&page=1&q=75&w=640", "rs:fit:640:640/el:0/q:75/f:jpg/pg:0", false},
		{"default page render",
			"/123.pdf?fit=max&fm=jpg&h=1056&page=3&q=75&w=1056", "rs:fit:1056:1056/el:0/q:75/f:jpg/pg:2", false},
		{"pdf report photo default",
			"/123-flattened-4.pdf?fit=max&fm=jpg&h=2012&page=1&q=15&w=2012", "rs:fit:2012:2012/el:0/q:15/f:jpg/pg:0", false},
		{"crop",
			"/123-flattened-4.pdf?fit=max&fm=jpg&page=2&q=100&rect=88,120,140,60", "rs:fit:0:0/el:0/q:100/f:jpg/pg:1/c:140:60:nowe:88:120", false},
		{"crop, no fm/q",
			"/123-flattened-4.pdf?fit=max&page=2&rect=88,120,140,60", "rs:fit:0:0/el:0/pg:1/c:140:60:nowe:88:120", false},
		{"preview",
			"/preview_9.pdf?fit=max&fm=jpg&h=90&q=75&w=272", "rs:fit:272:90/el:0/q:75/f:jpg", false},
		{"preview png",
			"/preview_9.pdf?fit=max&fm=png&h=1024&page=1&w=1024", "rs:fit:1024:1024/el:0/f:png/pg:0", false},
		{"photo",
			"/123.jpg?fit=max&fm=jpg&h=1024&q=75&w=1024", "rs:fit:1024:1024/el:0/q:75/f:jpg", false},
		{"heic to pdf conversion with dpi",
			"/123.heic?dpi=300&fit=max&fm=jpg&h=1024&q=75&w=1024", "rs:fit:1024:1024/el:0/q:75/f:jpg", false},
		{"project logo, no fm",
			"/logo_5.png?fit=max&h=40", "rs:fit:0:40/el:0", false},
		{"default signature png",
			"/sig-flattened-1.pdf?fit=max&fm=png&page=1", "rs:fit:0:0/el:0/f:png/pg:0", false},
		{"default signature, no fm (Profile)",
			"/sig-flattened-1.pdf?fit=max&page=1", "rs:fit:0:0/el:0/pg:0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := translated(t, tc.url+sig)
			require.Empty(t, r.Warnings)
			require.Equal(t, tc.json, r.JSON)
			require.Equal(t, tc.path+"/"+base64.RawURLEncoding.EncodeToString([]byte("s3://b/"+r.Key)), r.Path("s3://b/"+r.Key))
		})
	}
}
