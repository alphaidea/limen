package jpegcodec

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"slices"
)

func (layout *scan) compressProgressive(input []byte, width, height int, preserveMetadata bool, qualityCheck func(original, candidate []byte) bool) ([]byte, error) {
	original, err := jpeg.Decode(bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	if qualityCheck == nil {
		return input, nil
	}
	sequential := layout.sequential(input, width, height)
	if sequential == nil {
		return input, nil
	}
	decoded, err := jpeg.Decode(bytes.NewReader(sequential))
	if err != nil || !equalJPEG(original, decoded) {
		return input, nil
	}
	converted, err := inspect(sequential, width, height)
	if err != nil || converted == nil {
		return input, nil
	}
	output, err := converted.compress(sequential, preserveMetadata, func(_, candidate []byte) bool {
		return qualityCheck(input, candidate)
	})
	if err != nil {
		return input, nil
	}
	if !bytes.Contains(output, []byte(visualMarker)) {
		output = progressiveCandidate(output, width, height)
	}
	if len(output) >= len(input) {
		return input, nil
	}
	return output, nil
}

func equalJPEG(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	bounds := a.Bounds()
	switch a := a.(type) {
	case *image.Gray:
		other, ok := b.(*image.Gray)
		if !ok {
			return false
		}
		for y := range bounds.Dy() {
			if !bytes.Equal(a.Pix[y*a.Stride:][:bounds.Dx()], other.Pix[y*other.Stride:][:bounds.Dx()]) {
				return false
			}
		}
		return true
	case *image.YCbCr:
		other, ok := b.(*image.YCbCr)
		if !ok || a.SubsampleRatio != other.SubsampleRatio {
			return false
		}
		for y := range bounds.Dy() {
			first, second := a.YOffset(bounds.Min.X, y), other.YOffset(bounds.Min.X, y)
			if !bytes.Equal(a.Y[first:][:bounds.Dx()], other.Y[second:][:bounds.Dx()]) {
				return false
			}
		}
		for y := range bounds.Dy() {
			first, second := a.COffset(bounds.Min.X, y), other.COffset(bounds.Min.X, y)
			if y != 0 && first == a.COffset(bounds.Min.X, y-1) {
				continue
			}
			width := a.COffset(bounds.Max.X-1, y) - first + 1
			if !bytes.Equal(a.Cb[first:][:width], other.Cb[second:][:width]) ||
				!bytes.Equal(a.Cr[first:][:width], other.Cr[second:][:width]) {
				return false
			}
		}
		return true
	}
	return false
}

func (layout *scan) sequential(input []byte, width, height int) []byte {
	if layout.restart != 0 {
		return nil
	}
	planes, unitsPerRow := layout.makePlanes(width, height)
	decoder := progression{layout: layout, planes: planes, unitsPerRow: unitsPerRow}
	offset := layout.start - len(layout.segments[len(layout.segments)-1].data)
	for scanCount := 0; scanCount <= 32; {
		if offset >= len(input) || input[offset] != 0xff {
			return nil
		}
		for offset < len(input) && input[offset] == 0xff {
			offset++
		}
		if offset == len(input) {
			return nil
		}
		marker := input[offset]
		offset++
		if marker == 0xd9 && offset == len(input) {
			return layout.encodeSequential(input, planes, unitsPerRow)
		}
		if len(input)-offset < 2 {
			return nil
		}
		length := int(binary.BigEndian.Uint16(input[offset:]))
		if length < 2 || length > len(input)-offset {
			return nil
		}
		payload := input[offset+2 : offset+length]
		offset += length
		switch marker {
		case 0xc4:
			if !layout.readTables(payload) {
				return nil
			}
		case 0xda:
			scanCount++
			if scanCount > 32 {
				return nil
			}
			reader := bitReader{data: input, offset: offset}
			if !decoder.scan(&reader, payload) || !reader.padding() {
				return nil
			}
			offset = reader.offset
		default:
			return nil
		}
	}
	return nil
}

type progression struct {
	layout      *scan
	planes      []progressivePlane
	unitsPerRow int
	eob         int
}

func (p *progression) scan(reader *bitReader, header []byte) bool {
	if len(header) < 6 || len(header) != 4+2*int(header[0]) || header[0] == 0 || header[0] > 3 {
		return false
	}
	count := int(header[0])
	first, last, approximation := header[1+2*count], header[2+2*count], header[3+2*count]
	high, low := approximation>>4, approximation&15
	if first > last || last > 63 || first == 0 && last != 0 || first != 0 && count != 1 ||
		high != 0 && high != low+1 || low > 10 {
		return false
	}
	var selectors [3]int
	var tables [3]*huffman
	var used [3]bool
	for i := range count {
		id, selector := header[1+2*i], header[2+2*i]
		index := slices.IndexFunc(p.layout.components, func(part component) bool { return part.id == id })
		if index < 0 || used[index] || selector>>4 > 3 || selector&15 > 3 {
			return false
		}
		used[index] = true
		selectors[i] = index
		if first == 0 {
			tables[i] = p.layout.tables[selector>>4]
		} else {
			tables[i] = p.layout.tables[4+(selector&15)]
		}
		if tables[i] == nil && (first != 0 || high == 0) {
			return false
		}
	}
	p.eob = 0
	if first != 0 {
		plane := p.planes[selectors[0]]
		for y := range plane.rows {
			for x := range plane.columns {
				if !p.ac(reader, &plane.blocks[y*plane.stride+x], tables[0], int(first), int(last), high, low) {
					return false
				}
			}
		}
		return p.eob == 0
	}
	var previous [3]int
	readDC := func(i int, block *[64]int16) bool {
		if high != 0 {
			bit, ok := reader.read(1)
			block[0] |= int16(bit) << low
			return ok
		}
		symbol, ok := reader.symbol(tables[i])
		if !ok || symbol > 11 {
			return false
		}
		value, ok := reader.read(uint(symbol))
		previous[i] += amplitude(coefficient{size: uint(symbol), bits: value})
		dc := previous[i] << low
		if !ok || dc < -2048 || dc > 2047 {
			return false
		}
		block[0] = int16(dc)
		return true
	}
	if count == 1 {
		plane := p.planes[selectors[0]]
		for y := range plane.rows {
			for x := range plane.columns {
				if !readDC(0, &plane.blocks[y*plane.stride+x]) {
					return false
				}
			}
		}
		return true
	}
	for unit := range p.layout.unitCount(p.planes) {
		for i, index := range selectors[:count] {
			part, plane := p.layout.components[index], &p.planes[index]
			h, v := int(part.width), int(part.height)
			base := unit/p.unitsPerRow*v*plane.stride + unit%p.unitsPerRow*h
			for block := range h * v {
				if !readDC(i, &plane.blocks[base+block/h*plane.stride+block%h]) {
					return false
				}
			}
		}
	}
	return true
}

func (p *progression) ac(reader *bitReader, block *[64]int16, table *huffman, first, last int, high, low byte) bool {
	position := first
	delta := int16(1) << low
	if p.eob == 0 {
		for position <= last {
			symbol, ok := reader.symbol(table)
			if !ok {
				return false
			}
			run, size := int(symbol>>4), uint(symbol&15)
			if size == 0 && run < 15 {
				extra, ok := reader.read(uint(run))
				if !ok {
					return false
				}
				p.eob = 1<<run | int(extra)
				break
			}
			if size > 10 || high != 0 && size > 1 {
				return false
			}
			value, ok := reader.read(size)
			if !ok {
				return false
			}
			if high != 0 {
				position, ok = refineTerms(reader, block, position, last, run, delta)
			} else {
				position += run
			}
			if !ok || position > last {
				return false
			}
			if size != 0 {
				block[position] = int16(amplitude(coefficient{size: size, bits: value}) << low)
			}
			position++
		}
	}
	if p.eob != 0 {
		p.eob--
		if high != 0 {
			_, ok := refineTerms(reader, block, position, last, -1, delta)
			return ok
		}
	}
	return true
}

func refineTerms(reader *bitReader, block *[64]int16, position, last, zeros int, delta int16) (int, bool) {
	for ; position <= last; position++ {
		value := block[position]
		if value == 0 {
			if zeros == 0 {
				break
			}
			zeros--
			continue
		}
		bit, ok := reader.read(1)
		if !ok {
			return position, false
		}
		if bit != 0 {
			if value < 0 {
				block[position] -= delta
			} else {
				block[position] += delta
			}
		}
	}
	return position, true
}

func (layout *scan) eachBlock(planes []progressivePlane, unitsPerRow int, visit func(index int, block *[64]int16) bool) bool {
	for unit := range layout.unitCount(planes) {
		for i, part := range layout.components {
			plane := planes[i]
			h, v := int(part.width), int(part.height)
			base := unit/unitsPerRow*v*plane.stride + unit%unitsPerRow*h
			for block := range h * v {
				if !visit(i, &plane.blocks[base+block/h*plane.stride+block%h]) {
					return false
				}
			}
		}
	}
	return true
}

func (layout *scan) unitCount(planes []progressivePlane) int {
	first := layout.components[0]
	return len(planes[0].blocks) / (int(first.width) * int(first.height))
}

func (layout *scan) encodeSequential(input []byte, planes []progressivePlane, unitsPerRow int) []byte {
	visit := func(emit func(table, symbol byte, value uint32, size uint)) bool {
		var previous [3]int
		return layout.eachBlock(planes, unitsPerRow, func(index int, block *[64]int16) bool {
			size, value := encodeAmplitude(int(block[0]) - previous[index])
			if size > 11 {
				return false
			}
			previous[index] = int(block[0])
			table := byte(min(1, index))
			emit(table, byte(size), value, size)
			run := 0
			for _, term := range block[1:] {
				if term == 0 {
					run++
					continue
				}
				for run >= 16 {
					emit(4+table, 0xf0, 0, 0)
					run -= 16
				}
				size, value = encodeAmplitude(int(term))
				if size > 10 {
					return false
				}
				emit(4+table, byte(run<<4)|byte(size), value, size)
				run = 0
			}
			if run != 0 {
				emit(4+table, 0, 0, 0)
			}
			return true
		})
	}
	var frequency frequencies
	if !visit(func(table, symbol byte, _ uint32, _ uint) { frequency[table][symbol]++ }) {
		return nil
	}
	encoded := makeCandidate(&frequency, 0)
	writer := bitWriter{data: append(make([]byte, 0, len(input)), input[:2]...)}
	for _, part := range layout.segments {
		switch part.marker {
		case 0xc2:
			writer.data = append(writer.data, 0xff, 0xc0)
			writer.data = append(writer.data, segmentPayload(part.data)...)
		case 0xc4, 0xda:
		default:
			writer.data = append(writer.data, part.data...)
		}
	}
	for id, table := range &encoded.tables {
		if len(table.values) != 0 {
			writer.data = appendHuffman(writer.data, byte(id/4*16+id%4), &table)
		}
	}
	writer.data = append(writer.data, 0xff, 0xda, 0, byte(6+2*len(planes)), byte(len(planes)))
	for i, part := range layout.components {
		writer.data = append(writer.data, part.id, byte(min(1, i)*0x11))
	}
	writer.data = append(writer.data, 0, 63, 0)
	visit(func(table, symbol byte, value uint32, size uint) {
		writer.write(encoded.tables[table].codes[symbol], value, size)
	})
	writer.marker(0xd9)
	return writer.data
}
