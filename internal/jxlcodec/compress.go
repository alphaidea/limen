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
	if check == nil || preserveMetadata {
		return input, nil
	}
	source, err := decodeSDRStill(input, config)
	if err != nil {
		return nil, err
	}
	if source == nil {
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

// BrowserSource returns a supported sRGB still. With metadata preservation,
// only a raw codestream can be converted because it has no container metadata.
func BrowserSource(input []byte, preserveMetadata bool) image.Image {
	if !IsJPEGXL(input) || len(input) > 64<<20 ||
		(preserveMetadata && !bytes.HasPrefix(input, []byte{0xff, 0x0a})) {
		return nil
	}
	config, err := jxl.DecodeConfig(bytes.NewReader(input))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return nil
	}
	source, _ := decodeSRGBStill(input, config)
	return source
}

func decodeSDRStill(input []byte, config image.Config) (image.Image, error) {
	if config.Width > maxPixels/config.Height || (config.ColorModel != color.NRGBAModel && config.ColorModel != color.GrayModel) {
		return nil, nil
	}
	return decodeSRGBStill(input, config)
}

func decodeSRGBStill(input []byte, config image.Config) (image.Image, error) {
	if config.Width > maxPixels/config.Height ||
		(config.ColorModel != color.NRGBAModel && config.ColorModel != color.GrayModel &&
			config.ColorModel != color.NRGBA64Model && config.ColorModel != color.Gray16Model) {
		return nil, nil
	}
	decoded, err := jxl.DecodeAll(bytes.NewReader(input), jxl.Options{FrameSizeLimit: maxPixels, MaxFrames: 2, Threads: 1})
	if errors.Is(err, jxl.ErrUnsupported) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(decoded.Image) != 1 || len(decoded.Color.ICCP) != 0 || decoded.Color.Primaries != 1 || decoded.Color.Transfer != 13 {
		return nil, nil
	}
	source := decoded.Image[0]
	if source.Bounds().Size() != image.Pt(config.Width, config.Height) {
		return nil, nil
	}
	return source, nil
}
