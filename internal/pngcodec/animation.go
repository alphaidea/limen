package pngcodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/png"
)

const maxAnimationPixels = 50_000_000

type animationChunk struct {
	kind  string
	data  []byte
	frame int
}
type animationFrame struct {
	control       []byte
	compressed    []byte
	width, height int
}
type animation struct {
	header       []byte
	chunks       []animationChunk
	frames       []animationFrame
	palette      []byte
	transparency []byte
	pixels       int
}

// parseAnimation validates animation structure before any allocation by dimensions.
// inspectPNG has already validated PNG chunk boundaries and checksums.
func parseAnimation(data []byte) (*animation, error) {
	a := &animation{header: bytes.Clone(data[16:29])}
	width, height := binary.BigEndian.Uint32(a.header), binary.BigEndian.Uint32(a.header[4:])
	var declared, controls, sequence uint32
	active := -1
	seenData, seenAnimation := false, false
	invalid := errors.New("limen: invalid APNG structure")
	for offset := 8; offset < len(data); {
		size := int(binary.BigEndian.Uint32(data[offset:]))
		kind := string(data[offset+4 : offset+8])
		payload := data[offset+8 : offset+8+size]
		offset += size + 12
		chunk := animationChunk{kind: kind, data: payload, frame: -1}
		switch kind {
		case "acTL":
			if seenAnimation || seenData || len(payload) != 8 {
				return nil, invalid
			}
			seenAnimation = true
			declared = binary.BigEndian.Uint32(payload)
			if declared == 0 || declared > 10000 {
				return nil, invalid
			}
		case "fcTL":
			if !seenAnimation || controls >= declared || len(payload) != 26 || binary.BigEndian.Uint32(payload) != sequence {
				return nil, invalid
			}
			sequence++
			if active >= 0 && len(a.frames[active].compressed) == 0 {
				return nil, invalid
			}
			w, h, x, y := binary.BigEndian.Uint32(payload[4:]), binary.BigEndian.Uint32(payload[8:]), binary.BigEndian.Uint32(payload[12:]), binary.BigEndian.Uint32(payload[16:])
			if w == 0 || h == 0 || w > width || h > height || x > width-w || y > height-h || payload[24] > 2 || payload[25] > 1 {
				return nil, invalid
			}
			if !seenData && (w != width || h != height || x != 0 || y != 0) {
				return nil, invalid
			}
			if uint64(w)*uint64(h) > uint64(maxAnimationPixels-a.pixels) {
				return nil, errors.New("limen: APNG exceeds total pixel limit")
			}
			a.pixels += int(w) * int(h)
			a.frames = append(a.frames, animationFrame{control: payload, width: int(w), height: int(h)})
			active = len(a.frames) - 1
			controls++
			chunk.frame = active
		case "IDAT":
			if !seenAnimation {
				return nil, invalid
			}
			if !seenData && active < 0 {
				if uint64(width)*uint64(height) > maxAnimationPixels {
					return nil, invalid
				}
				a.pixels += int(width) * int(height)
				a.frames = append(a.frames, animationFrame{width: int(width), height: int(height)})
				active = 0
			}
			if active != 0 {
				return nil, invalid
			}
			seenData = true
			chunk.frame = 0
			a.frames[0].compressed = append(a.frames[0].compressed, payload...)
		case "fdAT":
			if !seenData || active < 0 || len(a.frames[active].control) == 0 || len(payload) < 4 || binary.BigEndian.Uint32(payload) != sequence || (active == 0) {
				return nil, invalid
			}
			sequence++
			chunk.frame = active
			a.frames[active].compressed = append(a.frames[active].compressed, payload[4:]...)
		case "PLTE":
			a.palette = payload
		case "tRNS":
			a.transparency = payload
		}
		a.chunks = append(a.chunks, chunk)
	}
	if !seenAnimation || controls != declared || active < 0 || len(a.frames[active].compressed) == 0 {
		return nil, invalid
	}
	return a, nil
}

func (a *animation) framePNG(index int) []byte {
	frame := a.frames[index]
	header := bytes.Clone(a.header)
	binary.BigEndian.PutUint32(header, uint32(frame.width))
	binary.BigEndian.PutUint32(header[4:], uint32(frame.height))
	data := appendAnimationChunk([]byte(pngSignature), "IHDR", header)
	if a.palette != nil {
		data = appendAnimationChunk(data, "PLTE", a.palette)
	}
	if a.transparency != nil {
		data = appendAnimationChunk(data, "tRNS", a.transparency)
	}
	data = appendAnimationChunk(data, "IDAT", frame.compressed)
	return appendAnimationChunk(data, "IEND", nil)
}

func (a *animation) encode(streams [][]byte, preserve bool) []byte {
	result := []byte(pngSignature)
	sequence := uint32(0)
	emitted := make([]bool, len(a.frames))
	for _, chunk := range a.chunks {
		payload := chunk.data
		switch chunk.kind {
		case "tEXt", "zTXt", "iTXt", "tIME":
			if !preserve {
				continue
			}
		case "fcTL":
			payload = bytes.Clone(payload)
			binary.BigEndian.PutUint32(payload, sequence)
			sequence++
		case "IDAT", "fdAT":
			if emitted[chunk.frame] {
				continue
			}
			emitted[chunk.frame] = true
			payload = streams[chunk.frame]
			if chunk.kind == "fdAT" {
				payload = append(binary.BigEndian.AppendUint32(nil, sequence), payload...)
				sequence++
			}
		}
		result = appendAnimationChunk(result, chunk.kind, payload)
	}
	return result
}

func compressAnimation(input []byte, options Options) ([]byte, error) {
	a, err := parseAnimation(input)
	if err != nil {
		return nil, err
	}
	streams := make([][]byte, len(a.frames))
	for i := range a.frames {
		original := a.framePNG(i)
		decoded, err := png.Decode(bytes.NewReader(original))
		if err != nil {
			return nil, err
		}
		candidate := optimiseAnimationFilters(original)
		if options.Zopfli {
			candidate = recompressWithZopfli(candidate)
		}
		// A separate decoder verifies each reconstructed frame before admission.
		verified, err := png.Decode(bytes.NewReader(candidate))
		if err != nil || !equivalentPNG(decoded, verified, 0) {
			return nil, errors.New("limen: APNG frame verification failed")
		}
		_, _, stream := imageData(candidate)
		streams[i] = stream.Bytes()
	}
	result := a.encode(streams, options.PreserveMetadata)
	streamBytes := 0
	for _, stream := range streams {
		streamBytes += len(stream)
	}
	// Highly repetitive frames already encode below one bit per pixel;
	// palette construction did not improve these in the measured corpus.
	if palette := a.paletteForSize(streamBytes); palette != nil {
		if indexed := a.palettedCandidate(palette, options); indexed != nil && len(indexed) < len(result) {
			result = indexed
		}
	}
	if len(result) < len(input) {
		return result, nil
	}
	return input, nil
}

// AnimationEquivalent proves equivalence from identical frame controls and
// decoded frame samples. This implies identical compositing at every timestamp.
// It intentionally rejects differently structured but possibly equivalent files.
func AnimationEquivalent(original, candidate []byte) bool {
	for _, data := range [][]byte{original, candidate} {
		if _, err := inspectPNG(data, Options{PreserveMetadata: true}); err != nil {
			return false
		}
	}
	a, err := parseAnimation(original)
	if err != nil {
		return false
	}
	b, err := parseAnimation(candidate)
	if err != nil {
		return false
	}
	if !bytes.Equal(a.header[:8], b.header[:8]) || len(a.frames) != len(b.frames) {
		return false
	}
	for i := range a.frames {
		ac, bc := a.frames[i].control, b.frames[i].control
		if len(ac) != len(bc) || (len(ac) > 0 && !bytes.Equal(ac[4:], bc[4:])) {
			return false
		}
		x, err := png.Decode(bytes.NewReader(a.framePNG(i)))
		if err != nil {
			return false
		}
		y, err := png.Decode(bytes.NewReader(b.framePNG(i)))
		if err != nil {
			return false
		}
		if !equivalentPNG(x, y, 0) {
			return false
		}
	}
	// Colour interpretation and loop count are part of the rendering contract.
	for _, kind := range []string{"acTL", "iCCP", "sRGB", "gAMA", "cHRM", "cICP", "mDCV", "cLLI"} {
		var x, y []byte
		for _, c := range a.chunks {
			if c.kind == kind {
				x = append(x, c.data...)
			}
		}
		for _, c := range b.chunks {
			if c.kind == kind {
				y = append(y, c.data...)
			}
		}
		if !bytes.Equal(x, y) {
			return false
		}
	}
	return true
}
