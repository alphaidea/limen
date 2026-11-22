package jxlcodec

import (
	"bytes"
	"errors"
	"image"
	"image/color"

	"github.com/gen2brain/jxl"
)

// IsJPEGXL recognises the codestream and container signatures.
func IsJPEGXL(input []byte) bool {
	return bytes.HasPrefix(input, []byte{0xff, 0x0a}) || bytes.HasPrefix(input, []byte{0, 0, 0, 12, 'J', 'X', 'L', ' ', 13, 10, 135, 10})
}

// Compress requires an encoded-file gate because decoded samples alone cannot
// prove preservation of orientation, auxiliary channels and colour metadata.
func Compress(input []byte, preserveMetadata bool, check func([]byte, []byte) bool) ([]byte, error) {
	if !IsJPEGXL(input) || len(input) > 64<<20 {
		return nil, errors.New("limen: invalid or oversized JPEG XL input")
	}
	config, err := jxl.DecodeConfig(bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 {
		return nil, errors.New("limen: invalid JPEG XL dimensions")
	}
	if check == nil || preserveMetadata || config.Width > maxPixels/config.Height {
		return input, nil
	}
	// Restrict recompression to eight-bit SDR; higher precision may represent
	// HDR values that an integer image cannot preserve.
	if config.ColorModel != color.NRGBAModel && config.ColorModel != color.GrayModel {
		return input, nil
	}
	decoded, err := jxl.DecodeAll(bytes.NewReader(input), jxl.Options{FrameSizeLimit: maxPixels, MaxFrames: 2, Threads: 1})
	if errors.Is(err, jxl.ErrUnsupported) {
		return input, nil
	}
	if err != nil {
		return nil, err
	}
	if len(decoded.Image) != 1 || len(decoded.Color.ICCP) != 0 || decoded.Color.Primaries != 1 || decoded.Color.Transfer != 13 {
		return input, nil
	}
	source := decoded.Image[0]
	if source.Bounds().Dx() != config.Width || source.Bounds().Dy() != config.Height {
		return input, nil
	}
	best := input
	accept := func(candidate []byte) bool {
		if len(candidate) >= len(best) || !check(input, candidate) {
			return false
		}
		best = candidate
		return true
	}
	baseline, err := Encode(source, func(_ image.Image, candidate []byte) bool { return accept(candidate) })
	if err != nil {
		return nil, err
	}
	accept(baseline)
	return best, nil
}
