// Package gifcodec recompresses GIF raster streams without changing rendering.
package gifcodec

import (
	"bytes"
	"compress/lzw"
	"encoding/binary"
	"errors"
	"image"
	"image/gif"
	"io"
	"math/bits"
)

var errInvalid = errors.New("gif: invalid or truncated stream")

// Compress preserves every rendering block and rewrites only smaller LZW streams.
// Work is bounded to 32 million raster pixels and 64 MiB of encoded input.
func Compress(input []byte, preserveMetadata bool) ([]byte, error) {
	if len(input) < 13 || (string(input[:6]) != "GIF87a" && string(input[:6]) != "GIF89a") {
		return nil, errInvalid
	}
	if len(input) > 64<<20 {
		return input, nil
	}
	position := 13
	if input[10]&128 != 0 {
		position += 3 * (2 << uint(input[10]&7))
	}
	if position > len(input) {
		return nil, errInvalid
	}
	output := append([]byte(nil), input[:position]...)
	total := uint64(0)
	var reader lzw.Reader
	var writer lzw.Writer
	var encoded bytes.Buffer
	var pixels []byte
	frames := 0
	for position < len(input) {
		start := position
		switch input[position] {
		case 0x3b:
			if frames == 0 {
				return nil, errInvalid
			}
			output = append(output, input[position:]...)
			if len(output) < len(input) {
				return output, nil
			}
			return input, nil
		case 0x21:
			if position+2 > len(input) {
				return nil, errInvalid
			}
			label := input[position+1]
			position += 2
			end, _, err := blocks(input, position, false)
			if err != nil {
				return nil, err
			}
			position = end
			if preserveMetadata || label != 0xfe {
				output = append(output, input[start:position]...)
			}
		case 0x2c:
			if position+10 > len(input) {
				return nil, errInvalid
			}
			width := uint64(binary.LittleEndian.Uint16(input[position+5:]))
			height := uint64(binary.LittleEndian.Uint16(input[position+7:]))
			if width == 0 || height == 0 {
				return nil, errInvalid
			}
			total += width * height
			if total > 32<<20 {
				return input, nil
			}
			packed := input[position+9]
			position += 10
			if packed&128 != 0 {
				position += 3 * (2 << uint(packed&7))
			}
			if position >= len(input) {
				return nil, errInvalid
			}
			rasterStart := position
			size := input[position]
			position++
			if size < 2 || size > 8 {
				return nil, errInvalid
			}
			end, compressed, err := blocks(input, position, true)
			if err != nil {
				return nil, err
			}
			position = end
			reader.Reset(bytes.NewReader(compressed), lzw.LSB, int(size))
			count := int(width * height)
			if count > cap(pixels) {
				pixels = make([]byte, count)
			} else {
				pixels = pixels[:count]
			}
			_, err = io.ReadFull(&reader, pixels)
			var extra [1]byte
			n, endErr := reader.Read(extra[:])
			reader.Close()
			if err != nil || n != 0 || endErr != io.EOF {
				return nil, errInvalid
			}
			var usedBits byte
			for _, pixel := range pixels {
				usedBits |= pixel
			}
			minimum := max(2, bits.Len8(usedBits))
			encoded.Reset()
			writer.Reset(&encoded, lzw.LSB, minimum)
			if _, err = writer.Write(pixels); err != nil {
				return nil, err
			}
			if err = writer.Close(); err != nil {
				return nil, err
			}
			data := encoded.Bytes()
			codeSize := byte(minimum)
			if len(compressed) < len(data) {
				data, codeSize = compressed, size
			}
			output = append(output, input[start:rasterStart]...)
			if 2+len(data)+(len(data)+254)/255 < position-rasterStart {
				output = append(output, codeSize)
				for len(data) > 0 {
					n := min(255, len(data))
					output = append(output, byte(n))
					output = append(output, data[:n]...)
					data = data[n:]
				}
				output = append(output, 0)
			} else {
				output = append(output, input[rasterStart:position]...)
			}
			frames++
		default:
			return nil, errInvalid
		}
	}
	return nil, errInvalid
}

func blocks(input []byte, position int, collect bool) (int, []byte, error) {
	var data []byte
	for position < len(input) {
		n := int(input[position])
		position++
		if n == 0 {
			return position, data, nil
		}
		if n > len(input)-position {
			return 0, nil, errInvalid
		}
		if collect {
			data = append(data, input[position:position+n]...)
		}
		position += n
	}
	return 0, nil, errInvalid
}

// WebPSource accepts a single full-canvas raster with square pixels and no extensions.
func WebPSource(input []byte) image.Image {
	if len(input) < 13 || input[12] != 0 {
		return nil
	}
	width := int(binary.LittleEndian.Uint16(input[6:]))
	height := int(binary.LittleEndian.Uint16(input[8:]))
	if height == 0 || width == 0 || width > 4_000_000/height {
		return nil
	}
	pos := 13
	if input[10]&128 != 0 {
		pos += 3 * (2 << (input[10] & 7))
	}
	if pos+11 > len(input) || input[pos] != 0x2c || binary.LittleEndian.Uint32(input[pos+1:]) != 0 || !bytes.Equal(input[6:10], input[pos+5:pos+9]) {
		return nil
	}
	flags := input[pos+9]
	pos += 10
	if flags&128 != 0 {
		pos += 3 * (2 << (flags & 7))
	}
	end, _, err := blocks(input, pos+1, false)
	if err != nil || end != len(input)-1 || input[end] != 0x3b {
		return nil
	}
	source, err := gif.Decode(bytes.NewReader(input))
	if err != nil {
		return nil
	}
	return source
}
