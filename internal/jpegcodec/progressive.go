package jpegcodec

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
	"math/bits"
)

type blockVisitor func(unit, component, block int, dc coefficient, ac []coefficient)

type progressivePlane struct {
	blocks                [][64]int16
	stride, columns, rows int
}

func progressiveCandidate(input []byte, width, height int) []byte {
	layout, err := inspect(input, width, height)
	if err != nil || layout == nil || layout.progressive || layout.restart != 0 {
		return input
	}
	planes, unitsPerRow := layout.makePlanes(width, height)
	var dcFrequency frequencies
	var dcWriter bitWriter
	dcTables := [8]huffman{}
	reader := bitReader{data: input, offset: layout.start}
	if !layout.walk(&reader, nil, nil, nil, nil, false, nil, func(unit, component, block int, dc coefficient, ac []coefficient) {
		part, plane := layout.components[component], &planes[component]
		h, v := int(part.width), int(part.height)
		x := unit%unitsPerRow*h + block%h
		y := unit/unitsPerRow*v + block/h
		current := &plane.blocks[y*plane.stride+x]
		current[0] = int16(amplitude(dc))
		dcFrequency[part.dc][dc.size]++
		for _, term := range ac {
			current[term.position] = int16(amplitude(term))
		}
	}) || !reader.marker(0xd9) || reader.offset != len(input) {
		return input
	}

	dcWriter.data = append(make([]byte, 0, len(input)), input[:2]...)
	for _, segment := range layout.segments {
		switch segment.marker {
		case 0xc4, 0xda:
			continue
		case 0xc0:
			dcWriter.data = append(dcWriter.data, 0xff, 0xc2)
			dcWriter.data = append(dcWriter.data, segmentPayload(segment.data)...)
		default:
			dcWriter.data = append(dcWriter.data, segment.data...)
		}
	}
	for _, part := range layout.components {
		if dcTables[part.dc].values == nil {
			dcTables[part.dc] = optimalTable(&dcFrequency[part.dc])
			dcWriter.data = appendHuffman(dcWriter.data, part.dc, &dcTables[part.dc])
		}
	}
	dcWriter.data = append(dcWriter.data, 0xff, 0xda, 0, byte(6+2*len(planes)), byte(len(planes)))
	for _, part := range layout.components {
		dcWriter.data = append(dcWriter.data, part.id, part.dc<<4)
	}
	dcWriter.data = append(dcWriter.data, 0, 0, 0)
	for unit := range layout.units {
		for i, part := range layout.components {
			plane := &planes[i]
			h, v := int(part.width), int(part.height)
			base := unit/unitsPerRow*v*plane.stride + unit%unitsPerRow*h
			for block := range h * v {
				value := int(plane.blocks[base+block/h*plane.stride+block%h][0])
				size, valueBits := encodeAmplitude(value)
				dcWriter.write(dcTables[part.dc].codes[size], valueBits, size)
			}
		}
	}
	dcWriter.align()

	var chroma bitWriter
	for i := 1; i < len(planes); i++ {
		writeBand(&chroma, planes[i], layout.components[i].id, 1, 63, 0, 0)
	}
	best := input
	luma, id := planes[0], layout.components[0].id
	plans := [...]struct{ split, approximation byte }{{5, 0}, {0, 0}, {10, 3}}
	for _, plan := range plans {
		writer := bitWriter{data: dcWriter.data}
		if plan.split == 0 {
			writeBand(&writer, luma, id, 1, 63, 0, plan.approximation)
		} else {
			writeBand(&writer, luma, id, 1, plan.split, 0, plan.approximation)
			writeBand(&writer, luma, id, plan.split+1, 63, 0, plan.approximation)
		}
		writer.data = append(writer.data, chroma.data...)
		for shift := plan.approximation; shift != 0; shift-- {
			writeBand(&writer, luma, id, 1, 63, shift, shift-1)
		}
		writer.marker(0xd9)
		if len(writer.data) < len(best) {
			best = append([]byte(nil), writer.data...)
		}
	}
	if len(best) < len(input) {
		original, originalErr := jpeg.Decode(bytes.NewReader(input))
		encoded, encodedErr := jpeg.Decode(bytes.NewReader(best))
		if originalErr != nil || encodedErr != nil || !equalJPEG(original, encoded) {
			return input
		}
	}
	return best
}

func segmentPayload(data []byte) []byte {
	index := 1
	for data[index] == 0xff {
		index++
	}
	return data[index+1:]
}

func appendHuffman(output []byte, id byte, table *huffman) []byte {
	output = append(output, 0xff, 0xc4)
	output = binary.BigEndian.AppendUint16(output, uint16(19+len(table.values)))
	output = append(output, id)
	output = append(output, table.counts[:]...)
	return append(output, table.values...)
}

func writeBand(writer *bitWriter, plane progressivePlane, id, first, last, high, low byte) {
	walk := func(emit func(int, uint32, uint)) {
		if high == 0 {
			plane.walkBand(first, last, uint(low), emit)
		} else {
			plane.walkRefinement(uint(low), emit)
		}
	}
	var counts [256]int
	walk(func(symbol int, _ uint32, _ uint) {
		if symbol >= 0 {
			counts[symbol]++
		}
	})
	table := optimalTable(&counts)
	writer.data = appendHuffman(writer.data, 0x10, &table)
	writer.data = append(writer.data, 0xff, 0xda, 0, 8, 1, id, 0, first, last, high<<4|low)
	walk(func(symbol int, value uint32, size uint) {
		var code uint32
		if symbol >= 0 {
			code = table.codes[symbol]
		}
		writer.write(code, value, size)
	})
	writer.align()
}

func (plane progressivePlane) walkBand(first, last byte, shift uint, emit func(int, uint32, uint)) {
	eob := 0
	flush := func() {
		if eob != 0 {
			size := uint(bits.Len(uint(eob)) - 1)
			emit(int(size<<4), uint32(eob)-(1<<size), size)
			eob = 0
		}
	}
	for y := range plane.rows {
		for x := range plane.columns {
			block := &plane.blocks[y*plane.stride+x]
			run := 0
			for position := first; position <= last; position++ {
				value := coefficientMagnitude(block[position]) >> shift
				if block[position] < 0 {
					value = -value
				}
				if value == 0 {
					run++
					continue
				}
				flush()
				for run >= 16 {
					emit(0xf0, 0, 0)
					run -= 16
				}
				size, valueBits := encodeAmplitude(value)
				emit(run<<4|int(size), valueBits, size)
				run = 0
			}
			if run != 0 {
				eob++
				if eob == 32767 {
					flush()
				}
			}
		}
	}
	flush()
}

func amplitude(term coefficient) int {
	value := int(term.bits)
	if term.size != 0 && value < 1<<(term.size-1) {
		value -= 1<<term.size - 1
	}
	return value
}

func encodeAmplitude(value int) (uint, uint32) {
	if value < 0 {
		size := uint(bits.Len(uint(-value)))
		return size, uint32(value + (1<<size - 1))
	}
	return uint(bits.Len(uint(value))), uint32(value)
}

func (layout *scan) makePlanes(width, height int) ([]progressivePlane, int) {
	maxWidth, maxHeight := 1, 1
	for _, part := range layout.components {
		maxWidth = max(maxWidth, int(part.width))
		maxHeight = max(maxHeight, int(part.height))
	}
	unitsPerRow := (width + 8*maxWidth - 1) / (8 * maxWidth)
	unitRows := (height + 8*maxHeight - 1) / (8 * maxHeight)
	planes := make([]progressivePlane, len(layout.components))
	for i, part := range layout.components {
		h, v := int(part.width), int(part.height)
		stride := unitsPerRow * h
		planes[i] = progressivePlane{
			blocks: make([][64]int16, stride*unitRows*v), stride: stride,
			columns: (width*h + 8*maxWidth - 1) / (8 * maxWidth),
			rows:    (height*v + 8*maxHeight - 1) / (8 * maxHeight),
		}
	}
	return planes, unitsPerRow
}
