// Package avifcodec encodes static sRGB images with a bounded candidate search.
package avifcodec

import (
	"bytes"
	"errors"
	"image"
	"image/color"

	"github.com/alphaidea/limen/internal/pixelcheck"
	"github.com/gen2brain/avif"
)

const maxPixels = 4_000_000

// Encode retains an equivalent or quality-gated baseline. Check must enforce all applicable
// quality gates against source; nil disables lossy candidates. Arguments passed
// to check are read-only. Source must contain static sRGB samples.
func Encode(source image.Image, check func(image.Image, []byte) bool) ([]byte, error) {
	if source == nil {
		return nil, errors.New("limen: nil AVIF source")
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || width > maxPixels/height {
		return nil, errors.New("limen: AVIF source exceeds pixel limits")
	}

	// Preserve premultiplied samples when the decoder already returned RGBA.
	pixels := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			r, g, b, a := source.At(x+bounds.Min.X, y+bounds.Min.Y).RGBA()
			if r%257 != 0 || g%257 != 0 || b%257 != 0 || a%257 != 0 {
				if check == nil {
					return nil, errors.New("limen: AVIF input needs a quality gate for precision conversion")
				}
			}
			pixels.SetRGBA(x, y, color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)})
		}
	}

	encode := func(quality int, lossless bool, effort int) ([]byte, error) {
		var output bytes.Buffer
		err := avif.Encode(&output, pixels, avif.Options{Quality: quality, QualityAlpha: 100, Lossless: lossless, Speed: 10 - effort, ChromaSubsampling: image.YCbCrSubsampleRatio444})
		return output.Bytes(), err
	}
	best, err := encode(100, true, 1)
	if err != nil {
		return nil, err
	}
	decoded, err := avif.Decode(bytes.NewReader(best))
	if err != nil {
		return nil, err
	}
	if !equivalent(source, decoded, true) && (check == nil || !check(source, best)) {
		return nil, errors.New("limen: AVIF lossless verification failed")
	}
	if check == nil {
		return best, nil
	}

	// Bounded descending search; each selected candidate is independently checked.
	for _, quality := range []int{100, 99, 98, 96, 94, 90, 82} {
		candidate, err := encode(quality, false, 4)
		if err != nil {
			return nil, err
		}
		if len(candidate) >= len(best) {
			continue
		}
		decoded, err := avif.Decode(bytes.NewReader(candidate))
		if err != nil {
			return nil, err
		}
		if !equivalent(pixels, decoded, false) || !check(source, candidate) {
			break
		}
		best = candidate

	}

	return best, nil
}

func equivalent(source, candidate image.Image, colours bool) bool {
	return pixelcheck.Equivalent(source, candidate, colours)
}
