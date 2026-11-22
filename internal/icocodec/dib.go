package icocodec

import "encoding/binary"

// optimiseDIB removes unused trailing palette entries without changing indices,
// bit depth, row layout or AND/XOR data. Keeping the palette prefix also retains
// its ordering for palette devices. Unrecognised layouts are returned unchanged.
func optimiseDIB(data, directory []byte) []byte {
	if len(data) < 40 || len(directory) < 8 {
		return data
	}
	order := binary.LittleEndian
	if order.Uint32(data) != 40 || order.Uint16(data[12:]) != 1 || order.Uint32(data[16:]) != 0 {
		return data
	}
	depth := int(order.Uint16(data[14:]))
	if depth != 1 && depth != 4 && depth != 8 {
		return data
	}
	width, height := int(directory[0]), int(directory[1])
	if width == 0 {
		width = 256
	}
	if height == 0 {
		height = 256
	}
	if order.Uint32(data[4:]) != uint32(width) || order.Uint32(data[8:]) != uint32(height*2) {
		return data
	}
	if order.Uint16(directory[4:]) != 1 || order.Uint16(directory[6:]) != uint16(depth) {
		return data
	}
	colours := order.Uint32(data[32:])
	if colours == 0 {
		colours = 1 << depth
	}
	if colours > 1<<depth {
		return data
	}
	// A nonzero importance list can influence palette-device rendering.
	if order.Uint32(data[36:]) != 0 {
		return data
	}
	stride := (width*depth + 31) / 32 * 4
	maskStride := (width + 31) / 32 * 4
	start := 40 + int(colours)*4
	if len(data) != start+(stride+maskStride)*height {
		return data
	}
	// Reserved palette bytes must be conventional, not private alpha data.
	for i := 0; i < int(colours); i++ {
		if data[40+4*i+3] != 0 {
			return data
		}
	}
	maximum := 0
	for y := 0; y < height; y++ {
		row := data[start+y*stride:]
		for x := 0; x < width; x++ {
			index := int(row[x*depth/8]>>uint(8-depth-x*depth%8)) & ((1 << depth) - 1)
			if index >= int(colours) {
				return data
			}
			if index > maximum {
				maximum = index
			}
		}
	}
	// Retain at least two entries for monochrome consumers.
	used := maximum + 1
	if used < 2 {
		used = 2
	}
	if used >= int(colours) {
		return data
	}
	result := make([]byte, 40+used*4+(stride+maskStride)*height)
	copy(result, data[:40+used*4])
	copy(result[40+used*4:], data[start:])
	order.PutUint32(result[32:], uint32(used))
	return result
}
