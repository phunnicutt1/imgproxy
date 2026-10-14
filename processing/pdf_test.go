package processing_test

import (
	"image/png"
	"io"
	"net/http"
	"testing"

	"github.com/imgproxy/imgproxy/v4/errctx"
	"github.com/imgproxy/imgproxy/v4/imagedata"
	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/options"
	"github.com/imgproxy/imgproxy/v4/options/keys"
	"github.com/imgproxy/imgproxy/v4/processing"
	"github.com/imgproxy/imgproxy/v4/vips"
	"github.com/stretchr/testify/suite"
)

// test-3pages.pdf has 3 US Letter pages (612x792 pt)
const testPDF = "test-3pages.pdf"

type PDFTestSuite struct {
	testSuite
}

func (s *PDFTestSuite) skipIfNoPDFLoad() {
	if !vips.SupportsLoad(imagetype.PDF) {
		s.T().Skip("libvips is built without pdfload support")
	}
}

// processPDF processes the test PDF with the given URL options
// and loads the result
func (s *PDFTestSuite) processPDF(urlOptions string) (imagedata.ImageData, *vips.Image) {
	s.T().Helper()

	resp := s.GET("/unsafe/" + urlOptions + "/plain/local:///" + testPDF)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	s.Require().NoError(err)
	s.Require().Equal(http.StatusOK, resp.StatusCode, string(body))

	data, err := s.Imgproxy().ImageDataFactory().NewFromBytes(body)
	s.Require().NoError(err)
	s.T().Cleanup(func() { data.Close() })

	img := new(vips.Image)
	s.T().Cleanup(img.Clear)
	s.Require().NoError(img.Load(data, 1.0, 0, 1))

	return data, img
}

func (s *PDFTestSuite) processImageDirect(
	data imagedata.ImageData,
	o *options.Options,
) (*processing.Result, error) {
	res, err := s.Imgproxy().Processor().ProcessImage(s.T().Context(), data, o)
	if res != nil && res.OutData != nil {
		s.T().Cleanup(func() { res.OutData.Close() })
	}
	return res, err
}

func (s *PDFTestSuite) TestPDFDefaultSize() {
	s.skipIfNoPDFLoad()

	data, img := s.processPDF("pg:1")

	// 72 DPI: a Letter page is 612x792 px
	s.Require().Equal(612, img.Width())
	s.Require().Equal(792, img.Height())

	// PDF pages are opaque, so the default output format is JPEG
	s.Require().Equal(imagetype.JPEG, data.Format())
}

func (s *PDFTestSuite) TestPDFResizeToFit() {
	s.skipIfNoPDFLoad()

	_, img := s.processPDF("rs:fit:300:300/pg:2/f:png")

	s.Require().Equal(232, img.Width())
	s.Require().Equal(300, img.Height())
}

func (s *PDFTestSuite) TestPDFScaleUpIsVector() {
	s.skipIfNoPDFLoad()

	// PDF is a vector format, so it's rendered at the requested size
	// rather than upscaled from 72 DPI.
	_, img := s.processPDF("w:1224/f:png")

	s.Require().Equal(1224, img.Width())
	s.Require().Equal(1584, img.Height())
}

func (s *PDFTestSuite) TestPDFPagesDiffer() {
	s.skipIfNoPDFLoad()

	page0, _ := s.processPDF("rs:fit:100:100/f:png")
	page0Again, _ := s.processPDF("pg:0/rs:fit:100:100/f:png")
	page2, _ := s.processPDF("pg:2/rs:fit:100:100/f:png")

	b0, err := io.ReadAll(page0.Reader())
	s.Require().NoError(err)
	b0Again, err := io.ReadAll(page0Again.Reader())
	s.Require().NoError(err)
	b2, err := io.ReadAll(page2.Reader())
	s.Require().NoError(err)

	// Page 0 is the default
	s.Require().Equal(b0, b0Again)
	s.Require().NotEqual(b0, b2)
}

func (s *PDFTestSuite) TestPDFPageOutOfRange() {
	s.skipIfNoPDFLoad()

	for _, page := range []string{"3", "100"} {
		s.Run(page, func() {
			resp := s.GET("/unsafe/pg:" + page + "/plain/local:///" + testPDF)
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			s.Require().NoError(err)

			s.Require().Equal(http.StatusUnprocessableEntity, resp.StatusCode, string(body))
			s.Require().Contains(string(body), "Page "+page+" is out of range (document has 3 pages)")
		})
	}
}

func (s *PDFTestSuite) TestPDFPageOutOfRangeDirect() {
	s.skipIfNoPDFLoad()

	data := imagedata.NewFromBytesWithFormat(imagetype.PDF, s.TestData.Read(testPDF))
	defer data.Close()

	o := options.New()
	o.Set(keys.Page, 3)

	_, err := s.processImageDirect(data, o)
	s.Require().Error(err)

	var pageErr processing.PageOutOfRangeError
	s.Require().ErrorAs(err, &pageErr)
	s.Require().Equal(http.StatusUnprocessableEntity, pageErr.StatusCode())
	s.Require().Equal("Page 3 is out of range (document has 3 pages)", pageErr.PublicMessage())
}

func (s *PDFTestSuite) TestPDFOutputFormatRejectedByHandler() {
	resp := s.GET("/unsafe/f:pdf/plain/local:///geometry.png")
	defer resp.Body.Close()

	s.Require().Equal(http.StatusUnprocessableEntity, resp.StatusCode)
}

func (s *PDFTestSuite) TestPDFOutputFormatRejectedByProcessing() {
	// Doesn't need pdfload: the source is PNG, only the output format is PDF.
	// This checks the processing pipeline itself, regardless of the handler checks.
	data := imagedata.NewFromBytesWithFormat(imagetype.PNG, s.TestData.Read("geometry.png"))
	defer data.Close()

	o := options.New()
	o.Set(keys.Format, imagetype.PDF)

	_, err := s.processImageDirect(data, o)
	s.Require().Error(err)

	var formatErr processing.SaveFormatError
	s.Require().ErrorAs(err, &formatErr)

	var ectx errctx.Error
	s.Require().ErrorAs(err, &ectx)
	s.Require().Equal(http.StatusUnprocessableEntity, ectx.StatusCode())
}

func (s *PDFTestSuite) TestPDFSkipProcessingIgnored() {
	s.skipIfNoPDFLoad()

	// PDF must always be rendered even if it's listed in skip_processing
	data, img := s.processPDF("skp:pdf")

	s.Require().Equal(imagetype.JPEG, data.Format())
	s.Require().Equal(612, img.Width())
}

// Absolute crop sizes and offsets are in source (page) pixels, even when
// the vector guard loads the page downscaled because of max_src_resolution.
//
// Page 0 is red from y=0 to y=264 and white below it on the left edge,
// so a 200x200 crop at (0, 164) has a red top half and a white bottom half.
func (s *PDFTestSuite) TestPDFCropWithVectorGuard() {
	s.skipIfNoPDFLoad()

	for _, opts := range []string{
		// Without the guard
		"c:200:200:nowe:0:164/rs:fit:200:200/f:png",
		// 612x792 > 100000 px, so the guard loads the page shrunk by ~2.2
		"msr:0.1/c:200:200:nowe:0:164/rs:fit:200:200/f:png",
	} {
		s.Run(opts, func() {
			data, img := s.processPDF(opts)

			s.Require().Equal(200, img.Width())
			s.Require().Equal(200, img.Height())

			px, err := png.Decode(data.Reader())
			s.Require().NoError(err)

			r, g, b, _ := px.At(100, 40).RGBA()
			s.Require().Greater(r>>8, uint32(200), "top half should be red")
			s.Require().Less(g>>8, uint32(80), "top half should be red")
			s.Require().Less(b>>8, uint32(80), "top half should be red")

			r, g, b, _ = px.At(100, 160).RGBA()
			s.Require().Greater(r>>8, uint32(200), "bottom half should be white")
			s.Require().Greater(g>>8, uint32(200), "bottom half should be white")
			s.Require().Greater(b>>8, uint32(200), "bottom half should be white")
		})
	}
}

func TestPDF(t *testing.T) {
	suite.Run(t, new(PDFTestSuite))
}
