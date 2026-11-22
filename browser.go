package limen

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"

	"github.com/alphaidea/limen/internal/pixelcheck"
	"github.com/alphaidea/limen/internal/webpcodec"
)

// ErrBrowserImage indicates that the input could not be converted to a
// browser-compatible image while preserving its presentation and requested options.
var ErrBrowserImage = errors.New("limen: browser-compatible output unavailable")

// BrowserImage targets modern Chrome, Edge, Firefox and Safari. For HEIC, HEIF,
// TIFF and JPEG XL it prefers lossless WebP if smaller than the input or a
// lossless PNG. PNG is verified against the source pixels before returning.
// The output may exceed the input size. Unsupported presentations
// and conflicts with KeepFormat or PreserveMetadata return ErrBrowserImage.
func BrowserImage() Option {
	return func(settings *options) { settings.browserImage = true }
}

func (settings options) convertBrowserImage(input []byte, format Format, decode func([]byte) image.Image) ([]byte, Format, error) {
	if !settings.allowsWebP(input) {
		return nil, "", fmt.Errorf("%w: %s", ErrBrowserImage, format)
	}
	source := decode(input)
	if source == nil {
		return nil, "", fmt.Errorf("%w: unsupported %s presentation", ErrBrowserImage, format)
	}
	webp, _ := webpcodec.Encode(source, math.MaxInt)
	if webp != nil && len(webp) < len(input) {
		return webp, WebP, nil
	}

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, source); err != nil {
		return nil, "", fmt.Errorf("%w: encode PNG: %v", ErrBrowserImage, err)
	}
	if webp != nil && len(webp) < buffer.Len() {
		return webp, WebP, nil
	}
	decoded, err := png.Decode(bytes.NewReader(buffer.Bytes()))
	if err != nil || !pixelcheck.Equivalent(source, decoded, true) {
		return nil, "", fmt.Errorf("%w: PNG pixel verification failed", ErrBrowserImage)
	}
	return buffer.Bytes(), PNG, nil
}
