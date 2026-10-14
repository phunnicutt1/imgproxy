#!/usr/bin/env python3
"""Generate smoke-test fixtures with the standard library only.

  test-3pages.pdf  3 US Letter pages (612x792 pt), page N shows a large "N"
                   on a differently tinted background so pages are distinguishable.
  test-800x600.png 800x600 opaque RGB gradient.

Usage: make_fixtures.py <out_dir>
"""
import os
import struct
import sys
import zlib


def make_pdf(path):
    tints = ["1 0.9 0.9", "0.9 1 0.9", "0.9 0.9 1"]
    objs = []  # index i -> object number i+1

    def add(body):
        objs.append(body)
        return len(objs)

    catalog = add(None)
    pages = add(None)
    font = add(b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold >>")
    page_ids = []
    for n in range(1, 4):
        stream = (
            f"{tints[n - 1]} rg 0 0 612 792 re f\n"
            f"0 g BT /F1 400 Tf 196 250 Td ({n}) Tj ET\n"
        ).encode()
        content = add(b"<< /Length %d >>\nstream\n" % len(stream) + stream + b"endstream")
        page_ids.append(add(
            b"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 612 792] "
            b"/Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>"
            % (pages, font, content)
        ))
    objs[catalog - 1] = b"<< /Type /Catalog /Pages %d 0 R >>" % pages
    kids = b" ".join(b"%d 0 R" % p for p in page_ids)
    objs[pages - 1] = b"<< /Type /Pages /Kids [%s] /Count %d >>" % (kids, len(page_ids))

    out = bytearray(b"%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
    offsets = []
    for i, body in enumerate(objs, start=1):
        offsets.append(len(out))
        out += b"%d 0 obj\n" % i + body + b"\nendobj\n"
    xref = len(out)
    out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1)
    for off in offsets:
        out += b"%010d 00000 n \n" % off
    out += b"trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (
        len(objs) + 1, catalog, xref)
    with open(path, "wb") as f:
        f.write(out)


def make_png(path, w=800, h=600):
    raw = bytearray()
    for y in range(h):
        raw.append(0)
        for x in range(w):
            raw += bytes((x * 255 // w, y * 255 // h, 128))

    def chunk(tag, data):
        return struct.pack(">I", len(data)) + tag + data + struct.pack(
            ">I", zlib.crc32(tag + data) & 0xFFFFFFFF)

    png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))
    png += chunk(b"IDAT", zlib.compress(bytes(raw), 9)) + chunk(b"IEND", b"")
    with open(path, "wb") as f:
        f.write(png)


if __name__ == "__main__":
    out_dir = sys.argv[1] if len(sys.argv) > 1 else "."
    os.makedirs(out_dir, exist_ok=True)
    make_pdf(os.path.join(out_dir, "test-3pages.pdf"))
    make_png(os.path.join(out_dir, "test-800x600.png"))
