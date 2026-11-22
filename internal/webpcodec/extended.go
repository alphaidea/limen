package webpcodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"

	encoder "github.com/SeriousBug/webp-go-pure/std"
	"golang.org/x/image/vp8"
	"golang.org/x/image/vp8l"
	"golang.org/x/image/webp"
)

const maxExtendedBytes = 64 << 20

var errAnimationFrame = errors.New("limen: invalid WebP animation frame")

func compressExtended(input []byte, check func([]byte, []byte) bool) ([]byte, error) {
	if len(input) > maxExtendedBytes || len(input) < 30 || string(input[12:16]) != "VP8X" ||
		binary.LittleEndian.Uint32(input[16:20]) != 10 {
		return input, nil
	}
	header := input[20:30]
	if header[0]&0xc1 != 0 || readUint24(header[1:]) != 0 {
		return input, nil
	}
	width, height := readUint24(header[4:])+1, readUint24(header[7:])+1
	if width > maxPixels/height {
		return input, nil
	}
	if header[0]&0x02 != 0 {
		return compressAnimation(input, image.Pt(width, height), check)
	}
	rest, ok := afterProfile(input[30:], header[0])
	if !ok {
		return input, nil
	}
	prefix := input[:len(input)-len(rest)]
	picture, rest := nextChunk(rest)
	alpha := chunkKind(picture) == "ALPH"
	if alpha {
		picture, rest = nextChunk(rest)
	}
	lossless := chunkKind(picture) == "VP8L"
	if (lossless && alpha) || (!lossless && (chunkKind(picture) != "VP8 " || alpha != (header[0]&0x10 != 0))) ||
		!supportedMetadata(rest, header[0]) {
		return input, nil
	}
	if !lossless && check == nil {
		return input, nil
	}
	if size, err := bitstreamSize(picture); err != nil || size != image.Pt(width, height) {
		return nil, errors.New("limen: WebP bitstream does not match canvas")
	}
	frame := standaloneFrame(input[len(prefix):len(input)-len(rest)], image.Pt(width, height), alpha)
	frameHeader := 12
	if alpha {
		frameHeader = 30
	}
	wrap := func(candidate []byte) []byte {
		out := make([]byte, 0, len(prefix)+len(candidate)-frameHeader+len(rest))
		out = append(out, prefix...)
		out = append(out, candidate[frameHeader:]...)
		return finishRIFF(append(out, rest...))
	}
	if lossless {
		candidate, err := recompressLosslessFrame(frame)
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			return input, nil
		}
		return wrap(candidate), nil
	}
	config := image.Config{Width: width, Height: height}
	var accepted []byte
	_, err := compressLossy(frame, config, func(_, candidate []byte) bool {
		out := wrap(candidate)
		if check(input, out) {
			accepted = out
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if accepted == nil {
		return input, nil
	}
	return accepted, nil
}

func afterProfile(data []byte, flags byte) ([]byte, bool) {
	if flags&0x20 == 0 {
		return data, true
	}
	chunk, rest := nextChunk(data)
	return rest, chunkKind(chunk) == "ICCP"
}

func supportedMetadata(data []byte, flags byte) bool {
	flags &= 0x0c
	for len(data) > 0 {
		chunk, rest := nextChunk(data)
		var bit byte
		switch chunkKind(chunk) {
		case "EXIF":
			bit = 0x08
		case "XMP ":
			bit = 0x04
		default:
			return false
		}
		if flags&bit == 0 {
			return false
		}
		flags &^= bit
		data = rest
	}
	return flags == 0
}

func readUint24(p []byte) int { return int(p[0]) | int(p[1])<<8 | int(p[2])<<16 }

// nextChunk returns the complete padded chunk, or leaves an invalid remainder.
func nextChunk(data []byte) (chunk, rest []byte) {
	if len(data) >= 8 {
		size := uint64(binary.LittleEndian.Uint32(data[4:8]))
		end := 8 + size + size%2
		if end <= uint64(len(data)) {
			return data[:int(end)], data[int(end):]
		}
	}
	return nil, data
}

func chunkKind(chunk []byte) string {
	if len(chunk) < 8 {
		return ""
	}
	return string(chunk[:4])
}

func bitstreamSize(chunk []byte) (image.Point, error) {
	n := int(binary.LittleEndian.Uint32(chunk[4:8]))
	payload := bytes.NewReader(chunk[8 : 8+n])
	if chunkKind(chunk) == "VP8L" {
		config, err := vp8l.DecodeConfig(payload)
		return image.Pt(config.Width, config.Height), err
	}
	decoder := vp8.NewDecoder()
	decoder.Init(payload, n)
	header, err := decoder.DecodeFrameHeader()
	return image.Pt(header.Width, header.Height), err
}

func appendChunk(out []byte, kind string, header, data []byte) []byte {
	out = append(out, kind...)
	size := len(header) + len(data)
	out = binary.LittleEndian.AppendUint32(out, uint32(size))
	out = append(out, header...)
	out = append(out, data...)
	if size%2 != 0 {
		out = append(out, 0)
	}
	return out
}

func finishRIFF(out []byte) []byte {
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out
}

func standaloneFrame(data []byte, size image.Point, alpha bool) []byte {
	out := make([]byte, 0, 30+len(data))
	out = append(out, "RIFF\x00\x00\x00\x00WEBP"...)
	if alpha {
		w, h := size.X-1, size.Y-1
		header := []byte{0x10, 0, 0, 0, byte(w), byte(w >> 8), byte(w >> 16), byte(h), byte(h >> 8), byte(h >> 16)}
		out = appendChunk(out, "VP8X", header, nil)
	}
	return finishRIFF(append(out, data...))
}

func compressAnimation(input []byte, canvas image.Point, check func([]byte, []byte) bool) ([]byte, error) {
	type frame struct {
		chunk []byte
		size  image.Point
	}
	rest, ok := afterProfile(input[30:], input[20])
	if !ok {
		return input, nil
	}
	control, rest := nextChunk(rest)
	if chunkKind(control) != "ANIM" || len(control) != 14 || binary.LittleEndian.Uint32(control[4:8]) != 6 {
		return input, nil
	}
	prefixEnd := len(input) - len(rest)
	var frames []frame
	totalPixels := 0
	for {
		chunk, tail := nextChunk(rest)
		if chunkKind(chunk) != "ANMF" {
			break
		}
		if len(chunk) < 32 || chunk[23]&0xfc != 0 {
			return nil, errAnimationFrame
		}
		position := image.Pt(2*readUint24(chunk[8:]), 2*readUint24(chunk[11:]))
		size := image.Pt(readUint24(chunk[14:])+1, readUint24(chunk[17:])+1)
		if position.X+size.X > canvas.X || position.Y+size.Y > canvas.Y {
			return nil, errAnimationFrame
		}
		totalPixels += size.X * size.Y
		if len(frames) >= 256 || totalPixels > 4*maxPixels {
			return input, nil
		}
		frames = append(frames, frame{chunk, size})
		rest = tail
	}
	if len(frames) == 0 || !supportedMetadata(rest, input[20]) {
		return input, nil
	}
	suffix := rest
	exact := append(make([]byte, 0, len(input)), input[:prefixEnd]...)
	var gated []byte
	if check != nil && totalPixels <= maxPixels && canvas.X*canvas.Y <= 4*maxPixels/len(frames) {
		gated = append(make([]byte, 0, len(input)), input[:prefixEnd]...)
	}
	for _, frame := range frames {
		original := frame.chunk
		data := original[24 : 8+int(binary.LittleEndian.Uint32(original[4:8]))]
		chunk, rest := nextChunk(data)
		alpha := chunkKind(chunk) == "ALPH"
		if alpha {
			chunk, rest = nextChunk(rest)
		}
		lossless := chunkKind(chunk) == "VP8L"
		if chunk == nil || len(rest) != 0 || (lossless && alpha) || (!lossless && chunkKind(chunk) != "VP8 ") {
			// An unknown frame subchunk can have presentation semantics.
			return input, nil
		}
		if size, err := bitstreamSize(chunk); err != nil || size != frame.size {
			return nil, errAnimationFrame
		}
		var wrapped []byte
		if lossless || gated != nil {
			wrapped = standaloneFrame(data, frame.size, alpha)
		}
		if lossless {
			candidate, err := recompressLosslessFrame(wrapped)
			if err != nil {
				return nil, err
			}
			if candidate != nil {
				original = appendChunk(nil, "ANMF", original[8:24], candidate[12:])
			}
		}
		exact = append(exact, original...)
		if gated != nil {
			if !lossless {
				config := image.Config{Width: frame.size.X, Height: frame.size.Y}
				// Prefer the first smaller candidate in quality search order;
				// the caller assesses the entire composited animation below.
				candidate, err := searchLossy(wrapped, config, func(_, _ []byte) bool { return true }, true)
				if err != nil {
					return nil, err
				}
				if len(candidate) < len(wrapped) {
					headerSize := 12
					if alpha {
						headerSize = 30
					}
					original = appendChunk(nil, "ANMF", original[8:24], candidate[headerSize:])
				}
			}
			gated = append(gated, original...)
		}
	}
	exact = finishRIFF(append(exact, suffix...))
	if len(exact) >= len(input) {
		exact = input
	}
	if gated != nil {
		gated = finishRIFF(append(gated, suffix...))
		if len(gated) < len(exact) && check(input, gated) {
			return gated, nil
		}
	}
	return exact, nil
}

func recompressLosslessFrame(input []byte) ([]byte, error) {
	source, err := webp.Decode(bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	candidate, err := encoder.EncodeBytes(source, &encoder.Options{Lossless: true, Effort: 6})
	if err != nil || len(candidate) >= len(input) {
		return nil, err
	}
	if chunk, rest := nextChunk(candidate[12:]); chunkKind(chunk) != "VP8L" || len(rest) != 0 {
		return nil, nil
	}
	decoded, err := webp.Decode(bytes.NewReader(candidate))
	if err != nil || !equivalent(source, decoded) {
		return nil, nil
	}
	return candidate, nil
}
