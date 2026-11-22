// Package jxlcodec encodes static sRGB images with a bounded candidate search.
package jxlcodec

import (
	"bytes"
	"errors"
	"image"
	"image/color"

	"github.com/alphaidea/limen/internal/pixelcheck"
	"github.com/gen2brain/jxl"
)

const maxPixels = 4_000_000

// Encode retains a verified lossless baseline. Check must enforce all applicable
// quality gates against source; nil disables lossy candidates. Arguments passed
// to check are read-only. Source must contain static sRGB samples.
func Encode(source image.Image, check func(image.Image, []byte) bool) ([]byte, error) {
	if source == nil {
		return nil, errors.New("limen: nil JPEG XL source")
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || width > maxPixels/height {
		return nil, errors.New("limen: JPEG XL source exceeds pixel limits")
	}
	// Normalise once, preserving precision and straight alpha for every trial.
	var pixels image.Image = source
	switch source.(type) {
	case *image.NRGBA, *image.NRGBA64, *image.Gray, *image.Gray16:
	default:
		normalised := image.NewNRGBA64(image.Rect(0, 0, width, height))
		eightBit := true
		for y := range height {
			for x := range width {
				pixel := color.NRGBA64Model.Convert(source.At(x+bounds.Min.X, y+bounds.Min.Y)).(color.NRGBA64)
				normalised.SetNRGBA64(x, y, pixel)
				eightBit = eightBit && pixel.R%257 == 0 && pixel.G%257 == 0 && pixel.B%257 == 0 && pixel.A%257 == 0
			}
		}
		pixels = normalised
		if eightBit {
			compact := image.NewNRGBA(normalised.Bounds())
			for i := range compact.Pix {
				compact.Pix[i] = normalised.Pix[i*2]
			}
			pixels = compact
		}
	}
	encode := func(quality int, lossless bool, effort int) ([]byte, error) {
		var output bytes.Buffer
		err := jxl.Encode(&output, pixels, jxl.EncodeOptions{Quality: quality, Lossless: lossless, Effort: effort, Threads: 1})
		return output.Bytes(), err
	}
	best, err := encode(100, true, 1)
	if err != nil {
		return nil, err
	}
	decoded, err := jxl.Decode(bytes.NewReader(best), jxl.Options{FrameSizeLimit: maxPixels, MaxFrames: 1, Threads: 1})
	if err != nil {
		return nil, err
	}
	if !equivalent(pixels, decoded, true) {
		return nil, errors.New("limen: JPEG XL lossless verification failed")
	}
	if check == nil {
		return best, nil
	}
	// Spend the assessment budget near the high-fidelity boundary. This is a
	// bounded heuristic, not a claim that quality gates are monotonic.
	winningQuality := 0
	tried := [8][101]bool{}
	try := func(quality, effort int) error {
		if quality < 1 || quality > 100 || tried[effort][quality] {
			return nil
		}
		tried[effort][quality] = true
		candidate, err := encode(quality, false, effort)
		if err != nil {
			return err
		}
		if len(candidate) >= len(best) {
			return nil
		}
		decoded, err := jxl.Decode(bytes.NewReader(candidate), jxl.Options{FrameSizeLimit: maxPixels, MaxFrames: 1, Threads: 1})
		if err != nil {
			return err
		}
		if equivalent(pixels, decoded, false) && check(source, candidate) {
			best, winningQuality = candidate, quality
		}
		return nil
	}
	for _, quality := range [...]int{96, 95, 94, 98, 100} {
		if err := try(quality, 1); err != nil {
			return nil, err
		}
	}
	if winningQuality == 0 {
		// High-quality VarDCT can exceed Modular on simple graphics. Keep a
		// bounded coarse fallback rather than assuming no smaller candidate exists.
		for _, quality := range [...]int{90, 80, 65, 45} {
			if err := try(quality, 1); err != nil {
				return nil, err
			}
			if winningQuality != 0 {
				break
			}
		}
	}
	if winningQuality == 94 {
		for _, quality := range [...]int{92, 90, 85, 80, 65, 45} {
			if err := try(quality, 1); err != nil {
				return nil, err
			}
			if winningQuality != quality {
				break
			}
		}
	}
	if winningQuality != 0 {
		centre := winningQuality
		for _, offset := range [...]int{-1, 1} {
			if err := try(centre+offset, 1); err != nil {
				return nil, err
			}
		}
		// Measured effort 4 improves the selected candidate at a lower cost
		// than effort 7. Recheck it: higher effort can change perceptual error.
		if err := try(winningQuality, 4); err != nil {
			return nil, err
		}
	}
	return best, nil
}

func equivalent(source, candidate image.Image, colours bool) bool {
	return pixelcheck.Equivalent(source, candidate, colours)
}
