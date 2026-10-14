# Imgix compatibility

This opt-in adapter answers a subset of the imgix Rendering API, intended for
local development against imgix-style URLs. It translates requests into imgproxy
option paths and calls `handlers/processing.Handler.ExecutePath`. It is not a
complete implementation of the imgix Rendering API.

## Configuration

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `IMGPROXY_IMGIX_COMPAT` | `false` | Replace the processing handler on `GET /*` with the imgix handler. |
| `IMGPROXY_IMGIX_SOURCE_PREFIX` | empty | Required when compatibility is enabled; absolute source URL prefix, normally ending in `/`, e.g. `s3://my-bucket/`. No query or fragment. |
| `IMGPROXY_IMGIX_SIGN_KEY` | empty | Optional imgix signing token, used literally (not hex-decoded). When set, missing or invalid `s` returns 403. |

Existing S3 configuration still applies: `IMGPROXY_USE_S3=true`,
`IMGPROXY_S3_ENDPOINT=http://minio:9000`, and
`IMGPROXY_S3_ENDPOINT_USE_PATH_STYLE=true`. Configure existing source allowlists,
credentials, and fetcher network restrictions as usual.

`/health`, a configured health path, the landing page, favicon, `.well-known`,
HEAD, and OPTIONS routes retain their upstream behavior. When compatibility is
false, the original processing route and URL signature checks are used.

The object key is the URL-decoded request path with its first `/` removed. Keys
with an empty, `.` or `..` segment (from `%2e%2e`, `//`, or a leading or
trailing `/`) return 400, so a key can't escape the prefix (a `local://` prefix
is cleaned by `http.Dir`).

The prefix and key are concatenated without path cleaning or an added
separator. For storage prefixes (`s3://`, `gs://`, `abs://`, `swift://`,
`local://`) the key is appended **literally**: the fetcher derives the storage
key from the raw source string (`fetcher/transport.GetBucketAndKey`), so the
object fetched for `/a%20b.pdf` is `a b.pdf`, and `%`, `+`, `#` and Unicode stay
part of the key. The handler then checks that the fetcher resolves the source to
exactly that key, and returns 400 when it wouldn't. That happens for a key
containing the source URL query separator (`IMGPROXY_SOURCE_URL_QUERY_SEPARATOR`,
`?` by default), which the fetcher would cut. For `http(s)://` prefixes the key
is escaped as normal URL path data. Either way the source travels in the
internal imgproxy path as unpadded URL-safe base64. Keys are never interpolated
into processing options.

## Parameter mapping

| Imgix input | Translation / behavior |
| --- | --- |
| `fit=max`, or absent | `rs:fit:W:H/el:0`. `f` is accepted as an alias of `fit`; sending both returns 400. |
| Any other `fit`, including `clip` and `crop` | Same as max; WARN once per distinct value across the process. |
| `w`, `h` | Positive decimal integers; absent becomes zero/unconstrained. Fractional ratios, zero, negatives, empty values, and overflow return 400. |
| `q=1..100` | `q:Q`; invalid values return 400. |
| `fm=jpg`, `jpeg`, `png`, `webp`, `avif`, `gif` | `f:FORMAT`; `jpeg` normalizes to `jpg`. |
| no `fm` | PDF sources render as PNG (imgproxy can't save PDF). Image sources keep imgproxy's choice: the source format when it is a preferred format (JPEG, PNG, GIF), otherwise JPEG (HEIC, for example). |
| `fm=json` | Source metadata, independent of resize/crop/page parameters after validation. |
| Other `fm`, including `pdf` | 400. |
| `page=N` | `pg:N-1`; page is 1-based. Absent means no `pg` (first page). Non-integer or less than 1 returns 400. A page past the end returns 422. |
| `rect=x,y,w,h` | Integers only: x,y >= 0, w,h >= 1, in source pixels (PDF points at 72 DPI), top-left origin; resolved after download, then `c:w:h:nowe:x:y`. Keywords, unit scalars, floats and `full` return 400. |
| `s`, `expires`, `ixlib` | Ignored by translation; `s` is verified separately when configured. Expiration is not enforced. |
| `dpi` | Ignored without a warning. In imgix it only sets output density metadata. |
| All other names | Ignored; WARN once per decoded parameter name across the process (`sync.Map`). |

Recognized transformation parameters must occur once; duplicates return 400.
Malformed query encoding returns 400. Ignored parameters may repeat. Integers
are limited to signed 32-bit range, matching libvips dimensions. Whitespace,
leading plus signs, decimal fractions, and rectangle aliases are rejected.
Rectangle widths and heights must be positive, and origins must not be negative
(imgix's parameter spec has `min: 0`). 400 responses name the bad parameter,
e.g. `Invalid parameters: rect supports integers only: x,y >= 0 and w,h >= 1`.

`Translate(url.URL) (Result, error)` is pure: it returns warning descriptions,
with no logging, environment access, mutation of global state, or downloads.
`Result.Resolve` is also pure and does not mutate the original rectangle.

## Crop and resize decisions

Upstream `processing.cropImage` limits crop dimensions to the full image and
`processing.calcPosition` clamps the origin to `width-cropWidth` /
`height-cropHeight`. Thus a raw `c:40:50:nowe:81:71` on a 100x80 image would shift
the crop inward, not clip it to the bottom-right edge. Relying on that behavior
would select the wrong pixels.

The adapter's `PreparePath` callback runs after downloading, under the existing
processing worker. It reads dimensions of the selected page, then:

- Drops `rect` when x or y is at/beyond the source edge (the full image comes
  back, as imgix does).
- When w or h overruns an edge, returns a crop **1px larger than naive
  clipping** by moving that origin back one pixel: width `sourceWidth-x+1` from
  `x-1` (and the same for height). With x or y at 0 the crop is the full
  dimension. This matches behavior observed on real imgix
  (`rect=600,700,200,200` on a 612x792 page returns 13x93); it isn't documented.
- Emits the bounded `c:...:nowe:x:y` option for the ordinary pipeline, which crops
  before resizing. Geometry respects the source's EXIF orientation.

The callback and rendering reuse the same `ImageData`; there is no second fetch
or opportunity for source contents to change between the dimension check and
rendering. The upstream conditional response, origin headers, ETag, worker,
download limits, and processing flow remain in use.

Upstream `processing.Context.calcScale` permits vector enlargement even when
`el:0`. **PDFs are allowed to scale up** to the requested `w`/`h` box: imgix's
PDF documentation says "Vectors will scale before rasterizing", and large page
renders need the extra resolution for legibility. Other vectors (SVG) are capped
to the page/crop dimensions so `fit=max` never enlarges them. Raster sources keep
the normal no-enlarge behavior and, without a crop, need no extra metadata load.

## Signing and errors

The signature is lowercase hex MD5 of `sign_key + path`, followed by
`? + raw_query_without_s` only if that query is nonempty. `expires` stays in the
signed query. `path` is the **decoded** request path only (the raw object key,
not URL-encoded). The escaped form is deliberately not accepted: otherwise a
signature for `/a%20b.pdf` (key `a b.pdf`) would also validate `/a%2520b.pdf`
(key `a%20b.pdf`). Removing `s` preserves the order, spelling, and encoding of
all other raw query components. An encoded parameter name `%73` is treated as
`s`; duplicate signature parameters fail. Comparison uses constant-time
comparison. Unknown and ignored parameters still participate in the signature.
The request URL is never rewritten.

`ExecutePath` accepts a **trusted internal path without a signature component**;
it deliberately does not call `VerifySignature`. The imgix signature replaces
the imgproxy HMAC signature. Both the adapter and the entry point still call
`VerifySourceURL`, and fetcher source-address restrictions still apply. Never
expose `ExecutePath` directly as a public unsigned route.

Bad imgix parameters return 400, missing objects 404, and signature/source
allowlist failures 403. Processing/download failures retain upstream status
codes but use generic response messages. Error causes and stacks are removed
from adapter errors so development error pages cannot expose source URLs,
object keys, or filesystem paths.

## Metadata

`fm=json` downloads through the same `ImageDataFactory.DownloadAsync` used by
the processing handler, with source validation, request cookies, source size
limits, worker acquisition, dimension checks, and request timeout checks. A
failed worker acquisition is classified like the processing handler's (timeout
or 499 for a canceled request), and a successful response logs `Completed`. It
loads **page zero only** using `vips.Image.Load(data, 1, 0, 1)` and returns 200
with `Content-Type: application/json`:

```json
{"PDF":{"PageCount":3}}
```

```json
{"PixelWidth":100,"PixelHeight":80}
```

Image keys follow imgix's documented `fm=json` example. imgix's docs don't show
PDF metadata; `{"PDF":{"PageCount":N}}` is the shape this adapter returns. The
page count is read with `img.Pages()` right after `vips.Image.Load`, before
anything can strip `n-pages`.

`MetadataLoader` in `handlers/imgix/metadata.go` is injectable for tests. PDF
identification uses `%PDF-` magic or `application/pdf`.

## Example request shapes

Typical imgix-style requests this adapter is tested against (each is a row in
`TestTranslateAppURLs`; signed URLs also carry `expires=<unix>&s=<md5>`):

| Use | Query |
| --- | --- |
| PDF page count | `fit=max&fm=json` |
| PDF page thumbnail | `fit=max&fm=jpg&h=640&page=1&q=75&w=640` |
| Full PDF page | `fit=max&fm=jpg&h=1056&page=N&q=75&w=1056` |
| Cropped region of a PDF page | `fit=max&fm=jpg&page=N&q=100&rect=x,y,w,h` (or without `fm`/`q`) |
| Small first-page preview | `fit=max&fm=jpg&h=90&q=75&w=272` |
| Photo | `fit=max&fm=jpg&h=1024&q=75&w=1024` |
| HEIC conversion | `dpi=R&fit=max&fm=jpg&h=M&q=75&w=M` |
| Logo (no `fm`) | `fit=max&h=40` |

## Public documentation cross-check (2026-10-14)

| Assumption | Evidence and result |
| --- | --- |
| Page numbering is 1-based | Confirmed by imgix's machine-readable parameter spec ([`imgix-url-params` parameters.json](https://github.com/imgix/imgix-url-params/blob/master/dist/parameters.json) v11.36.0: `page` integer, min 1, default 1). The [PDF page documentation](https://docs.imgix.com/en-US/apis/rendering/pdf/page) doesn't state the base. |
| PDFs use 72 DPI | Not in imgix's docs. Matches observed imgix output: a Letter page renders as 612x792 and `rect` uses PDF points. |
| `fit=max` on PDFs | [Fit reference](https://docs.imgix.com/en-US/apis/rendering/size/fit) says max never enlarges, with no vector caveat. The PDF page says "Vectors will scale before rasterizing". This adapter lets PDFs scale up to the w/h box. |
| Off-page rect is ignored; overruns clipped | Not in the [rect reference](https://docs.imgix.com/en-US/apis/rendering/size/rect). Observed on real imgix, including the 1px-larger clip (13x93). |
| Rect precedes resize | Confirmed by the rect reference. |
| JSON key names | The [fm reference](https://docs.imgix.com/en-US/apis/rendering/format/fm) example shows `PixelWidth` and `PixelHeight` (no `Content-Type` key, so none is emitted). `PDF.PageCount` isn't in imgix's docs. |

**Documented API differences:** imgix's rect allows keywords (`left`,
`center`, `top`, ...), unit scalars, floats and `full`; this adapter
deliberately rejects them with 400. The current imgix default fit is `clip`;
this adapter defaults to `max`.

## Verification

All tests and fixtures are contained under `handlers/imgix/`. The suite checks
mappings, invalid inputs, raw signing, escaped keys, actual crop pixels and
resize dimensions, vector no-enlarge behavior, mocked/real image metadata,
route selection, signature isolation, source allowlists, and once-only warnings.
PDF tests need a libvips with `pdfload` (build the `vips-pdf` target of
`docker/Dockerfile.pdf`) and skip without it.

```sh
docker build -f docker/Dockerfile.pdf --target vips-pdf -t imgproxy-imgix:vips-pdf .
docker run --rm -v "$PWD":/app -w /app imgproxy-imgix:vips-pdf bash -c 'go vet ./... && go test ./...'
```

Fixtures in `testdata/pages.pdf` and `testdata/vector.svg` are synthetic.
