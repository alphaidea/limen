// Package bmpcodec compresses conventional Windows RGB bitmaps losslessly.
package bmpcodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/alphaidea/limen/internal/webpcodec"
	"golang.org/x/image/bmp"
)

// Compress preserves unfamiliar colour, metadata and bitmap layouts unchanged.
func Compress(input []byte, keepFormat, preserveMetadata bool) ([]byte, bool, error) {
	if len(input) < 54 {
		return nil, false, errors.New("limen: truncated BMP")
	}
	u := binary.LittleEndian
	if len(input) > 64<<20 || preserveMetadata || u.Uint32(input[14:]) != 40 {
		return input, false, nil
	}
	w, h := int64(int32(u.Uint32(input[18:]))), int64(int32(u.Uint32(input[22:])))
	if h < 0 {
		h = -h
	}
	if w <= 0 || h <= 0 {
		return nil, false, errors.New("limen: invalid BMP dimensions")
	}
	if w > 4_000_000/h {
		return input, false, nil
	}
	depth := int(u.Uint16(input[28:]))
	offset := int64(u.Uint32(input[10:]))
	if u.Uint16(input[26:]) != 1 || u.Uint32(input[30:]) != 0 || (depth != 24 && depth != 8) || u.Uint32(input[50:]) != 0 || u.Uint32(input[6:]) != 0 {
		return input, false, nil
	}
	stride := (w*int64(depth) + 31) / 32 * 4
	if offset < 54 || offset+stride*h != int64(len(input)) || int64(u.Uint32(input[2:])) != int64(len(input)) {
		return input, false, nil
	}
	if depth == 24 && (offset != 54 || u.Uint32(input[46:]) != 0) {
		return input, false, nil
	}
	if depth == 8 {
		n := int(u.Uint32(input[46:]))
		if n == 0 {
			n = 256
		}
		if n > 256 || offset != int64(54+4*n) {
			return input, false, nil
		}
		for i := 0; i < n; i++ {
			if input[54+4*i+3] != 0 {
				return input, false, nil
			}
		}
	}
	tryWebP := !keepFormat && u.Uint32(input[38:]) == 0 && u.Uint32(input[42:]) == 0
	// A 24-bit image with more than 256 colours cannot use an indexed BMP.
	// Check a bounded prefix before allocating a decoded photograph.
	if depth == 24 && !tryWebP && w*h >= 4096 && string(input[:2]) == "BM" {
		colours := make(map[uint32]struct{}, 256)
		for i := int64(0); i < min(w*h, 1024); i++ {
			pos := offset + i/w*stride + i%w*3
			key := uint32(input[pos]) | uint32(input[pos+1])<<8 | uint32(input[pos+2])<<16
			colours[key] = struct{}{}
			if len(colours) > 256 {
				return input, false, nil
			}
		}
	}
	source, err := bmp.Decode(bytes.NewReader(input))
	if err != nil {
		return nil, false, err
	}
	// WebP cannot carry BMP pixel-density metadata without an additional contract.
	if tryWebP {
		out, _ := webpcodec.Encode(source, len(input))
		if out != nil {
			return out, true, nil
		}
	}
	colours := make(map[uint32]byte, 256)
	palette := make([]uint32, 0, 256)
	indices := make([]byte, int(w*h))
	for y := 0; y < int(h); y++ {
		for x := 0; x < int(w); x++ {
			r, g, b, _ := source.At(x, y).RGBA()
			key := r>>8<<16 | g>>8<<8 | b>>8
			index, ok := colours[key]
			if !ok {
				if len(palette) == 256 {
					return input, false, nil
				}
				index = byte(len(palette))
				colours[key] = index
				palette = append(palette, key)
			}
			indices[y*int(w)+x] = index
		}
	}
	bits := 8
	if len(palette) <= 2 {
		bits = 1
	} else if len(palette) <= 16 {
		bits = 4
	}
	row := (int(w)*bits + 31) / 32 * 4
	start := 54 + 4*len(palette)
	size := start + row*int(h)
	if size >= len(input) {
		return input, false, nil
	}
	out := make([]byte, size)
	copy(out, input[:54])
	u.PutUint32(out[2:], uint32(size))
	u.PutUint32(out[10:], uint32(start))
	u.PutUint16(out[28:], uint16(bits))
	u.PutUint32(out[34:], uint32(row*int(h)))
	u.PutUint32(out[46:], uint32(len(palette)))
	for i, c := range palette {
		out[54+4*i] = byte(c)
		out[55+4*i] = byte(c >> 8)
		out[56+4*i] = byte(c >> 16)
	}
	for y := 0; y < int(h); y++ {
		dy := y
		if int32(u.Uint32(input[22:])) > 0 {
			dy = int(h) - 1 - y
		}
		for x := 0; x < int(w); x++ {
			out[start+dy*row+x*bits/8] |= indices[y*int(w)+x] << uint(8-bits-x*bits%8)
		}
	}
	decoded, err := bmp.Decode(bytes.NewReader(out))
	if err != nil {
		return nil, false, err
	}
	for y := 0; y < int(h); y++ {
		for x := 0; x < int(w); x++ {
			r, g, b, a := source.At(x, y).RGBA()
			rr, gg, bb, aa := decoded.At(x, y).RGBA()
			if r != rr || g != gg || b != bb || a != aa {
				return nil, false, errors.New("limen: BMP pixel verification failed")
			}
		}
	}
	return out, false, nil
}
