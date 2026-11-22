package limen

import (
	"image"

	"github.com/alphaidea/limen/internal/heiccodec"
)

// HEIC identifies the HEVC Image File Format.
const HEIC Format = "heic"

// HEIF identifies a generic HEIF container whose codec is not established.
const HEIF Format = "heif"

// EncodeHEIC encodes a static SDR sRGB image of at most four million pixels.
// The caller must resolve orientation and colour management beforehand.
// Nil check permits only verified pixel-equivalent output. Transparent or higher
// precision input may need a check because the backend uses eight-bit premultiplied
// samples. The check must enforce every applicable calibrated quality-assessment gate against
// source. Callback arguments are read-only. Encoding has no file-size fallback.
func EncodeHEIC(source image.Image, check func(image.Image, []byte) bool) ([]byte, error) {
	return heiccodec.Encode(source, check)
}

// HEICQualityCheck enables supported single-image SDR and HDR recompression,
// including supported high-resolution grids and HEIC with an HDR gain map.
// The gate must assess the complete presented image, including orientation,
// colour and, where applicable, both the SDR base and HDR rendition.
// Nil retains the original.
// Callback arguments are read-only.
func HEICQualityCheck(check func(original, candidate []byte) bool) Option {
	return func(settings *options) { settings.heicQualityCheck = check }
}
