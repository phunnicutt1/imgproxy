package vips_test

import (
	"image"
	"image/color"
	"image/png"
	"net/http"
	"testing"

	"github.com/imgproxy/imgproxy/v4/errctx"
	"github.com/imgproxy/imgproxy/v4/imagedata"
	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/options"
	"github.com/imgproxy/imgproxy/v4/testutil"
	"github.com/imgproxy/imgproxy/v4/vips"
	"github.com/stretchr/testify/require"
)

// test-3pages.pdf has 3 US Letter pages (612x792 pt):
// page 0 has a red block at the top, page 1 a green block in the middle,
// page 2 a blue block at the bottom.
const testPDF = "test-3pages.pdf"

func skipIfNoPDFLoad(t *testing.T) {
	t.Helper()

	if !vips.SupportsLoad(imagetype.PDF) {
		t.Skip("libvips is built without pdfload support")
	}
}

func loadPDFPage(t *testing.T, page int, shrink float64) *vips.Image {
	t.Helper()

	testData := testutil.NewTestDataProvider(func() *testing.T { return t })
	id := imagedata.NewFromBytesWithFormat(imagetype.PDF, testData.Read(testPDF))
	t.Cleanup(func() { id.Close() })

	img := new(vips.Image)
	t.Cleanup(img.Clear)

	require.NoError(t, img.Load(id, shrink, page, 1))

	return img
}

// decodePNG renders the image to PNG and decodes it with Go to inspect pixels
func decodePNG(t *testing.T, img *vips.Image) image.Image {
	t.Helper()

	data, err := img.Save(imagetype.PNG, 0, options.New())
	require.NoError(t, err)
	defer data.Close()

	decoded, err := png.Decode(data.Reader())
	require.NoError(t, err)

	return decoded
}

func rgbAt(img image.Image, x, y int) color.RGBA {
	r, g, b, _ := img.At(x, y).RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
}

func TestPDFSaveNotSupported(t *testing.T) {
	require.False(t, vips.SupportsSave(imagetype.PDF))
}

func TestPDFLoad(t *testing.T) {
	skipIfNoPDFLoad(t)

	img := loadPDFPage(t, 0, 1.0)

	// 72 DPI: 1 PDF point is 1 pixel
	require.Equal(t, 612, img.Width())
	require.Equal(t, 792, img.Height())

	// The page is rendered on an opaque white background without alpha
	require.False(t, img.HasAlpha())

	// The total number of pages in the document is available after loading a single page
	require.Equal(t, 3, img.Pages())
}

func TestPDFLoadScale(t *testing.T) {
	skipIfNoPDFLoad(t)

	img := loadPDFPage(t, 0, 0.5)

	require.Equal(t, 1224, img.Width())
	require.Equal(t, 1584, img.Height())
}

func TestPDFLoadPage(t *testing.T) {
	skipIfNoPDFLoad(t)

	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}

	page0 := decodePNG(t, loadPDFPage(t, 0, 1.0))
	page2 := loadPDFPage(t, 2, 1.0)
	require.Equal(t, 3, page2.Pages())
	page2Px := decodePNG(t, page2)

	// Top: red block on page 0, white on page 2
	top0, top2 := rgbAt(page0, 20, 20), rgbAt(page2Px, 20, 20)
	require.Greater(t, top0.R, uint8(200))
	require.Less(t, top0.G, uint8(80))
	require.Less(t, top0.B, uint8(80))
	require.Equal(t, white, top2)

	// Bottom: white on page 0, blue block on page 2
	bottom0, bottom2 := rgbAt(page0, 20, 770), rgbAt(page2Px, 20, 770)
	require.Equal(t, white, bottom0)
	require.Less(t, bottom2.R, uint8(80))
	require.Less(t, bottom2.G, uint8(80))
	require.Greater(t, bottom2.B, uint8(200))
}

func TestPDFLoadPageOutOfRange(t *testing.T) {
	skipIfNoPDFLoad(t)

	testData := testutil.NewTestDataProvider(func() *testing.T { return t })
	id := imagedata.NewFromBytesWithFormat(imagetype.PDF, testData.Read(testPDF))
	defer id.Close()

	img := new(vips.Image)
	defer img.Clear()

	require.Error(t, img.Load(id, 1.0, 3, 1))
}

// Encrypted (password-protected) and corrupt PDFs are bad input, not server errors
func TestPDFLoadBrokenIsUnprocessable(t *testing.T) {
	skipIfNoPDFLoad(t)

	for _, name := range []string{"test-encrypted.pdf", "test-truncated.pdf"} {
		t.Run(name, func(t *testing.T) {
			testData := testutil.NewTestDataProvider(func() *testing.T { return t })
			id := imagedata.NewFromBytesWithFormat(imagetype.PDF, testData.Read(name))
			defer id.Close()

			img := new(vips.Image)
			defer img.Clear()

			err := img.Load(id, 1.0, 0, 1)
			require.Error(t, err)

			var ectx errctx.Error
			require.ErrorAs(t, err, &ectx)
			require.Equal(t, http.StatusUnprocessableEntity, ectx.StatusCode(), err.Error())
		})
	}
}
