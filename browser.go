package limen

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"

	"github.com/alphaidea/limen/internal/heiccodec"
	"github.com/alphaidea/limen/internal/jxlcodec"
	"github.com/alphaidea/limen/internal/pixelcheck"
	"github.com/alphaidea/limen/internal/pngcodec"
	"github.com/alphaidea/limen/internal/tiffcodec"
	"github.com/alphaidea/limen/internal/webpcodec"
)

// ErrBrowserImage indicates that the input could not be converted to a
// browser-compatible image within the supported limits and requested options.
var ErrBrowserImage = errors.New("limen: browser-compatible output unavailable")

// BrowserImage targets modern Chrome, Edge, Firefox and Safari. For HEIC, HEIF,
// TIFF and JPEG XL it prefers lossless WebP if smaller than the input or a
// lossless PNG. PNG is verified against the source pixels before returning.
// HEIC and HEIF can retain RGB ICC profiles and high bit depth in PNG.
// Supported PQ and HLG stills retain their HDR signalling in 16-bit PNG.
// Other decodable HEIC and HEIF presentations can fall back to a primary-image
// PNG, which may omit secondary images, gain maps or HDR metadata.
// PreserveMetadata also carries supported HEIC and HEIF Exif and XMP into PNG.
// The output may exceed the input size. Unsupported presentations and option
// conflicts return ErrBrowserImage.
func BrowserImage() Option {
	return func(settings *options) { settings.browserImage = true }
}

func (settings options) convertBrowserTIFF(input []byte) ([]byte, Format, error) {
	if settings.keepFormat {
		return nil, "", fmt.Errorf("%w: TIFF requires format conversion", ErrBrowserImage)
	}
	source, density := tiffcodec.BrowserSource(input)
	if source == nil {
		return nil, "", fmt.Errorf("%w: unsupported TIFF presentation", ErrBrowserImage)
	}
	return encodeBrowserImage(input, source, pngcodec.BrowserMetadata{PixelsPerMetre: density}, density == ([2]uint32{}))
}

func (settings options) convertBrowserJPEGXL(input []byte) ([]byte, Format, error) {
	if settings.keepFormat {
		return nil, "", fmt.Errorf("%w: JPEG XL requires format conversion", ErrBrowserImage)
	}
	source := jxlcodec.BrowserSource(input, settings.preserveMetadata)
	if source == nil {
		return nil, "", fmt.Errorf("%w: unsupported JPEG XL presentation or metadata", ErrBrowserImage)
	}
	switch source.(type) {
	case *image.NRGBA, *image.Gray:
		return encodeBrowserImage(input, source, pngcodec.BrowserMetadata{}, true)
	default:
		return encodeBrowserImage(input, source, pngcodec.BrowserMetadata{}, false)
	}
}

func (settings options) convertBrowserHEIC(input []byte, format Format) ([]byte, Format, error) {
	if settings.keepFormat {
		return nil, "", fmt.Errorf("%w: %s requires format conversion", ErrBrowserImage, format)
	}
	still, ok := heiccodec.BrowserSource(input, settings.preserveMetadata)
	if !ok {
		return nil, "", fmt.Errorf("%w: unsupported %s presentation", ErrBrowserImage, format)
	}
	_, eightBit := still.Pixels.(*image.NRGBA)
	metadata := pngcodec.BrowserMetadata{ICC: still.ICC, Exif: still.Exif, XMP: still.XMP}
	if len(metadata.ICC) != 0 && !pngcodec.ValidRGBProfile(metadata.ICC) {
		metadata.ICC = nil
	}
	if len(metadata.ICC) == 0 {
		metadata.ColourPrimaries = still.ColourPrimaries
		metadata.TransferFunction = still.TransferFunction
	}
	if hdr := still.HDR; hdr != nil {
		metadata.ColourPrimaries = hdr.Primaries
		metadata.TransferFunction = hdr.Transfer
		metadata.MasteringDisplay = hdr.MasteringDisplay
		metadata.ContentLight = hdr.ContentLight
	}
	allowWebP := eightBit && !still.PrimaryOnly && len(still.ICC) == 0 && len(still.Exif) == 0 && len(still.XMP) == 0
	return encodeBrowserImage(input, still.Pixels, metadata, allowWebP)
}

func encodeBrowserImage(input []byte, source image.Image, metadata pngcodec.BrowserMetadata, allowWebP bool) ([]byte, Format, error) {
	var webp []byte
	if allowWebP {
		webp, _ = webpcodec.Encode(source, math.MaxInt)
	}
	if webp != nil && len(webp) < len(input) {
		return webp, WebP, nil
	}

	output, err := pngcodec.EncodeBrowser(source, metadata)
	if err != nil {
		return nil, "", fmt.Errorf("%w: encode PNG: %v", ErrBrowserImage, err)
	}
	if webp != nil && len(webp) < len(output) {
		return webp, WebP, nil
	}
	decoded, err := png.Decode(bytes.NewReader(output))
	if err != nil || !pixelcheck.Equivalent(source, decoded, true) {
		return nil, "", fmt.Errorf("%w: PNG pixel verification failed", ErrBrowserImage)
	}
	return output, PNG, nil
}
