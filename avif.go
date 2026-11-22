package limen

import (
	"image"

	"github.com/alphaidea/limen/internal/avifcodec"
)

// AVIF identifies the AV1 Image File Format.
const AVIF Format = "avif"

// EncodeAVIF encodes a static SDR sRGB image of at most four million pixels.
// The caller must resolve orientation and colour management beforehand.
// Nil check permits only verified pixel-equivalent output. Transparent or higher
// precision input may need a check because the backend uses eight-bit premultiplied
// samples. The check must enforce every applicable calibrated quality-assessment gate against
// source. Callback arguments are read-only. Encoding has no file-size fallback.
func EncodeAVIF(source image.Image, check func(image.Image, []byte) bool) ([]byte, error) {
	return avifcodec.Encode(source, check)
}

// AVIFQualityCheck enables supported still and animated SDR recompression.
// The gate must assess all frames of an animation. Nil retains the original;
// arguments are read-only.
func AVIFQualityCheck(check func(original, candidate []byte) bool) Option {
	return func(settings *options) { settings.avifQualityCheck = check }
}
