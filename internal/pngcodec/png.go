package pngcodec

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"io"
	"math"
)

const pngSignature = "\x89PNG\r\n\x1a\n"

type pngLayout struct {
	depth           byte
	beforePixels    []byte
	afterPixels     []byte
	fallback        []byte
	preserve        bool
	animated        bool
	sdr             bool
	displayMetadata bool
}

func inspectPNG(data []byte, options Options) (pngLayout, error) {
	layout := pngLayout{sdr: true, fallback: data}
	if !bytes.HasPrefix(data, []byte(pngSignature)) {
		return layout, errors.New("limen: unsupported image format")
	}
	var header, pixels, pastPixels, profile bool
	var stripped []byte
	segmentStart := 0
	for rest := data[len(pngSignature):]; len(rest) >= 12; {
		size := uint64(binary.BigEndian.Uint32(rest))
		if size > 0x7fffffff || size > uint64(len(rest)-12) {
			break
		}
		chunk := rest[:int(size)+12]
		rest = rest[len(chunk):]
		kind := string(chunk[4:8])
		if crc32.ChecksumIEEE(chunk[4:len(chunk)-4]) != binary.BigEndian.Uint32(chunk[len(chunk)-4:]) {
			return layout, errors.New("limen: invalid PNG chunk checksum")
		}
		if !header && kind != "IHDR" {
			break
		}
		if pixels && kind != "IDAT" {
			pastPixels = true
		}
		metadata := false
		switch kind {
		case "IHDR":
			if header || size != 13 {
				return layout, errors.New("limen: invalid PNG header")
			}
			header = true
			layout.depth = chunk[16]
		case "IDAT":
			if pastPixels {
				return layout, errors.New("limen: non-consecutive PNG image data")
			}
			pixels = true
		case "IEND":
			if size == 0 && pixels && len(rest) == 0 {
				if stripped != nil {
					layout.fallback = append(stripped, data[segmentStart:]...)
				}
				return layout, nil
			}
			return layout, errors.New("limen: invalid PNG ending")
		case "PLTE", "tRNS":
		case "acTL", "fcTL", "fdAT":
			layout.preserve = true
			layout.animated = true
		case "pHYs", "eXIf", "tEXt", "zTXt", "iTXt":
			metadata = true
		case "sRGB", "gAMA", "cHRM", "iCCP":
			if pixels {
				return layout, errors.New("limen: PNG colour metadata after image data")
			}
			metadata = true
			switch kind {
			case "iCCP":
				layout.sdr = layout.sdr && (options.NearLossless || options.QualityCheck != nil) && !profile && isSRGBProfile(chunk[8:len(chunk)-4])
				profile = true
				// A greyscale ICC profile cannot describe an RGB palette.
				if data[25] == 0 || data[25] == 4 {
					layout.preserve = true
				}
			case "gAMA", "cHRM":
				layout.sdr = false
			}
		default:
			if chunk[4]&32 == 0 {
				return layout, errors.New("limen: unsupported critical PNG chunk")
			}
			metadata = true
			layout.sdr = false
			// Unknown chunks may depend on the original image encoding.
			layout.preserve = true
		}
		if metadata {
			descriptive := kind == "tEXt" || kind == "iTXt" || kind == "zTXt"
			if !descriptive {
				layout.displayMetadata = true
			}
			if descriptive && !options.PreserveMetadata {
				chunkEnd := len(data) - len(rest)
				stripped = append(stripped, data[segmentStart:chunkEnd-len(chunk)]...)
				segmentStart = chunkEnd
			} else if pixels {
				layout.afterPixels = append(layout.afterPixels, chunk...)
			} else {
				layout.beforePixels = append(layout.beforePixels, chunk...)
			}
		}
	}
	return layout, errors.New("limen: incomplete PNG")
}

func (layout pngLayout) withMetadata(data []byte) []byte {
	if len(layout.beforePixels)+len(layout.afterPixels) == 0 {
		return data
	}
	const headerEnd = 8 + 25
	imageEnd := len(data) - 12
	result := make([]byte, 0, len(data)+len(layout.beforePixels)+len(layout.afterPixels))
	result = append(result, data[:headerEnd]...)
	result = append(result, layout.beforePixels...)
	result = append(result, data[headerEnd:imageEnd]...)
	result = append(result, layout.afterPixels...)
	return append(result, data[imageEnd:]...)
}

func appendIDAT(output *bytes.Buffer, data []byte) {
	var word [4]byte
	binary.BigEndian.PutUint32(word[:], uint32(len(data)))
	output.Write(word[:])
	start := output.Len()
	output.WriteString("IDAT")
	output.Write(data)
	binary.BigEndian.PutUint32(word[:], crc32.ChecksumIEEE(output.Bytes()[start:]))
	output.Write(word[:])
}

func imageData(data []byte) (first, last int, compressed *bytes.Buffer) {
	compressed = new(bytes.Buffer)
	for offset := 8; offset < len(data); {
		size := int(binary.BigEndian.Uint32(data[offset:]))
		end := offset + size + 12
		if string(data[offset+4:offset+8]) == "IDAT" {
			if first == 0 {
				first = offset
			}
			last = end
			compressed.Write(data[offset+8 : end-4])
		}
		offset = end
	}
	return first, last, compressed
}

func replaceImageData(data []byte, first, last int, compressed []byte) []byte {
	var output bytes.Buffer
	output.Grow(first + len(compressed) + 12 + len(data) - last)
	output.Write(data[:first])
	appendIDAT(&output, compressed)
	output.Write(data[last:])
	return output.Bytes()
}

// Go's encoder uses adaptive filters for truecolour and None for indexed
// images. Compare fixed and entropy-selected predictors at the default level.
func optimiseFilters(data []byte) []byte {
	return optimiseFiltersLevel(data, zlib.DefaultCompression)
}

func optimiseFiltersLevel(data []byte, level int) []byte {
	header := data[16:29]
	if header[12] != 0 {
		return data
	}
	depth := int(header[8])
	channels := [...]int{1, 0, 3, 1, 2, 0, 4}[header[9]]
	width, height := int(binary.BigEndian.Uint32(header)), int(binary.BigEndian.Uint32(header[4:]))
	stride := (width*depth*channels + 7) / 8
	step := max(1, depth*channels/8)
	if height*(stride+1) > 32<<20 {
		return data
	}
	// High-effort refiltering had no benefit on the large photographic sample.
	if level == zlib.BestCompression && height*(stride+1) > 256<<10 {
		return data
	}
	first, last, compressed := imageData(data)
	reader, err := zlib.NewReader(compressed)
	if err != nil {
		return data
	}
	defer reader.Close()
	rows := make([]byte, height*(stride+1))
	if _, err := io.ReadFull(reader, rows); err != nil {
		return data
	}

	previous := make([]byte, stride)
	for offset := 0; offset < len(rows); offset += stride + 1 {
		filter := rows[offset]
		row := rows[offset+1 : offset+1+stride]
		for x := range row {
			var left, upperLeft byte
			if x >= step {
				left, upperLeft = row[x-step], previous[x-step]
			}
			row[x] += predictor(filter, left, previous[x], upperLeft)
		}
		previous = row
	}

	best := data
	var output bytes.Buffer
	filtered := make([]byte, stride+1)
	zero := make([]byte, stride)
	pool := &defaultWriters
	if level == zlib.BestCompression {
		pool = &bestWriters
	}
	writer := pool.Get().(*zlib.Writer)
	defer pool.Put(writer)
	strategies := []int{0, 1, 2, 3, 4}
	var entropy []float64
	var scratch []byte
	if stride <= 1<<18 {
		strategies = append(strategies, -1)
		entropy = entropyCosts(stride + 1)
		scratch = make([]byte, stride+1)
	}
	for _, strategy := range strategies {
		output.Reset()
		writer.Reset(&output)
		previous := zero
		for offset := 0; offset < len(rows); offset += stride + 1 {
			current := rows[offset+1 : offset+1+stride]
			if strategy >= 0 {
				filterRow(filtered, byte(strategy), current, previous, step)
			} else {
				filtered, scratch = selectEntropyFilter(filtered, scratch, current, previous, step, entropy)
			}
			writer.Write(filtered)
			previous = current
		}
		writer.Close()
		if first+output.Len()+12+len(data)-last < len(best) {
			best = replaceImageData(data, first, last, output.Bytes())
		}
	}
	return best
}

// Spend the highest DEFLATE effort once, on the smallest screened candidate.
func recompressImageData(data []byte) []byte {
	first, last, compressed := imageData(data)
	reader, err := zlib.NewReader(compressed)
	if err != nil {
		return data
	}
	defer reader.Close()
	var output bytes.Buffer
	writer := bestWriters.Get().(*zlib.Writer)
	defer bestWriters.Put(writer)
	writer.Reset(&output)
	_, copyErr := io.Copy(writer, reader)
	if writer.Close() != nil || copyErr != nil || output.Len()+12 >= last-first {
		return data
	}
	return replaceImageData(data, first, last, output.Bytes())
}

func filterRow(output []byte, filter byte, current, previous []byte, step int) {
	output[0] = filter
	switch filter {
	case 0:
		copy(output[1:], current)
		return
	case 1:
		prefix := min(step, len(current))
		copy(output[1:], current[:prefix])
		for x := prefix; x < len(current); x++ {
			output[x+1] = current[x] - current[x-step]
		}
		return
	case 2:
		for x, value := range current {
			output[x+1] = value - previous[x]
		}
		return
	}
	for x, value := range current {
		var left, upperLeft byte
		if x >= step {
			left, upperLeft = current[x-step], previous[x-step]
		}
		output[x+1] = value - predictor(filter, left, previous[x], upperLeft)
	}
}

func entropyCosts(length int) []float64 {
	costs := make([]float64, length+1)
	for count := 2; count <= length; count++ {
		n := float64(count)
		costs[count] = n * math.Log2(n)
	}
	return costs
}

func selectEntropyFilter(best, scratch, current, previous []byte, step int, costs []float64) ([]byte, []byte) {
	bestScore := -1.0
	for filter := byte(0); filter <= 4; filter++ {
		filterRow(scratch, filter, current, previous, step)
		var histogram [256]int
		for _, value := range scratch {
			histogram[value]++
		}
		// Every row has the same length, so maximising Σ count·log₂(count)
		// minimises Shannon entropy.
		var score float64
		for _, count := range histogram {
			score += costs[count]
		}
		if score > bestScore {
			best, scratch = scratch, best
			bestScore = score
		}
	}
	return best, scratch
}

func predictor(filter, left, up, upperLeft byte) byte {
	switch filter {
	case 1:
		return left
	case 2:
		return up
	case 3:
		return byte((int(left) + int(up)) / 2)
	case 4:
		leftDistance := abs(int(up) - int(upperLeft))
		upDistance := abs(int(left) - int(upperLeft))
		diagonalDistance := abs(int(left) + int(up) - 2*int(upperLeft))
		if leftDistance <= upDistance && leftDistance <= diagonalDistance {
			return left
		}
		if upDistance <= diagonalDistance {
			return up
		}
		return upperLeft
	default:
		return 0
	}
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func equivalentPNG(original, candidate image.Image, tolerance uint8) bool {
	bounds := original.Bounds()
	if bounds != candidate.Bounds() {
		return false
	}
	if tolerance > 0 {
		return checkQuantisation(original, candidate, tolerance)
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			a := straightPixel(original, x, y)
			b := straightPixel(candidate, x, y)
			if a.A != b.A || (a.A != 0 && a != b) {
				return false
			}
		}
	}
	return true
}

func straightPixel(img image.Image, x, y int) color.NRGBA64 {
	switch img := img.(type) {
	case *image.NRGBA:
		return expandNRGBA(img.NRGBAAt(x, y))
	case *image.NRGBA64:
		return img.NRGBA64At(x, y)
	default:
		return straightColour(img.At(x, y))
	}
}

func straightColour(value color.Color) color.NRGBA64 {
	if rgba, ok := value.(color.NRGBA); ok {
		return expandNRGBA(rgba)
	}
	return color.NRGBA64Model.Convert(value).(color.NRGBA64)
}

func expandNRGBA(rgba color.NRGBA) color.NRGBA64 {
	return color.NRGBA64{R: uint16(rgba.R) * 257, G: uint16(rgba.G) * 257,
		B: uint16(rgba.B) * 257, A: uint16(rgba.A) * 257}
}

// Fully transparent pixels render identically regardless of their RGB samples.
func normaliseTransparentPixels(original image.Image) image.Image {
	switch img := original.(type) {
	case *image.NRGBA:
		if pixels := clearHiddenRGB(img.Pix, 4); pixels != nil {
			normalised := *img
			normalised.Pix = pixels
			return &normalised
		}
	case *image.NRGBA64:
		if pixels := clearHiddenRGB(img.Pix, 8); pixels != nil {
			normalised := *img
			normalised.Pix = pixels
			return &normalised
		}
	}
	return nil
}

// The standard PNG decoder returns densely packed NRGBA and NRGBA64 buffers.
func clearHiddenRGB(pixels []byte, step int) []byte {
	alphaOffset := 3 * step / 4
	var normalised []byte
	for offset := 0; offset < len(pixels); offset += step {
		pixel := pixels[offset : offset+step]
		var alpha, rgb byte
		for _, sample := range pixel[alphaOffset:] {
			alpha |= sample
		}
		if alpha != 0 {
			continue
		}
		for _, sample := range pixel[:alphaOffset] {
			rgb |= sample
		}
		if rgb != 0 {
			if normalised == nil {
				normalised = bytes.Clone(pixels)
			}
			clear(normalised[offset : offset+alphaOffset])
		}
	}
	return normalised
}
