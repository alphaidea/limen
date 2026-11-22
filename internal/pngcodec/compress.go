package pngcodec

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"image"
	"image/png"

	"github.com/alphaidea/limen/internal/webpcodec"
)

const maxPixels = 25_000_000

type Options struct {
	NearLossless     bool
	PreserveMetadata bool
	Zopfli           bool
	QualityCheck     func(original, candidate []byte) bool
}

// Compress finds a smaller PNG within the selected error limits.
func Compress(input []byte, options Options) ([]byte, error) {
	output, _, err := CompressPreferred(input, options, false)
	return output, err
}

// CompressPreferred returns a verified smaller WebP when permitted, otherwise a PNG.
func CompressPreferred(input []byte, options Options, preferWebP bool) ([]byte, bool, error) {
	layout, err := inspectPNG(input, options)
	if err != nil {
		return nil, false, err
	}
	config, err := png.DecodeConfig(bytes.NewReader(input))
	if err != nil {
		return nil, false, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxPixels/config.Height {
		return nil, false, errors.New("limen: PNG exceeds pixel limit")
	}
	if layout.animated {
		output, err := compressAnimation(input, options)
		return output, false, err
	}
	original, err := png.Decode(bytes.NewReader(input))
	if err != nil {
		return nil, false, err
	}
	if preferWebP && !options.PreserveMetadata && layout.depth <= 8 && !layout.displayMetadata {
		if candidate, _ := webpcodec.Encode(original, len(input)); candidate != nil {
			return candidate, true, nil
		}
	}
	output, err := compressStatic(input, options, layout, original)
	return output, false, err
}

func compressStatic(input []byte, options Options, layout pngLayout, original image.Image) ([]byte, error) {
	if layout.preserve {
		return layout.fallback, nil
	}

	var best []byte
	encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
	consider := func(candidate image.Image, tolerance uint8) error {
		if tolerance != 0 && !checkQuantisation(original, candidate, tolerance) {
			return nil
		}
		var encoded bytes.Buffer
		if err := encoder.Encode(&encoded, candidate); err != nil {
			return fmt.Errorf("limen: encode PNG candidate: %w", err)
		}
		data := layout.withMetadata(encoded.Bytes())
		data = optimiseFilters(data)
		if best != nil && len(data) >= len(best) {
			return nil
		}
		decoded, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("limen: decode PNG candidate: %w", err)
		}
		if equivalentPNG(original, decoded, tolerance) {
			if tolerance != 0 && options.QualityCheck != nil && !options.QualityCheck(input, data) {
				return nil
			}
			best = data
		}
		return nil
	}

	if err := consider(original, 0); err != nil {
		return nil, err
	}
	if normalised := normaliseTransparentPixels(original); normalised != nil {
		if err := consider(normalised, 0); err != nil {
			return nil, err
		}
	}
	if layout.depth <= 8 {
		// Bound the cost of the experimental histogram and candidate search.
		experimental := (options.NearLossless || options.QualityCheck != nil) && layout.sdr &&
			original.Bounds().Dx() >= 11 && original.Bounds().Dy() >= 11 && original.Bounds().Dx()*original.Bounds().Dy() <= 4_000_000
		if err := searchPalettes(original, experimental, consider); err != nil {
			return nil, err
		}
		if experimental {
			// Search fine steps first so strict checks can retain a useful
			// candidate even when all coarse quantisation is rejected.
			if options.QualityCheck != nil {
				for _, step := range []int{2, 3} {
					if err := consider(reduceColourPrecision(original, step), uint8(step/2)); err != nil {
						return nil, err
					}
				}
			}
			if err := searchMergedColours(original, consider); err != nil {
				return nil, err
			}
			// An odd step avoids rounding ties and can pass the local error
			// limit on images where the coarser grid is rejected.
			for _, step := range []int{4, 7, 8} {
				if err := consider(reduceColourPrecision(original, step), uint8(step/2)); err != nil {
					return nil, err
				}
			}
		}
	}
	result := layout.fallback
	if best != nil {
		best = recompressImageData(best)
		if len(best) < len(result) {
			result = best
		}
	}
	if options.Zopfli {
		// Keep the previous winner: filter rankings can change under Zopfli.
		filtered := optimiseFiltersLevel(result, zlib.BestCompression)
		changed := !bytes.Equal(filtered, result)
		result = recompressWithZopfli(result)
		if changed {
			filtered = recompressWithZopfli(filtered)
			if len(filtered) < len(result) {
				result = filtered
			}
		}
	}
	return result, nil
}
