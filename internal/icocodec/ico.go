// Package icocodec optimises ICO containers without changing entry selection or DIB rendering.
package icocodec

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image/png"

	"github.com/alphaidea/limen/internal/pngcodec"
)

const maxPayloadBytes = 64 << 20

func IsICO(b []byte) bool { return len(b) >= 4 && bytes.Equal(b[:4], []byte{0, 0, 1, 0}) }

// Compress preserves directory order, dimensions, bit depths and legacy masks.
// Unknown payloads remain byte-identical. Work is bounded to 256 entries and 64 MiB.
func Compress(b []byte, preserve, zopfli bool) ([]byte, error) {
	if !IsICO(b) || len(b) < 6 {
		return nil, errors.New("ico: invalid header")
	}
	n := int(binary.LittleEndian.Uint16(b[4:6]))
	end := 6 + 16*n
	if n == 0 || end > len(b) {
		return nil, errors.New("ico: truncated directory")
	}
	var total uint64
	for i := 0; i < n; i++ {
		e := b[6+16*i:]
		size := uint64(binary.LittleEndian.Uint32(e[8:]))
		offset := uint64(binary.LittleEndian.Uint32(e[12:]))
		total += size
		if size == 0 || offset < uint64(end) || offset+size > uint64(len(b)) {
			return nil, errors.New("ico: invalid payload bounds")
		}
	}
	if preserve || n > 256 || len(b) > maxPayloadBytes || total > maxPayloadBytes {
		return b, nil
	}
	type payloadKey struct {
		directory [8]byte
		digest    [sha256.Size]byte
	}
	type result struct {
		offset int
		data   []byte
	}
	cache := make(map[payloadKey]result, n)
	entries := make([]result, n)
	outputSize, retainedBytes := end, 0
	for i := 0; i < n; i++ {
		e := b[6+16*i : 6+16*(i+1)]
		size := int(binary.LittleEndian.Uint32(e[8:]))
		offset := int(binary.LittleEndian.Uint32(e[12:]))
		payload := b[offset : offset+size]
		key := payloadKey{[8]byte(e[:8]), sha256.Sum256(payload)}
		entry, ok := cache[key]
		if !ok {
			candidate := payload
			if bytes.HasPrefix(payload, []byte("\x89PNG\r\n\x1a\n")) {
				config, err := png.DecodeConfig(bytes.NewReader(payload))
				if err != nil {
					return nil, err
				}
				width, height := int(e[0]), int(e[1])
				if width == 0 {
					width = 256
				}
				if height == 0 {
					height = 256
				}
				if config.Width != width || config.Height != height {
					return nil, errors.New("ico: inconsistent PNG dimensions")
				}
				// Keep colour metadata: ICO consumers must see the same colour interpretation.
				candidate, err = pngcodec.Compress(payload, pngcodec.Options{PreserveMetadata: true, Zopfli: zopfli})
				if err != nil {
					return nil, err
				}
			} else {
				candidate = optimiseDIB(payload, e)
			}
			entry = result{outputSize, candidate}
			outputSize += len(candidate)
			if &candidate[0] != &payload[0] {
				retainedBytes += cap(candidate)
			}
			if outputSize+retainedBytes > maxPayloadBytes {
				return b, nil
			}
			cache[key] = entry
		}
		entries[i] = entry
	}
	// Allocate the final buffer only after all payloads have been validated.
	if outputSize >= len(b) {
		return b, nil
	}
	out := make([]byte, outputSize)
	copy(out, b[:end])
	for _, entry := range cache {
		copy(out[entry.offset:], entry.data)
	}
	for i, entry := range entries {
		e := out[6+16*i:]
		binary.LittleEndian.PutUint32(e[8:], uint32(len(entry.data)))
		binary.LittleEndian.PutUint32(e[12:], uint32(entry.offset))
	}
	return out, nil
}
