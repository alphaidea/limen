// Package limen compresses encoded images while retaining their dimensions.
package limen

import (
	"image"

	"github.com/alphaidea/limen/internal/avifcodec"
	"github.com/alphaidea/limen/internal/bmpcodec"
	"github.com/alphaidea/limen/internal/gifcodec"
	"github.com/alphaidea/limen/internal/heiccodec"
	"github.com/alphaidea/limen/internal/icocodec"
	"github.com/alphaidea/limen/internal/jpegcodec"
	"github.com/alphaidea/limen/internal/jxlcodec"
	"github.com/alphaidea/limen/internal/pngcodec"
	"github.com/alphaidea/limen/internal/svgcodec"
	"github.com/alphaidea/limen/internal/tiffcodec"
	"github.com/alphaidea/limen/internal/webpcodec"
)

// Format identifies the encoding of an image.
type Format string

// PNG is the Portable Network Graphics format.
const PNG Format = "png"

// JPEG is the Joint Photographic Experts Group image format.
const JPEG Format = "jpeg"

// WebP is the WebP image format.
const WebP Format = "webp"

// WebPQualityCheck enables a bounded lossy search for supported WebP inputs.
// For animations it must assess every composited frame of the complete files.
// The callback must enforce all applicable, calibrated quality-assessment gates.
// Both byte slices are read-only. A nil callback permits only equivalent output.
func WebPQualityCheck(check func(original, candidate []byte) bool) Option {
	return func(settings *options) { settings.webpQualityCheck = check }
}

// Option configures Compress.
type Option func(*options)

type options struct {
	browserImage     bool
	keepFormat       bool
	svgStandalone    bool
	heicQualityCheck func([]byte, []byte) bool
	avifQualityCheck func([]byte, []byte) bool
	jxlQualityCheck  func(original, candidate []byte) bool
	webpQualityCheck func(original, candidate []byte) bool
	nearLosslessPNG  bool
	preserveMetadata bool
	zopfli           bool
	jpegQualityCheck func(original, candidate []byte) bool
	pngQualityCheck  func(original, candidate []byte) bool
}

// KeepFormat restricts compression to the input format.
func KeepFormat() Option {
	return func(settings *options) { settings.keepFormat = true }
}

// PreserveMetadata retains metadata that compression would otherwise remove.
func PreserveMetadata() Option {
	return func(settings *options) { settings.preserveMetadata = true }
}

// ExperimentalNearLosslessPNG enables colour quantisation for 8-bit SDR PNGs.
// Its error limits are experimental and have not been calibrated by blind tests.
func ExperimentalNearLosslessPNG() Option {
	return func(settings *options) { settings.nearLosslessPNG = true }
}

// PNGQualityCheck enables quantisation for supported static 8-bit SDR PNGs up to four
// megapixels. The check must validate all applicable perceptual quality gates,
// including colour management and transparency, and treat both slices as read-only.
// Rejected candidates fall back to pixel-equivalent compression. A nil check
// leaves quantisation disabled unless ExperimentalNearLosslessPNG is also set.
func PNGQualityCheck(check func(original, candidate []byte) bool) Option {
	return func(settings *options) { settings.pngQualityCheck = check }
}

// JPEGQualityCheck enables near-lossless JPEG search. The check receives the
// original and candidate JPEG bytes after built-in structural and colour checks.
// It must treat both slices as read-only and return true to accept the candidate.
// Pixel-equivalent compression is used when the check is nil or no candidate
// passes. The search applies to supported SDR JPEGs of at most four megapixels.
// For Ultra HDR JPEG the check receives the complete files and must also
// assess the reconstructed HDR rendition.
func JPEGQualityCheck(check func(original, candidate []byte) bool) Option {
	return func(settings *options) { settings.jpegQualityCheck = check }
}

// Zopfli adds an offline recompression pass for PNG images and APNG frames whose
// filtered data is at most 8 MiB. Only smaller, equivalent output is retained.
func Zopfli() Option {
	return func(settings *options) { settings.zopfli = true }
}

// Compress prefers a smaller verified WebP, then a smaller same-format image.
// When neither candidate qualifies, it returns the original input. BrowserImage
// enables a browser-compatible fallback for selected formats.
// Treat the returned slice as read-only if it aliases input.
func Compress(input []byte, opts ...Option) ([]byte, Format, error) {
	var settings options
	for _, opt := range opts {
		if opt != nil {
			opt(&settings)
		}
	}
	if len(input) >= 2 && string(input[:2]) == "BM" {
		out, webp, err := bmpcodec.Compress(input, settings.keepFormat, settings.preserveMetadata)
		if err != nil {
			return nil, "", err
		}
		if webp {
			return out, WebP, nil
		}
		return out, BMP, nil
	}
	if icocodec.IsICO(input) {
		output, err := icocodec.Compress(input, settings.preserveMetadata, settings.zopfli)
		if err != nil {
			return nil, "", err
		}
		return output, ICO, nil
	}
	if tiffcodec.IsTIFF(input) {
		if settings.browserImage {
			return settings.convertBrowserImage(input, TIFF, tiffcodec.BrowserSource)
		}
		if candidate := settings.webPCandidate(input, tiffcodec.WebPSource); candidate != nil {
			return candidate, WebP, nil
		}
		output, err := tiffcodec.Compress(input, settings.preserveMetadata)
		if err != nil {
			return nil, "", err
		}
		return output, TIFF, nil
	}
	if len(input) >= 6 && (string(input[:6]) == "GIF87a" || string(input[:6]) == "GIF89a") {
		if candidate := settings.webPCandidate(input, gifcodec.WebPSource); candidate != nil {
			return candidate, WebP, nil
		}
		output, err := gifcodec.Compress(input, settings.preserveMetadata)
		if err != nil {
			return nil, "", err
		}
		return output, GIF, nil
	}
	if svgcodec.IsSVG(input) {
		output, err := svgcodec.CompressWithOptions(input, settings.svgStandalone)
		if err != nil {
			return nil, "", err
		}
		return output, SVG, nil
	}
	if heiccodec.IsHEIC(input) {
		if settings.browserImage {
			return settings.convertBrowserImage(input, HEIC, heiccodec.BrowserSource)
		}
		if candidate := settings.webPCandidate(input, heiccodec.WebPSource); candidate != nil {
			return candidate, WebP, nil
		}
		output, err := heiccodec.Compress(input, settings.preserveMetadata, settings.heicQualityCheck)
		if err != nil {
			return nil, "", err
		}
		return output, HEIC, nil
	}
	if avifcodec.IsAVIF(input) {
		output, err := avifcodec.Compress(input, settings.preserveMetadata, settings.avifQualityCheck)
		if err != nil {
			return nil, "", err
		}
		return output, AVIF, nil
	}
	if jxlcodec.IsJPEGXL(input) {
		if settings.browserImage {
			return settings.convertBrowserImage(input, JPEGXL, jxlcodec.BrowserSource)
		}
		output, err := jxlcodec.Compress(input, settings.preserveMetadata, settings.jxlQualityCheck)
		if err != nil {
			return nil, "", err
		}
		return output, JPEGXL, nil
	}
	// Generic HEIF does not imply HEVC. Preserve all items and presentation
	// properties until the actual codec and complete rendering can be verified.
	if heiccodec.IsHEIF(input) {
		if settings.browserImage {
			return settings.convertBrowserImage(input, HEIF, heiccodec.BrowserSource)
		}
		return input, HEIF, nil
	}

	if len(input) >= 12 && string(input[:4]) == "RIFF" && string(input[8:12]) == "WEBP" {
		output, err := webpcodec.Compress(input, settings.webpQualityCheck)
		if err != nil {
			return nil, "", err
		}
		return output, WebP, nil
	}

	if len(input) >= 2 && input[0] == 0xff && input[1] == 0xd8 {
		if candidate := settings.webPCandidate(input, jpegcodec.WebPSource); candidate != nil {
			return candidate, WebP, nil
		}
		output, err := jpegcodec.Compress(input, settings.preserveMetadata, settings.jpegQualityCheck)
		if err != nil {
			return nil, "", err
		}
		return output, JPEG, nil
	}

	output, webp, err := pngcodec.CompressPreferred(input, pngcodec.Options{
		NearLossless:     settings.nearLosslessPNG,
		PreserveMetadata: settings.preserveMetadata,
		Zopfli:           settings.zopfli,
		QualityCheck:     settings.pngQualityCheck,
	}, settings.allowsWebP(input))
	if err != nil {
		return nil, "", err
	}
	if webp {
		return output, WebP, nil
	}
	return output, PNG, nil
}

func (settings options) allowsWebP(input []byte) bool {
	return !settings.keepFormat && !settings.preserveMetadata && len(input) <= 64<<20
}

func (settings options) webPCandidate(input []byte, decode func([]byte) image.Image) []byte {
	if !settings.allowsWebP(input) {
		return nil
	}
	source := decode(input)
	if source == nil {
		return nil
	}
	candidate, _ := webpcodec.Encode(source, len(input))
	return candidate
}
