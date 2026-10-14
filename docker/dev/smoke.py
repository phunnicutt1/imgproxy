#!/usr/bin/env python3
"""Smoke test: imgix-style requests against the imgproxy-imgix dev stack.

Asserts real results (dimensions, content types, page counts), not just HTTP 200.
Usage: smoke.py [base_url]   (default http://localhost:8089)
"""
import json
import struct
import sys
import urllib.error
import urllib.request

BASE = (sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8089").rstrip("/")
PDF = "/fixtures/test-3pages.pdf"
PNG = "/fixtures/test-800x600.png"

failures = 0


def get(path):
    try:
        with urllib.request.urlopen(BASE + path, timeout=60) as r:
            return r.status, r.headers.get("Content-Type", ""), r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers.get("Content-Type", ""), e.read()


def dims(body):
    if body[:8] == b"\x89PNG\r\n\x1a\n":
        return struct.unpack(">II", body[16:24])
    if body[:2] == b"\xff\xd8":
        i = 2
        while i < len(body):
            marker, size = body[i + 1], struct.unpack(">H", body[i + 2:i + 4])[0]
            if marker in (0xC0, 0xC1, 0xC2):
                h, w = struct.unpack(">HH", body[i + 5:i + 9])
                return w, h
            i += 2 + size
    return None


def check(name, cond, detail=""):
    global failures
    print(("PASS " if cond else "FAIL ") + name + ("" if cond else f"  -> {detail}"))
    if not cond:
        failures += 1


def expect_image(name, path, ctype, size):
    status, ct, body = get(path)
    got = dims(body) if status == 200 else None
    check(name, status == 200 and ct.startswith(ctype) and got == size,
          f"status={status} ct={ct} dims={got} body={body[:200]!r}")
    return body


# Page count (fm=json)
status, ct, body = get(PDF + "?fm=json")
try:
    count = json.loads(body)["PDF"]["PageCount"]
except Exception:
    count = None
check("fm=json PDF -> {PDF:{PageCount:3}}", status == 200 and "json" in ct and count == 3,
      f"status={status} ct={ct} body={body[:200]!r}")

status, ct, body = get(PNG + "?fm=json")
try:
    pw = json.loads(body).get("PixelWidth")
except Exception:
    pw = None
check("fm=json PNG -> PixelWidth 800", status == 200 and pw == 800, f"status={status} body={body[:200]!r}")

# PDF page rendering (72 DPI: Letter = 612x792)
p1 = expect_image("page=1 native size, jpg", PDF + "?page=1&fm=jpg&q=60", "image/jpeg", (612, 792))
expect_image("page=2 fit=max 300x300 png", PDF + "?page=2&w=300&h=300&fit=max&fm=png", "image/png", (232, 300))
a = get(PDF + "?page=1&w=200&fm=png")[2]
b = get(PDF + "?page=3&w=200&fm=png")[2]
check("page=1 and page=3 differ", a != b and len(a) > 0 and len(b) > 0)
expect_image("no page param = first page", PDF + "?w=200&fm=png", "image/png", (200, 259))
status, _, _ = get(PDF + "?page=4&fm=png")
check("page out of range -> 4xx", 400 <= status < 500, f"status={status}")

# rect crops (PDF points), applied before resize, imgix edge behavior.
# Overflow sizes follow the app's documented observation of real imgix
# on real imgix: rect=600,700,200,200 on 612x792 -> 13x93,
# i.e. one pixel more than naive clipping in each overflowing dimension.
expect_image("rect quarter page", PDF + "?page=1&rect=0,0,306,396&fm=png", "image/png", (306, 396))
expect_image("rect then resize", PDF + "?page=1&rect=0,0,306,396&w=153&fm=png", "image/png", (153, 198))
expect_image("rect overflow (imgix-observed 13x93)", PDF + "?page=1&rect=600,700,200,200&fm=png",
             "image/png", (13, 93))
expect_image("rect overflowing edge is clipped (+1)", PDF + "?page=1&rect=306,396,1000,1000&fm=png",
             "image/png", (307, 397))
expect_image("rect origin off-page -> full image", PDF + "?page=1&rect=700,900,50,50&fm=png",
             "image/png", (612, 792))

# PDFs are vectors: they scale up to the requested box (raster sources never enlarge)
expect_image("pdf scales up to fit=max box", PDF + "?page=1&w=2012&h=2012&fm=jpg&q=15",
             "image/jpeg", (1555, 2012))

# Typical client query shapes (ksorted, fit=max default, expires + s appended)
SIG = "&expires=4102444800&s=0123456789abcdef0123456789abcdef"
expect_image("client: page region crop", PDF + "?fit=max&fm=jpg&page=2&q=100&rect=100,100,200,150" + SIG,
             "image/jpeg", (200, 150))
expect_image("client: crop without fm/q -> png", PDF + "?fit=max&page=2&rect=100,100,200,150" + SIG,
             "image/png", (200, 150))
expect_image("client: page thumbnail", PDF + "?fit=max&fm=jpg&h=640&page=1&q=75&w=640" + SIG,
             "image/jpeg", (495, 640))
expect_image("client: default page render", PDF + "?fit=max&fm=jpg&h=1056&page=3&q=75&w=1056" + SIG,
             "image/jpeg", (816, 1056))
expect_image("client: small preview (no page -> first page)", PDF + "?fit=max&fm=jpg&h=90&q=75&w=272" + SIG,
             "image/jpeg", (70, 90))
expect_image("client: pdf without fm -> png", PDF + "?fit=max&page=1" + SIG,
             "image/png", (612, 792))
expect_image("client: dpi accepted and ignored", PNG + "?dpi=300&fit=max&fm=jpg&h=100&q=75&w=100" + SIG,
             "image/jpeg", (100, 75))
expect_image("client: logo (no fm keeps png)", PNG + "?fit=max&h=40" + SIG, "image/png", (53, 40))
status, ct, body = get(PDF + "?fit=max&fm=json" + SIG)
try:
    count = json.loads(body)["PDF"]["PageCount"]
except Exception:
    count = None
check("client: page count URL", status == 200 and count == 3, f"status={status} body={body[:200]!r}")

# Raster input, fit=max never enlarges
expect_image("png w=400 keeps aspect", PNG + "?w=400&fm=png", "image/png", (400, 300))
expect_image("png fit=max does not enlarge", PNG + "?w=2000&h=2000&fit=max&fm=png", "image/png", (800, 600))
expect_image("png -> jpg", PNG + "?w=100&fm=jpg&q=50", "image/jpeg", (100, 75))

# Signing params and unknown params are ignored, missing objects are 404
expect_image("s/expires/ixlib ignored", PNG + "?w=100&fm=png&s=deadbeef&expires=1&ixlib=php-3.3.1",
             "image/png", (100, 75))
expect_image("unknown param ignored", PNG + "?w=100&fm=png&blur=20", "image/png", (100, 75))
status, _, _ = get("/fixtures/does-not-exist.pdf?page=1")
check("missing object -> 404", status == 404, f"status={status}")

print(f"\n{'OK' if failures == 0 else 'FAILED'}: {failures} failure(s)")
sys.exit(1 if failures else 0)
