package imgix

import (
	"bytes"
	"fmt"
	"io"
	"runtime"

	"github.com/imgproxy/imgproxy/v4/imagedata"
	"github.com/imgproxy/imgproxy/v4/processing"
	"github.com/imgproxy/imgproxy/v4/vips"
)

// Metadata describes a single loaded page and the source's total page count.
type Metadata struct {
	Width, Height, Pages int
	ContentType          string
	PDF                  bool
}

// MetadataLoader loads source metadata; injectable without a PDF backend.
// page is zero-based; implementations must load exactly one page.
type MetadataLoader func(data imagedata.ImageData, page int) (Metadata, error)

func isPDF(data imagedata.ImageData) bool {
	if data.Format().Mime() == "application/pdf" {
		return true
	}
	var magic [5]byte
	_, err := io.ReadFull(data.Reader(), magic[:])
	return err == nil && bytes.Equal(magic[:], []byte("%PDF-"))
}

func loadMetadata(data imagedata.ImageData, page int) (Metadata, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer vips.Cleanup()
	var img vips.Image
	defer img.Clear()
	// PDF detection and Load dispatch come from vips, so no imagetype.PDF
	// dependency is needed in this adapter.
	if err := img.Load(data, 1, page, 1); err != nil {
		return Metadata{}, err
	}
	if err := data.Error(); err != nil {
		return Metadata{}, err
	}
	width, height, _, _ := processing.ExtractGeometry(&img, 0, true)
	if width < 1 || height < 1 {
		return Metadata{}, fmt.Errorf("invalid source dimensions")
	}
	return Metadata{width, height, img.Pages(), data.Format().Mime(), isPDF(data)}, nil
}
