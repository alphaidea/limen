package pngcodec

import (
	"encoding/binary"
	"hash/crc32"
)

func appendAnimationChunk(result []byte, kind string, payload []byte) []byte {
	result = binary.BigEndian.AppendUint32(result, uint32(len(payload)))
	start := len(result)
	result = append(result, kind...)
	result = append(result, payload...)
	return binary.BigEndian.AppendUint32(result, crc32.ChecksumIEEE(result[start:]))
}
