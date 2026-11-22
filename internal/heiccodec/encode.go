// Package heiccodec encodes static sRGB images with a bounded candidate search.
package heiccodec

import (
	"bytes"
	"errors"
	"image"
	"image/color"

	"github.com/alphaidea/limen/internal/pixelcheck"
	"github.com/gen2brain/h265/heic"
)

const maxPixels = 4_000_000

var searchQualities = [...]int{82, 90, 94, 98, 100}

// Encode retains an equivalent or quality-gated baseline. Check must enforce all applicable
// quality gates against source; nil disables lossy candidates. Arguments passed
// to check are read-only. Source must contain static sRGB samples.
func Encode(source image.Image, check func(image.Image, []byte) bool) ([]byte, error) {
	if source == nil {
		return nil, errors.New("limen: nil HEIC source")
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || width > maxPixels/height {
		return nil, errors.New("limen: HEIC source exceeds pixel limits")
	}

	// Preserve premultiplied samples when the decoder already returned RGBA.
	pixels := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			r, g, b, a := source.At(x+bounds.Min.X, y+bounds.Min.Y).RGBA()
			if r%257 != 0 || g%257 != 0 || b%257 != 0 || a%257 != 0 {
				if check == nil {
					return nil, errors.New("limen: HEIC input needs a quality gate for precision conversion")
				}
			}
			pixels.SetRGBA(x, y, color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)})
		}
	}

	encode := func(quality int, lossless bool) ([]byte, error) {
		var output bytes.Buffer
		err := heic.Encode(&output, pixels, heic.EncodeOptions{Quality: quality, Lossless: lossless, Chroma: heic.Chroma444})
		return output.Bytes(), err
	}
	// Avoid a PCM baseline and its decode when a gated candidate suffices.
	// Failed quality checks do not imply that subsequent qualities will fail.
	if check != nil {
		for _, quality := range searchQualities {
			candidate, err := encode(quality, false)
			if err != nil {
				return nil, err
			}
			decoded, err := heic.Decode(bytes.NewReader(candidate))
			if err != nil {
				return nil, err
			}
			if equivalent(pixels, decoded, false) && check(source, candidate) {
				return candidate, nil
			}
		}
	}
	best, err := encode(100, true)
	if err != nil {
		return nil, err
	}
	decoded, err := heic.Decode(bytes.NewReader(best))
	if err != nil {
		return nil, err
	}
	if !equivalent(source, decoded, true) && (check == nil || !check(source, best)) {
		return nil, errors.New("limen: HEIC lossless verification failed")
	}
	return best, nil
}

func equivalent(source, candidate image.Image, colours bool) bool {
	return pixelcheck.Equivalent(source, candidate, colours)
}
