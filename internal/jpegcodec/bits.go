package jpegcodec

import "encoding/binary"

type bitReader struct {
	data   []byte
	offset int
	bits   uint32
	count  uint
}

func (reader *bitReader) fill(count uint) bool {
	if reader.offset+2 <= len(reader.data) {
		pair := binary.BigEndian.Uint16(reader.data[reader.offset:])
		if pair>>8 != 0xff && byte(pair) != 0xff {
			reader.bits = reader.bits<<16 | uint32(pair)
			reader.count += 16
			reader.offset += 2
			return true
		}
	}
	for reader.count < count && reader.offset < len(reader.data) {
		value := reader.data[reader.offset]
		if value == 0xff {
			if reader.offset+1 == len(reader.data) || reader.data[reader.offset+1] != 0 {
				break
			}
			reader.offset++
		}
		reader.offset++
		reader.bits = reader.bits<<8 | uint32(value)
		reader.count += 8
	}
	return reader.count >= count
}

func (reader *bitReader) read(count uint) (uint32, bool) {
	if reader.count < count && !reader.fill(count) {
		return 0, false
	}
	reader.count -= count
	return (reader.bits >> reader.count) & (1<<count - 1), true
}

func (reader *bitReader) symbol(table *huffman) (byte, bool) {
	if reader.count >= 8 || reader.fill(8) {
		entry := table.lookup[byte(reader.bits>>(reader.count-8))]
		if entry != 0 {
			reader.count -= uint(entry & 31)
			return byte(entry >> 5), true
		}
	}
	code := 0
	for length := 1; length <= 16; length++ {
		value, ok := reader.read(1)
		if !ok {
			break
		}
		code = code<<1 | int(value)
		if code <= table.limit[length] {
			return table.values[code+table.base[length]], true
		}
	}
	return 0, false
}

func (reader *bitReader) padding() bool {
	padding := uint32(1<<reader.count) - 1
	return reader.count <= 7 && reader.bits&padding == padding
}

func (reader *bitReader) marker(marker byte) bool {
	if !reader.padding() {
		return false
	}
	reader.count = 0
	offset := reader.offset
	for offset < len(reader.data) && reader.data[offset] == 0xff {
		offset++
	}
	if offset == reader.offset || offset == len(reader.data) || reader.data[offset] != marker {
		return false
	}
	reader.offset = offset + 1
	return true
}

type bitWriter struct {
	data  []byte
	bits  uint64
	count uint
}

func (writer *bitWriter) write(code, amplitude uint32, size uint) {
	writer.bits = writer.bits<<(uint(code&31)+size) | uint64(code>>5)<<size | uint64(amplitude)
	writer.count += uint(code&31) + size
	for writer.count >= 8 {
		writer.count -= 8
		value := byte(writer.bits >> writer.count)
		writer.data = append(writer.data, value)
		if value == 0xff {
			writer.data = append(writer.data, 0)
		}
	}
}

func (writer *bitWriter) align() {
	if writer.count != 0 {
		padding := 8 - writer.count
		writer.write(0, 1<<padding-1, padding)
	}
}

func (writer *bitWriter) marker(marker byte) {
	writer.align()
	writer.data = append(writer.data, 0xff, marker)
}
