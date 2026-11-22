// Package webpcodec generates independently verified, pixel-equivalent WebP.
package webpcodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"

	codec "github.com/SeriousBug/webp-go-pure"
	encoder "github.com/SeriousBug/webp-go-pure/std"
	"golang.org/x/image/webp"
)

const maxPixels = 4_000_000

// Encode returns nil when encoding cannot produce a smaller equivalent image.
func Encode(source image.Image, limit int) ([]byte, error) {
	b := source.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 || b.Dx() > 16383 || b.Dy() > 16383 || b.Dx() > maxPixels/b.Dy() {
		return nil, nil
	}
	out, err := encoder.EncodeBytes(source, &encoder.Options{Lossless: true, Effort: encoder.EffortFastest})
	if err != nil {
		return nil, err
	}
	if len(out) >= limit {
		return nil, nil
	}
	decoded, err := webp.Decode(bytes.NewReader(out))
	if err != nil {
		return nil, err
	}
	if !equivalent(source, decoded) {
		return nil, errors.New("limen: WebP pixel verification failed")
	}
	return out, nil
}

func equivalent(a, b image.Image) bool {
	if a.Bounds().Size() != b.Bounds().Size() {
		return false
	}
	first, firstOK := a.(*image.NRGBA)
	second, secondOK := b.(*image.NRGBA)
	if firstOK && secondOK {
		width := a.Bounds().Dx() * 4
		for y := 0; y < a.Bounds().Dy(); y++ {
			left := first.Pix[y*first.Stride : y*first.Stride+width]
			right := second.Pix[y*second.Stride : y*second.Stride+width]
			if bytes.Equal(left, right) {
				continue
			}
			for x := 0; x < width; x += 4 {
				if left[x+3] != right[x+3] || left[x+3] != 0 && (left[x] != right[x] || left[x+1] != right[x+1] || left[x+2] != right[x+2]) {
					return false
				}
			}
		}
		return true
	}
	for y := 0; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			p := color.NRGBA64Model.Convert(a.At(x+a.Bounds().Min.X, y+a.Bounds().Min.Y)).(color.NRGBA64)
			q := color.NRGBA64Model.Convert(b.At(x+b.Bounds().Min.X, y+b.Bounds().Min.Y)).(color.NRGBA64)
			if p.A != q.A || p.A != 0 && (p.R != q.R || p.G != q.G || p.B != q.B) {
				return false
			}
		}
	}
	return true
}

// Compress searches for smaller WebP files while preserving presentation properties.
func Compress(input []byte, check func([]byte, []byte) bool) ([]byte, error) {
	if len(input) < 12 || string(input[:4]) != "RIFF" || string(input[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(input[4:8]))+8 != uint64(len(input)) {
		return nil, errors.New("limen: invalid WebP container")
	}
	chunks := 0
	simple := true
	for pos := 12; pos < len(input); {
		if len(input)-pos < 8 {
			return nil, errors.New("limen: truncated WebP chunk")
		}
		size := uint64(binary.LittleEndian.Uint32(input[pos+4 : pos+8]))
		end := uint64(pos) + 8 + size + (size & 1)
		if end > uint64(len(input)) {
			return nil, errors.New("limen: truncated WebP payload")
		}
		kind := string(input[pos : pos+4])
		if kind != "VP8 " && kind != "VP8L" {
			simple = false
		}
		chunks++
		pos = int(end)
	}
	if !simple {
		return compressExtended(input, check)
	}
	if chunks != 1 {
		return nil, errors.New("limen: invalid WebP image chunks")
	}
	config, err := webp.DecodeConfig(bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxPixels/config.Height {
		return input, nil
	}
	if string(input[12:16]) == "VP8 " {
		return compressLossy(input, config, check)
	}
	source, err := webp.Decode(bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	best := input
	candidate, err := Encode(source, len(input))
	if err != nil {
		return nil, err
	}
	if candidate != nil {
		best = candidate
	}
	if check != nil && config.Width >= 11 && config.Height >= 11 {
		for _, step := range []int{2, 4, 8} {
			adjusted := quantise(source, step)
			encoded, encodeErr := encoder.EncodeBytes(adjusted, &encoder.Options{Lossless: true, Effort: 6})
			if encodeErr != nil {
				return nil, encodeErr
			}
			if len(encoded) >= len(best) {
				continue
			}
			decoded, decodeErr := webp.Decode(bytes.NewReader(encoded))
			if decodeErr == nil && equivalent(adjusted, decoded) && check(input, encoded) {
				best = encoded
			}
		}
	}
	return best, nil
}

// quantise leaves alpha unchanged; perceptual acceptance is always external.
func quantise(source image.Image, step int) *image.NRGBA {
	bounds := source.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	round := func(v byte) byte { return byte(min(255, (int(v)+step/2)/step*step)) }
	if packed, ok := source.(*image.NRGBA); ok {
		for y := 0; y < bounds.Dy(); y++ {
			row := out.Pix[y*out.Stride : y*out.Stride+4*bounds.Dx()]
			copy(row, packed.Pix[y*packed.Stride:y*packed.Stride+len(row)])
			for i := 0; i < len(row); i += 4 {
				if row[i+3] != 0 {
					row[i+2] = round(row[i+2])
				}
			}
		}
		return out
	}
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			c := color.NRGBAModel.Convert(source.At(x+bounds.Min.X, y+bounds.Min.Y)).(color.NRGBA)
			if c.A != 0 {
				c.B = round(c.B)
			}
			out.SetNRGBA(x, y, c)
		}
	}
	return out
}

func compressLossy(input []byte, config image.Config, check func([]byte, []byte) bool) ([]byte, error) {
	return searchLossy(input, config, check, false)
}

func searchLossy(input []byte, config image.Config, check func([]byte, []byte) bool, firstMatch bool) ([]byte, error) {
	if check == nil || config.Width < 11 || config.Height < 11 {
		return input, nil
	}
	planes, err := codec.DecodeYUV(input)
	if err != nil {
		return nil, err
	}
	// Use an independent decoder for the alpha reference as well as candidates.
	var source image.Image
	if planes.A != nil {
		source, err = webp.Decode(bytes.NewReader(input))
		if err != nil {
			return nil, err
		}
	}
	best := input
	try := func(quality uint8, effort uint8) (bool, error) {
		encoded, err := codec.EncodeLossyYUV(&planes, &codec.LossyOptions{Quality: quality, Effort: effort})
		if err != nil || len(encoded) >= len(best) {
			return false, err
		}
		decoded, err := webp.Decode(bytes.NewReader(encoded))
		if err != nil || decoded.Bounds().Size() != image.Pt(config.Width, config.Height) {
			return false, nil
		}
		if source != nil {
			a, aOK := source.(*image.NYCbCrA)
			b, bOK := decoded.(*image.NYCbCrA)
			if !aOK || !bOK || !bytes.Equal(a.A, b.A) {
				return false, nil
			}
			chunk, rest := nextChunk(encoded[12:])
			alpha, rest := nextChunk(rest)
			picture, rest := nextChunk(rest)
			if chunkKind(chunk) != "VP8X" || chunkKind(alpha) != "ALPH" || chunkKind(picture) != "VP8 " || len(rest) != 0 {
				return false, nil
			}
		}
		if !check(input, encoded) {
			return false, nil
		}
		best = encoded
		return true, nil
	}
	var bestQuality uint8
	for _, quality := range []uint8{98, 96, 94, 90, 97, 95} {
		accepted, err := try(quality, 6)
		if err != nil {
			return nil, err
		}
		if accepted {
			if firstMatch {
				return best, nil
			}
			bestQuality = quality
		}
	}
	if bestQuality != 0 {
		if _, err := try(bestQuality, 8); err != nil {
			return nil, err
		}
	}
	return best, nil
}
