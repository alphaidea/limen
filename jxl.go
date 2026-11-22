package limen

import (
	"image"

	"github.com/alphaidea/limen/internal/jxlcodec"
)

// JPEGXL identifies the JPEG XL format.
const JPEGXL Format = "jxl"

// JPEGXLQualityCheck enables JPEG XL recompression. Check must verify original
// pixel and display dimensions, orientation, colour, content structure, and all
// applicable calibrated perceptual gates using the complete encoded files.
// Arguments are read-only. Nil or PreserveMetadata keeps the original file.
func JPEGXLQualityCheck(check func(original, candidate []byte) bool) Option {
	return func(settings *options) { settings.jxlQualityCheck = check }
}

// EncodeJPEGXL encodes a static sRGB image without changing its dimensions or
// alpha. The caller must apply orientation and colour management before calling.
// A nil check returns verified lossless encoding. Otherwise check must enforce
// every applicable quality-assessment gate against the original source. Check
// receives read-only arguments. Encoding is limited to four million pixels.
// Unlike Compress, encoding has no original file size against which to fall back.
func EncodeJPEGXL(source image.Image, check func(image.Image, []byte) bool) ([]byte, error) {
	return jxlcodec.Encode(source, check)
}
