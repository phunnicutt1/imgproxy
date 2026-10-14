// Package imgix implements an opt-in subset of the imgix Rendering API.
package imgix

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Rect is a source-pixel crop, before resizing.
type Rect struct{ X, Y, Width, Height int }

// Result is independent of configuration, logging, and source I/O.
// Rect needs source dimensions before rendering; call Resolve after download.
type Result struct {
	Key                    string
	Width, Height, Quality int
	Format                 string
	JSON                   bool
	Page                   int
	HasPage                bool
	Rect                   *Rect
	Warnings               []Warning
}

type Warning struct{ Kind, Value string }

// Translate validates and translates an imgix URL without side effects.
func Translate(u url.URL) (Result, error) {
	r := Result{Key: strings.TrimPrefix(u.Path, "/")}
	if err := validKey(r.Key); err != nil {
		return r, err
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return r, fmt.Errorf("invalid query")
	}
	// imgix accepts f as an alias for fit.
	if f, ok := q["f"]; ok {
		if _, dup := q["fit"]; dup {
			return r, fmt.Errorf("fit and f are aliases; send only one")
		}
		q["fit"] = f
		delete(q, "f")
	}
	names := make([]string, 0, len(q))
	for name := range q {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		values := q[name]
		switch name {
		// dpi only sets output density metadata in imgix (sent with HEIC
		// converting HEIC to PDF), so it is known and ignored without a warning.
		case "s", "expires", "ixlib", "dpi":
			continue
		case "w", "h", "q", "page", "rect", "fit", "fm":
			if len(values) != 1 {
				return r, fmt.Errorf("%s is given more than once", name)
			}
		default:
			r.Warnings = append(r.Warnings, Warning{"param", name})
			continue
		}
		value := values[0]
		switch name {
		case "w", "h", "q", "page":
			n, err := integer(value)
			if err != nil || n < 1 || (name == "q" && n > 100) {
				if name == "q" {
					return r, fmt.Errorf("q must be an integer from 1 to 100")
				}
				return r, fmt.Errorf("%s must be a positive integer", name)
			}
			switch name {
			case "w":
				r.Width = n
			case "h":
				r.Height = n
			case "q":
				r.Quality = n
			case "page":
				r.Page, r.HasPage = n-1, true
			}
		case "fit":
			// Absent fit means max (the common case). Every
			// other mode, clip included, is rendered as max and logged once.
			if value != "max" {
				r.Warnings = append(r.Warnings, Warning{"fit", value})
			}
		case "fm":
			switch value {
			case "json":
				r.JSON = true
			case "jpg", "jpeg":
				r.Format = "jpg"
			case "png", "webp", "avif", "gif":
				r.Format = value
			default:
				return r, fmt.Errorf("fm must be one of json, jpg, png, webp, avif, gif")
			}
		case "rect":
			// Only the integer form is supported. imgix also accepts keywords
			// (left, center, top, ...), unit scalars and `full`; real clients rarely
			// sends those, so they are rejected rather than half-implemented.
			parts := strings.Split(value, ",")
			if len(parts) != 4 {
				return r, fmt.Errorf("rect must be x,y,w,h")
			}
			var n [4]int
			for i, part := range parts {
				n[i], err = integer(part)
				if err != nil || (i >= 2 && n[i] < 1) {
					return r, fmt.Errorf("rect supports integers only: x,y >= 0 and w,h >= 1")
				}
			}
			r.Rect = &Rect{n[0], n[1], n[2], n[3]}
		}
	}
	return r, nil
}

// Keep values within the signed 32-bit range accepted by libvips dimensions.
func integer(digits string) (int, error) {
	if digits == "" {
		return 0, fmt.Errorf("invalid integer")
	}
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid integer")
		}
	}
	n, err := strconv.ParseInt(digits, 10, 32)
	return int(n), err
}

// validKey rejects keys that could address anything other than the literal
// object: empty, "." or ".." segments (local storage cleans those, so
// /%2e%2e/x could escape the prefix), plus empty segments from "//" or a
// leading or trailing "/".
func validKey(key string) error {
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("object key must not have empty, . or .. path segments")
		}
	}
	return nil
}

// SourceURL joins the source prefix and the decoded object key.
//
// For http(s) prefixes the key is escaped as URL path data. Storage schemes
// (s3, gs, abs, swift, local) take the key literally: the fetcher derives the
// storage key from the raw source string (see fetcher/transport.GetBucketAndKey),
// so escaping here would make /a%20b.pdf fetch the object "a%20b.pdf".
func (r Result) SourceURL(prefix string) (string, error) {
	u, err := url.Parse(prefix)
	if err != nil || u.Scheme == "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("invalid source prefix")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return prefix + r.Key, nil
	}
	u.Path += r.Key
	u.RawPath = ""
	return u.String(), nil
}

// Path is a signature-free imgproxy path for processing.Handler.ExecutePath.
func (r Result) Path(source string) string {
	parts := []string{fmt.Sprintf("rs:fit:%d:%d", r.Width, r.Height), "el:0"}
	if r.Quality != 0 {
		parts = append(parts, fmt.Sprintf("q:%d", r.Quality))
	}
	if r.Format != "" {
		parts = append(parts, "f:"+r.Format)
	}
	if r.HasPage {
		parts = append(parts, fmt.Sprintf("pg:%d", r.Page))
	}
	if c := r.Rect; c != nil {
		parts = append(parts, fmt.Sprintf("c:%d:%d:nowe:%d:%d", c.Width, c.Height, c.X, c.Y))
	}
	return strings.Join(parts, "/") + "/" + base64.RawURLEncoding.EncodeToString([]byte(source))
}

// Resolve clips a crop to the selected page/image and enforces fit=max for
// vectors too (upstream deliberately permits vector enlargement with el:0).
//
// Edge behavior copies what was observed on real imgix (not in imgix's docs): an origin outside the
// image ignores rect, and a rect that overflows an edge comes back 1px larger
// than naive clipping (rect=600,700,200,200 on 612x792 gives 13x93). The extra
// pixel is taken by moving the origin back by one.
func (r Result) Resolve(width, height int) Result {
	if c := r.Rect; c != nil {
		if c.X < 0 || c.Y < 0 || c.X >= width || c.Y >= height {
			r.Rect = nil
		} else {
			clipped := *c
			clipped.X, clipped.Width = clipEdge(c.X, c.Width, width)
			clipped.Y, clipped.Height = clipEdge(c.Y, c.Height, height)
			r.Rect = &clipped
			width, height = clipped.Width, clipped.Height
		}
	}
	if r.Width > width {
		r.Width = width
	}
	if r.Height > height {
		r.Height = height
	}
	return r
}

func clipEdge(start, length, limit int) (int, int) {
	if start+length <= limit {
		return start, length
	}
	if start == 0 {
		return 0, limit
	}
	return start - 1, limit - start + 1
}
