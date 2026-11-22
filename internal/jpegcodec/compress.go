package jpegcodec

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
)

type coefficient struct {
	position byte
	size     uint
	bits     uint32
}

type frequencies [8][256]int

type candidate struct {
	tables [8]huffman
	size   int
}

func (layout *scan) compress(input []byte, preserveMetadata bool, qualityCheck func(original, candidate []byte) bool) ([]byte, error) {
	var exact, visual frequencies
	reader := bitReader{data: input, offset: layout.start}
	var visualCounts *frequencies
	if qualityCheck != nil {
		visualCounts = &visual
	}
	if !layout.walk(&reader, &exact, visualCounts, nil, nil, qualityCheck != nil, nil, nil) {
		return nil, invalidJPEG
	}
	if !reader.marker(0xd9) {
		// Sequential JPEGs may contain more scans or table markers after SOS.
		if _, err := jpeg.Decode(bytes.NewReader(input)); err != nil {
			return nil, err
		}
		return input, nil
	}
	if reader.offset != len(input) {
		// A trailing payload may contain a gain map or an additional image.
		return input, nil
	}

	headerSize, metadataSize := 2, 0
	for _, segment := range layout.segments {
		switch {
		case !preserveMetadata && segment.marker == 0xfe:
			metadataSize += len(segment.data)
		case segment.marker != 0xc4:
			headerSize += len(segment.data)
		}
	}
	size := len(input) - metadataSize
	best := input
	if metadataSize != 0 {
		best = make([]byte, 0, size)
		best = append(best, input[:2]...)
		for _, segment := range layout.segments {
			if segment.marker != 0xfe {
				best = append(best, segment.data...)
			}
		}
		best = append(best, input[layout.start:]...)
	}

	exactCandidate := makeCandidate(&exact, headerSize)
	if exactCandidate.size < size {
		if output := layout.encode(input, &exactCandidate, preserveMetadata, false, nil); len(output) < size {
			best = output
		}
	}
	if qualityCheck == nil {
		return best, nil
	}
	var legacy *candidate
	visualHeaderSize := headerSize + 4 + len(visualMarker)
	if visual != exact {
		choice := makeCandidate(&visual, visualHeaderSize)
		legacy = &choice
	}
	for _, segment := range layout.segments {
		if segment.marker == 0xdb {
			visualHeaderSize -= len(segment.data)
		}
	}
	visualHeaderSize += len(appendQuantisation(nil, &layout.quantisation))
	return layout.search(input, best, preserveMetadata, visualHeaderSize, legacy, qualityCheck), nil
}

func makeCandidate(frequency *frequencies, headerSize int) candidate {
	result := candidate{size: headerSize + 4 + 2}
	var bits int
	for id := range frequency {
		if frequency[id] == ([256]int{}) {
			continue
		}
		table := optimalTable(&frequency[id])
		result.tables[id] = table
		result.size += 17 + len(table.values)
		for _, symbol := range table.values {
			bits += frequency[id][symbol] * (int(table.codes[symbol]&31) + int(symbol&15))
		}
	}
	// Stuffed bytes, restart markers and restart padding are checked on output.
	result.size += (bits + 7) / 8
	return result
}

func (layout *scan) encode(input []byte, selected *candidate, preserveMetadata, visual bool, target *quantisation) []byte {
	writer := bitWriter{data: make([]byte, 0, min(len(input), selected.size+selected.size/32))}
	writer.data = append(writer.data, input[:2]...)
	for _, segment := range layout.segments {
		switch {
		case segment.marker == 0xc4 || !preserveMetadata && segment.marker == 0xfe:
			continue
		case target != nil && segment.marker == 0xdb:
			continue
		case segment.marker == 0xda:
			if target != nil {
				writer.data = appendQuantisation(writer.data, target)
			}
			if visual {
				writer.data = appendVisualMarker(writer.data)
			}
			writer.data = append(writer.data, 0xff, 0xc4)
			length := len(writer.data)
			writer.data = append(writer.data, 0, 0)
			for id, table := range &selected.tables {
				if len(table.values) == 0 {
					continue
				}
				writer.data = append(writer.data, byte((id/4)<<4|id%4))
				writer.data = append(writer.data, table.counts[:]...)
				writer.data = append(writer.data, table.values...)
			}
			binary.BigEndian.PutUint16(writer.data[length:], uint16(len(writer.data)-length))
		}
		writer.data = append(writer.data, segment.data...)
	}
	reader := bitReader{data: input, offset: layout.start}
	layout.walk(&reader, nil, nil, &selected.tables, &writer, visual, target, nil)
	writer.marker(0xd9)
	return writer.data
}

func (layout *scan) walk(reader *bitReader, exact, visual *frequencies, tables *[8]huffman, writer *bitWriter, prune bool, target *quantisation, visit blockVisitor) bool {
	var coefficients [63]coefficient
	nextRestart, restart := layout.restart, byte(0xd0)
	for unit := 0; unit < layout.units; unit++ {
		if layout.restart != 0 && unit == nextRestart {
			if !reader.marker(restart) {
				return false
			}
			if writer != nil {
				writer.marker(restart)
			}
			nextRestart += layout.restart
			restart = 0xd0 + (restart+1)&7
		}
		for componentIndex, part := range layout.components {
			dc, ac := layout.tables[part.dc], layout.tables[part.ac]
			for blockIndex := range int(part.width) * int(part.height) {
				symbol, ok := reader.symbol(dc)
				if !ok || symbol > 11 {
					return false
				}
				size := uint(symbol)
				amplitude, ok := reader.read(size)
				if !ok {
					return false
				}
				dcTerm := coefficient{size: size, bits: amplitude}
				if writer == nil {
					if exact != nil {
						exact[part.dc][symbol]++
					}
					if visual != nil {
						visual[part.dc][symbol]++
					}
				} else {
					writer.write(tables[part.dc].codes[symbol], amplitude, size)
				}

				count := 0
				for position := 1; position < 64; {
					symbol, ok := reader.symbol(ac)
					if !ok {
						return false
					}
					size := uint(symbol & 15)
					if exact != nil {
						exact[part.ac][symbol]++
					}
					if symbol == 0 {
						if writer != nil && !prune {
							writer.write(tables[part.ac].codes[0], 0, 0)
						}
						break
					}
					position += int(symbol>>4) + 1
					if position > 64 || size > 10 || size == 0 && symbol != 0xf0 {
						return false
					}
					amplitude, ok := reader.read(size)
					if !ok {
						return false
					}
					if (prune || visit != nil) && size != 0 {
						coefficients[count] = coefficient{byte(position - 1), size, amplitude}
						count++
					}
					if writer != nil && !prune {
						writer.write(tables[part.ac].codes[symbol], amplitude, size)
					}
				}
				if visit != nil {
					visit(unit, componentIndex, blockIndex, dcTerm, coefficients[:count])
				}
				if prune {
					remaining := coefficients[:count]
					if target == nil {
						remaining = pruneBlock(remaining, &layout.quantisation[part.quantisation], part.luma)
					} else {
						remaining = requantise(remaining, &layout.quantisation[part.quantisation], &target[part.quantisation])
					}
					if writer == nil {
						countAC(remaining, &visual[part.ac])
					} else {
						writeAC(remaining, &tables[part.ac], writer)
					}
				}
			}
		}
	}
	return true
}

func countAC(coefficients []coefficient, frequency *[256]int) {
	last := 0
	for _, value := range coefficients {
		run := int(value.position) - last - 1
		frequency[0xf0] += run / 16
		frequency[(run&15)<<4|int(value.size)]++
		last = int(value.position)
	}
	if last != 63 {
		frequency[0]++
	}
}

func writeAC(coefficients []coefficient, table *huffman, writer *bitWriter) {
	last := 0
	for _, value := range coefficients {
		run := int(value.position) - last - 1
		for run >= 16 {
			writer.write(table.codes[0xf0], 0, 0)
			run -= 16
		}
		writer.write(table.codes[run<<4|int(value.size)], value.bits, value.size)
		last = int(value.position)
	}
	if last != 63 {
		writer.write(table.codes[0], 0, 0)
	}
}

func appendVisualMarker(output []byte) []byte {
	output = append(output, 0xff, 0xef)
	output = binary.BigEndian.AppendUint16(output, 2+uint16(len(visualMarker)))
	return append(output, visualMarker...)
}
