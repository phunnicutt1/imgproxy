package processing

import (
	"math"
)

// vectorGuardScale checks if the image is a vector format and downscales it
// to the maximum allowed resolution if necessary
func (p *Processor) vectorGuardScale(c *Context) error {
	if c.ImgData == nil || !c.ImgData.Format().IsVector() {
		return nil
	}

	maxSrcRes := c.PO.MaxSrcResolution()

	if resolution := c.Img.Width() * c.Img.Height(); resolution > maxSrcRes {
		shrink := math.Sqrt(float64(resolution) / float64(maxSrcRes))
		c.VectorBaseShrink = shrink

		if err := loadSinglePage(c.Img, c.ImgData, shrink, c.PO); err != nil {
			return err
		}

		// Absolute crop offsets are in source image pixels, adjust them
		// to the downscaled image. Crop sizes are adjusted in [Context.CalcParams].
		c.shrinkCropGravity(shrink, shrink)
	}
	c.CalcParams()

	return nil
}
