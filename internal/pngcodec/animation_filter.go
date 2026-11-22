package pngcodec

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"sync"
)

var defaultWriters = sync.Pool{New: func() any { writer, _ := zlib.NewWriterLevel(nil, zlib.DefaultCompression); return writer }}

var bestWriters = sync.Pool{New: func() any {
	writer, _ := zlib.NewWriterLevel(nil, zlib.BestCompression)
	return writer
}}

// Screen the original predictors and a minimum-residual adaptive predictor.
// Reuse one DEFLATE workspace across frames and calls instead of allocating a
// compressor for every filter. PNG decoding has validated all filter bytes.
func optimiseAnimationFilters(data []byte) []byte {
	if data[28] != 0 {
		return data
	}
	depth, kind := int(data[24]), int(data[25])
	channels := [...]int{1, 0, 3, 1, 2, 0, 4}[kind]
	width, height := int(binary.BigEndian.Uint32(data[16:])), int(binary.BigEndian.Uint32(data[20:]))
	stride := (width*depth*channels + 7) / 8
	step := max(1, depth*channels/8)
	if height > (32<<20)/(stride+1) {
		return data
	}
	first, last, compressed := imageData(data)
	raw := inflateImageData(compressed, height*(stride+1))
	if len(raw) != height*(stride+1) {
		return data
	}
	writer := defaultWriters.Get().(*zlib.Writer)
	defer defaultWriters.Put(writer)
	var output bytes.Buffer
	best := data
	consider := func(rows []byte) {
		output.Reset()
		writer.Reset(&output)
		writer.Write(rows)
		writer.Close()
		if first+output.Len()+12+len(data)-last < len(best) {
			best = replaceImageData(data, first, last, output.Bytes())
		}
	}
	consider(raw)
	previous := make([]byte, stride)
	for offset := 0; offset < len(raw); offset += stride + 1 {
		filter := raw[offset]
		row := raw[offset+1 : offset+stride+1]
		for x := range row {
			var left, upperLeft byte
			if x >= step {
				left = row[x-step]
				upperLeft = previous[x-step]
			}
			row[x] += predictor(filter, left, previous[x], upperLeft)
		}
		previous = row
	}
	filtered := make([]byte, len(raw))
	scratch := make([]byte, stride+1)
	zero := make([]byte, stride)
	previous = zero
	for offset := 0; offset < len(raw); offset += stride + 1 {
		row := raw[offset+1 : offset+stride+1]
		bestRow := filtered[offset : offset+stride+1]
		minimum := int(^uint(0) >> 1)
		for filter := byte(0); filter <= 4; filter++ {
			filterRow(scratch, filter, row, previous, step)
			score := 0
			for _, value := range scratch[1:] {
				score += min(int(value), 256-int(value))
			}
			if score < minimum {
				minimum = score
				copy(bestRow, scratch)
			}
		}
		previous = row
	}
	consider(filtered)
	for _, filter := range []byte{0, 1, 2, 4} {
		previous = zero
		for offset := 0; offset < len(raw); offset += stride + 1 {
			row := raw[offset+1 : offset+stride+1]
			filterRow(filtered[offset:offset+stride+1], filter, row, previous, step)
			previous = row
		}
		consider(filtered)
	}
	// Refine only the winning filter, reusing the high-effort compressor.
	// Already optimised streams showed no gain from this extra pass in the
	// animation corpus; avoid repeating expensive work when screening lost.
	if bytes.Equal(best, data) {
		return best
	}
	bestFirst, bestLast, bestStream := imageData(best)
	bestRows := inflateImageData(bestStream, height*(stride+1))
	finalWriter := bestWriters.Get().(*zlib.Writer)
	output.Reset()
	finalWriter.Reset(&output)
	finalWriter.Write(bestRows)
	finalWriter.Close()
	bestWriters.Put(finalWriter)
	if bestFirst+output.Len()+12+len(best)-bestLast < len(best) {
		best = replaceImageData(best, bestFirst, bestLast, output.Bytes())
	}
	return best
}
