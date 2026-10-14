package imagetype_test

import (
	"os"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/imgproxy/imgproxy/v4/imagetype"
)

func TestDefaultTypesRegistered(t *testing.T) {
	// Test that all default types are properly registered by init()
	defaultTypes := []imagetype.Type{
		imagetype.JPEG, imagetype.JXL, imagetype.PNG, imagetype.WEBP, imagetype.GIF,
		imagetype.ICO, imagetype.SVG, imagetype.HEIC, imagetype.AVIF, imagetype.BMP, imagetype.TIFF,
		imagetype.PDF,
	}

	for _, typ := range defaultTypes {
		t.Run(typ.String(), func(t *testing.T) {
			desc := imagetype.GetTypeDesc(typ)
			require.NotNil(t, desc)

			// Verify that the description has non-empty fields
			require.NotEmpty(t, desc.String)
			require.NotEmpty(t, desc.Ext)
			require.NotEqual(t, "application/octet-stream", desc.Mime)
		})
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		file string
		want imagetype.Type
	}{
		{"JPEG", "../testdata/test-images/jpg/jpg.jpg", imagetype.JPEG},
		{"JXL", "../testdata/test-images/jxl/jxl.jxl", imagetype.JXL},
		{"PNG", "../testdata/test-images/png/png.png", imagetype.PNG},
		{"WEBP", "../testdata/test-images/webp/webp.webp", imagetype.WEBP},
		{"GIF", "../testdata/test-images/gif/gif.gif", imagetype.GIF},
		{"ICO", "../testdata/test-images/ico/png-256x256.ico", imagetype.ICO},
		{"SVG", "../testdata/test-images/svg/svg.svg", imagetype.SVG},
		{"HEIC", "../testdata/test-images/heif/heif.heif", imagetype.HEIC},
		{"BMP", "../testdata/test-images/bmp/24-bpp.bmp", imagetype.BMP},
		{"TIFF", "../testdata/test-images/tiff/tiff.tiff", imagetype.TIFF},
		{"SVG", "../testdata/test-images/svg/svg.svg", imagetype.SVG},
		{"PDF", "../testdata/test-3pages.pdf", imagetype.PDF},
		{"RAW", "../testdata/test-images/raw/RAW_CANON_1DM2.CR2", imagetype.Unknown}, // RAW is not supported
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := os.Open(tt.file)
			require.NoError(t, err)
			defer f.Close()

			got, err := imagetype.Detect(f, "", path.Ext(tt.file))
			if tt.want == imagetype.Unknown {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

func TestPDFType(t *testing.T) {
	require.Equal(t, "pdf", imagetype.PDF.String())
	require.Equal(t, ".pdf", imagetype.PDF.Ext())
	require.Equal(t, "application/pdf", imagetype.PDF.Mime())

	typ, ok := imagetype.GetTypeByName("pdf")
	require.True(t, ok)
	require.Equal(t, imagetype.PDF, typ)
}

func TestDetectPDFMagicBytes(t *testing.T) {
	typ, err := imagetype.Detect(strings.NewReader("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n"), "", "")
	require.NoError(t, err)
	require.Equal(t, imagetype.PDF, typ)

	// Content type and extension don't matter, only the magic bytes do
	typ, err = imagetype.Detect(strings.NewReader("%PDF-2.0\n"), "application/octet-stream", ".bin")
	require.NoError(t, err)
	require.Equal(t, imagetype.PDF, typ)

	_, err = imagetype.Detect(strings.NewReader("%PDX-1.7\n"), "application/pdf", ".pdf")
	require.Error(t, err)
}
