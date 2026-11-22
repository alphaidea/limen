package jpegcodec

import (
	"bytes"
	"image"
	"image/jpeg"
)

const maxSearchPixels = 4_000_000

func (layout *scan) search(input, best []byte, preserveMetadata bool, headerSize int, legacy *candidate, qualityCheck func(original, candidate []byte) bool) []byte {
	original, err := jpeg.Decode(bytes.NewReader(input))
	if err != nil || min(original.Bounds().Dx(), original.Bounds().Dy()) < 11 {
		return best
	}
	reference := newQualityReference(original)
	visited := make(map[quantisation]bool)
	consider := func(output []byte) bool {
		if len(output) == 0 || len(output) >= len(best)+len(best)/20 {
			return false
		}
		decoded, err := jpeg.Decode(bytes.NewReader(output))
		if err != nil || !reference.accepts(decoded) {
			return false
		}
		output = progressiveCandidate(output, original.Bounds().Dx(), original.Bounds().Dy())
		if len(output) >= len(best) || !qualityCheck(input, output) {
			return false
		}
		best = output
		return true
	}
	try := func(tables quantisation) bool {
		if tables == layout.quantisation {
			return true
		}
		if accepted, ok := visited[tables]; ok {
			return accepted
		}
		var frequency frequencies
		reader := bitReader{data: input, offset: layout.start}
		layout.walk(&reader, nil, &frequency, nil, nil, true, &tables, nil)
		encoded := makeCandidate(&frequency, headerSize)
		// A baseline candidate slightly larger than the current best can
		// still win after progressive entropy coding.
		accepted := encoded.size < len(best)+len(best)/20 &&
			consider(layout.encode(input, &encoded, preserveMetadata, true, &tables))
		visited[tables] = accepted
		return accepted
	}

	var lumaTable [64]byte
	for _, part := range layout.components {
		if part.luma {
			lumaTable = layout.quantisation[part.quantisation]
			break
		}
	}
	strongChroma := lumaTable[32] <= 3 && len(layout.components) == 3
	improved := false
	if strongChroma {
		// Fine luma steps benefit from separate, coarser chroma tables.
		size := len(best)
		if !try(layout.frequencyQuantisation(3, 16)) {
			if !try(layout.frequencyQuantisation(3, 12)) {
				try(layout.frequencyQuantisation(2, 6))
			}
		}
		improved = len(best) < size
	}

	// Start from the source's quantisation and expand or narrow the search
	// according to the decoded candidate. Retain the best actual byte count.
	if !improved {
		level := min(12, max(2, int(lumaTable[32])))
		low, high := 0, 0
		for range 5 {
			if try(layout.quantisationFor(level)) {
				low = level
				if high == 0 {
					level *= 2
					continue
				}
			} else {
				high = level
			}
			if high-low <= 1 {
				break
			}
			level = (low + high) / 2
		}
	}
	if strongChroma {
		try(layout.frequencyQuantisation(2, 12))
		// Explore finer luma increments when the original quantisation grid
		// is dense. The tilt favours preserving lower spatial frequencies.
		for _, level := range [...]int{32, 30, 28} {
			if try(layout.radialQuantisation(level, 17, 2)) {
				break
			}
		}
		if colour, ok := original.(*image.YCbCr); ok && colour.SubsampleRatio == image.YCbCrSubsampleRatio444 {
			consider(layout.subsample(colour, preserveMetadata))
		}
	} else {
		for _, level := range []int{4, 5} {
			try(layout.frequencyQuantisation(level, level+2))
		}
		try(layout.frequencyQuantisation(4, 2))
	}
	try(layout.quantisationFor(2))
	if legacy != nil && legacy.size < len(best) {
		consider(layout.encode(input, legacy, preserveMetadata, true, nil))
	}
	return best
}

func (layout *scan) subsample(source *image.YCbCr, preserveMetadata bool) []byte {
	// Try a fine 4:2:0 encode and preserve the original display metadata.
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, source, &jpeg.Options{Quality: 98}); err != nil {
		return nil
	}
	data := buffer.Bytes()
	bounds := source.Bounds()
	encoded, err := inspect(data, bounds.Dx(), bounds.Dy())
	if err != nil || encoded == nil {
		return nil
	}
	optimised, err := encoded.compress(data, true, nil)
	if err != nil {
		return nil
	}
	result := append(make([]byte, 0, len(optimised)), optimised[:2]...)
	for _, part := range layout.segments {
		if part.marker >= 0xe0 && part.marker <= 0xef || part.marker == 0xfe && preserveMetadata {
			result = append(result, part.data...)
		}
	}
	result = appendVisualMarker(result)
	return append(result, optimised[2:]...)
}
