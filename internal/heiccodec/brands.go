package heiccodec

import "encoding/binary"

// brands reads only the bounded file-type box, never image payloads.
func brands(input []byte) []byte {
	if len(input) < 16 || string(input[4:8]) != "ftyp" {
		return nil
	}
	size, start := uint64(binary.BigEndian.Uint32(input)), 8
	if size == 1 {
		if len(input) < 24 {
			return nil
		}
		size, start = binary.BigEndian.Uint64(input[8:16]), 16
	}
	if size < uint64(start+8) || size > uint64(len(input)) || (size-uint64(start))%4 != 0 {
		return nil
	}
	return input[start:int(size)]
}

// IsHEIF recognises generic HEIF containers without claiming a codec.
func IsHEIF(input []byte) bool {
	b := brands(input)
	generic := false
	for i := 0; i+4 <= len(b); i += 4 {
		if i == 4 {
			continue
		}
		switch string(b[i : i+4]) {
		case "avif", "avis":
			return false
		case "mif1", "msf1":
			generic = true
		}
	}
	return generic
}
