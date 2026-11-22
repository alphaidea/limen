package pngcodec

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"slices"
)

// A shared exact palette is required: APNG has one colour model for all frames.
// Stop on the 257th colour rather than constructing an unbounded histogram.
func (a *animation) paletteForSize(streamBytes int) color.Palette {
	if streamBytes <= a.pixels/8 {
		return nil
	}
	return a.exactPalette()
}

func (a *animation) exactPalette() color.Palette {
	if a.header[8] != 8 || (a.header[9] != 2 && a.header[9] != 6) {
		return nil
	}
	for _, chunk := range a.chunks {
		switch chunk.kind {
		case "IHDR", "IDAT", "IEND", "acTL", "fcTL", "fdAT", "PLTE", "tRNS", "sRGB", "gAMA", "cHRM", "iCCP", "pHYs", "tEXt", "zTXt", "iTXt", "tIME", "eXIf":
		default:
			return nil // Unrecognised metadata may depend on the colour model.
		}
	}
	seen := make(map[color.NRGBA]bool, 256)
	var palette color.Palette
	for i := range a.frames {
		decoded, err := png.Decode(bytes.NewReader(a.framePNG(i)))
		if err != nil {
			return nil
		}
		bounds := decoded.Bounds()
		for y := 0; y < bounds.Dy(); y++ {
			for x := 0; x < bounds.Dx(); x++ {
				c := pixelColour(decoded, x, y)
				if !seen[c] {
					if len(palette) == 256 {
						return nil
					}
					seen[c] = true
					palette = append(palette, c)
				}
			}
		}
	}
	slices.SortFunc(palette, func(a, b color.Color) int { return compareColours(a.(color.NRGBA), b.(color.NRGBA)) })
	return palette
}

func (a *animation) palettedCandidate(palette color.Palette, options Options) []byte {
	lookup := make(map[color.NRGBA]uint8, len(palette))
	for i, c := range palette {
		lookup[c.(color.NRGBA)] = uint8(i)
	}
	streams := make([][]byte, len(a.frames))
	var header, plte, trns []byte
	for i := range a.frames {
		decoded, err := png.Decode(bytes.NewReader(a.framePNG(i)))
		if err != nil {
			return nil
		}
		indexed := image.NewPaletted(decoded.Bounds(), palette)
		for y := 0; y < indexed.Rect.Dy(); y++ {
			for x := 0; x < indexed.Rect.Dx(); x++ {
				indexed.SetColorIndex(x, y, lookup[pixelColour(decoded, x, y)])
			}
		}
		var encoded bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
		if encoder.Encode(&encoded, indexed) != nil {
			return nil
		}
		data := optimiseAnimationFilters(encoded.Bytes())
		if options.Zopfli {
			data = recompressWithZopfli(data)
		}
		verified, err := png.Decode(bytes.NewReader(data))
		if err != nil || !equivalentPNG(decoded, verified, 0) {
			return nil
		}
		_, _, stream := imageData(data)
		streams[i] = stream.Bytes()
		if i == 0 {
			header = bytes.Clone(data[16:29])
			copy(header[:8], a.header[:8])
			for offset := 8; offset < len(data); {
				size := int(binary.BigEndian.Uint32(data[offset:]))
				kind := string(data[offset+4 : offset+8])
				if kind == "PLTE" {
					plte = bytes.Clone(data[offset+8 : offset+8+size])
				}
				if kind == "tRNS" {
					trns = bytes.Clone(data[offset+8 : offset+8+size])
				}
				offset += size + 12
			}
		}
	}
	candidate := *a
	candidate.chunks = nil
	inserted := false
	for _, chunk := range a.chunks {
		if chunk.kind == "PLTE" || chunk.kind == "tRNS" {
			continue
		}
		if chunk.kind == "IHDR" {
			chunk.data = header
		}
		if !inserted && chunk.kind == "IDAT" {
			candidate.chunks = append(candidate.chunks, animationChunk{kind: "PLTE", data: plte, frame: -1})
			if trns != nil {
				candidate.chunks = append(candidate.chunks, animationChunk{kind: "tRNS", data: trns, frame: -1})
			}
			inserted = true
		}
		candidate.chunks = append(candidate.chunks, chunk)
	}
	return candidate.encode(streams, options.PreserveMetadata)
}
