package heiccodec

import (
	"bytes"
	"encoding/binary"
	"image"
	"math/bits"

	"github.com/gen2brain/h265/heic"
	"github.com/gen2brain/h265/hevc"
)

func isHDR(primaries, transfer uint16) bool {
	return (primaries == 9 || primaries == 12) && (transfer == 16 || transfer == 18)
}

type itemExtent struct {
	id          uint16
	offset, end int
	inMetadata  bool
}

// itemExtents accepts one local, contiguous extent per item. Offsets are
// checked against mdat before any slice is constructed.
func itemExtents(data []byte, mdat boxSpan, primary uint32) ([]itemExtent, int) {
	extents := readItemExtents(data, mdat, boxSpan{})
	primaryIndex := -1
	for i, extent := range extents {
		if uint32(extent.id) == primary {
			primaryIndex = i
			break
		}
	}
	if primaryIndex < 0 {
		return nil, -1
	}
	sample := extents[primaryIndex]
	for i, extent := range extents {
		if i != primaryIndex && extent.offset < sample.end && extent.end > sample.offset {
			return nil, -1
		}
	}
	return extents, primaryIndex
}

func readItemExtents(data []byte, mdat, idat boxSpan) []itemExtent {
	if len(data) < 8 || data[0] > 1 || data[1] != 0 || data[2] != 0 || data[3] != 0 ||
		data[5]&15 != 0 {
		return nil
	}
	version := data[0]
	offsetSize, lengthSize, baseSize := int(data[4]>>4), int(data[4]&15), int(data[5]>>4)
	if (offsetSize != 0 && offsetSize != 4 && offsetSize != 8) || (lengthSize != 4 && lengthSize != 8) ||
		(baseSize != 0 && baseSize != 4 && baseSize != 8) {
		return nil
	}
	count := int(binary.BigEndian.Uint16(data[6:]))
	entrySize := 6 + 2*int(version) + baseSize + offsetSize + lengthSize
	data = data[8:]
	if count == 0 || count > 256 || len(data) != count*entrySize {
		return nil
	}
	read := func(p []byte, n int) uint64 {
		switch n {
		case 4:
			return uint64(binary.BigEndian.Uint32(p))
		case 8:
			return binary.BigEndian.Uint64(p)
		default:
			return 0
		}
	}
	var extents []itemExtent
	ids := make(map[uint16]bool)
	for range count {
		id := binary.BigEndian.Uint16(data)
		p := 2 + 2*int(version)
		var method uint16
		if version != 0 {
			method = binary.BigEndian.Uint16(data[2:])
		}
		if method > 1 || (method == 1 && idat.end == 0) || id == 0 || ids[id] || binary.BigEndian.Uint16(data[p:]) != 0 {
			return nil
		}
		ids[id] = true
		base := read(data[p+2:], baseSize)
		p += 2 + baseSize
		if binary.BigEndian.Uint16(data[p:]) != 1 {
			return nil
		}
		p += 2
		offset, length := read(data[p:], offsetSize), read(data[p+offsetSize:], lengthSize)
		minOffset, maxOffset := mdat.payload, mdat.end
		if method == 1 {
			minOffset, maxOffset = 0, idat.end-idat.payload
		}
		if base > uint64(maxOffset) || offset > uint64(maxOffset)-base {
			return nil
		}
		offset += base
		if offset < uint64(minOffset) || length == 0 || length > uint64(maxOffset)-offset {
			return nil
		}
		extents = append(extents, itemExtent{id, int(offset), int(offset + length), method == 1})
		data = data[entrySize:]
	}
	return extents
}

type hdrSample struct {
	parameters []hevc.NALUnit
	slices     []hevc.NALUnit
	configSEI  []hevc.NALUnit
	prefixSEI  []hevc.NALUnit
	suffixSEI  []hevc.NALUnit
}

func readHDRSample(config, data []byte) (hdrSample, bool) {
	var sample hdrSample
	if len(config) < 23 || config[0] != 1 {
		return sample, false
	}
	lengthSize, arrays := int(config[21]&3)+1, int(config[22])
	config = config[23:]
	for range arrays {
		if len(config) < 3 {
			return sample, false
		}
		kind, count := hevc.NALType(config[0]&0x3f), int(binary.BigEndian.Uint16(config[1:]))
		config = config[3:]
		for range count {
			nal, rest, ok := takeNAL(config, 2)
			if !ok || nal.Type != kind || len(sample.parameters)+len(sample.configSEI) >= 64 {
				return sample, false
			}
			switch kind {
			case hevc.NALVPS, hevc.NALSPS, hevc.NALPPS:
				sample.parameters = append(sample.parameters, nal)
			case hevc.NALPrefixSEI, hevc.NALSuffixSEI:
				if !staticSEI(nal.RBSP) {
					return sample, false
				}
				sample.configSEI = append(sample.configSEI, nal)
			default:
				return sample, false
			}
			config = rest
		}
	}
	if len(config) != 0 {
		return sample, false
	}
	for count := 0; len(data) > 0; count++ {
		nal, rest, ok := takeNAL(data, lengthSize)
		if !ok || count >= 256 {
			return sample, false
		}
		switch {
		case nal.Type == hevc.NALPrefixSEI && len(sample.slices) == 0 && staticSEI(nal.RBSP):
			sample.prefixSEI = append(sample.prefixSEI, nal)
		case nal.Type.IsIRAP() && len(sample.suffixSEI) == 0:
			sample.slices = append(sample.slices, nal)
		case nal.Type == hevc.NALSuffixSEI && len(sample.slices) > 0 && staticSEI(nal.RBSP):
			sample.suffixSEI = append(sample.suffixSEI, nal)
		default:
			return sample, false
		}
		data = rest
	}
	return sample, len(sample.slices) != 0
}

func takeNAL(data []byte, width int) (hevc.NALUnit, []byte, bool) {
	if len(data) < width {
		return hevc.NALUnit{}, nil, false
	}
	var size uint64
	for _, b := range data[:width] {
		size = size<<8 | uint64(b)
	}
	data = data[width:]
	if size < 2 || size > uint64(len(data)) {
		return hevc.NALUnit{}, nil, false
	}
	nal, ok := hevc.ParseNAL(data[:int(size)])
	return nal, data[int(size):], ok && nal.LayerID == 0
}

func staticSEI(data []byte) bool {
	// Copy only mastering-display, content-light and alternate-transfer
	// messages. Other messages may describe the old coding or dynamic HDR.
	message := false
	for len(data) > 2 {
		size := int(data[1])
		if !((data[0] == 137 && size == 24) || (data[0] == 144 && size == 4) ||
			(data[0] == 147 && size == 1 && data[2] == 18)) || size > len(data)-2 {
			return false
		}
		message = true
		data = data[2+size:]
	}
	return message && len(data) == 1 && data[0] == 0x80
}

func decodeNative(parameters, slices []hevc.NALUnit, pixelLimit int) *hevc.Picture {
	var decoder hevc.Decoder
	decoder.FrameSizeLimit(pixelLimit)
	decoder.Threads(2)
	for _, nal := range parameters {
		if _, err := decoder.DecodeNAL(nal); err != nil {
			return nil
		}
	}
	var pictures []*hevc.Picture
	for _, nal := range slices {
		output, err := decoder.DecodeNAL(nal)
		pictures = append(pictures, output...)
		if err != nil || len(pictures) > 1 {
			return nil
		}
	}
	pictures = append(pictures, decoder.Flush()...)
	if len(pictures) != 1 || pictures[0].Corrupt {
		return nil
	}
	return pictures[0]
}

func nativeFrame(p *hevc.Picture) hevc.Frame {
	sw, sh := 1, 1
	if p.ChromaFormat == 1 || p.ChromaFormat == 2 {
		sw = 2
	}
	if p.ChromaFormat == 1 {
		sh = 2
	}
	y := p.CropY*p.StrideY + p.CropX
	frame := hevc.Frame{StrideY: p.StrideY}
	if p.BitDepth == 8 {
		frame.Y = p.Y[y:]
	} else {
		frame.Y16 = p.Y16[y:]
	}
	if p.ChromaFormat != 0 {
		origin := (p.CropY/sh)*p.StrideC + p.CropX/sw
		if p.BitDepth == 8 {
			frame.Cb, frame.Cr = p.Cb[origin:], p.Cr[origin:]
		} else {
			frame.Cb16, frame.Cr16 = p.Cb16[origin:], p.Cr16[origin:]
		}
		frame.StrideC = p.StrideC
	}
	return frame
}

func signalHDR(sps []byte, colour heic.ColorInfo) []byte {
	// The pinned encoder ends its SPS with zero VUI and extension flags
	// before rbsp_trailing_bits. Replace those flags with a colour-only VUI.
	if len(sps) == 0 || sps[len(sps)-1] == 0 {
		return nil
	}
	stop := len(sps)*8 - bits.TrailingZeros8(sps[len(sps)-1]) - 1
	start := stop - 2
	if start < 0 || sps[start/8]&(0x80>>uint(start%8)) != 0 ||
		sps[(start+1)/8]&(0x80>>uint((start+1)%8)) != 0 {
		return nil
	}
	out := append(bytes.Clone(sps[:start/8]), make([]byte, 8)...)
	if start%8 != 0 {
		out[start/8] = sps[start/8] & (0xff << uint(8-start%8))
	}
	pos := start
	put := func(value uint16, width int) {
		for i := width - 1; i >= 0; i-- {
			out[pos/8] |= byte(value>>i&1) << uint(7-pos%8)
			pos++
		}
	}
	put(0b1001101, 7) // VUI present, video signal type present, video format unspecified.
	if colour.FullRange {
		put(1, 1)
	} else {
		put(0, 1)
	}
	put(1, 1) // Colour description present.
	put(colour.Primaries, 8)
	put(colour.Transfer, 8)
	put(colour.Matrix, 8)
	put(0, 8) // Remaining VUI flags and SPS extension flag.
	put(1, 1) // RBSP stop bit.
	return out[:(pos+7)/8]
}

func replacementConfig(parameters, supplemental []hevc.NALUnit, chroma, depth int) []byte {
	ptl, ok := hevc.ProfileTierLevel(parameters[1].RBSP)
	if !ok {
		return nil
	}
	output := append([]byte{1}, ptl...)
	output = append(output, 0xf0, 0, 0xfc, 0xfc|byte(chroma), 0xf8|byte(depth-8), 0xf8|byte(depth-8), 0, 0, 0x0f, 0)
	for _, kind := range []hevc.NALType{hevc.NALVPS, hevc.NALSPS, hevc.NALPPS, hevc.NALPrefixSEI, hevc.NALSuffixSEI} {
		var payload []byte
		count := uint16(0)
		for _, group := range [][]hevc.NALUnit{parameters, supplemental} {
			for _, nal := range group {
				if nal.Type != kind {
					continue
				}
				raw := hevc.MarshalNAL(nal)
				if len(raw) > 65535 {
					return nil
				}
				payload = binary.BigEndian.AppendUint16(payload, uint16(len(raw)))
				payload = append(payload, raw...)
				count++
			}
		}
		if count != 0 {
			output[22]++
			output = append(output, 0x80|byte(kind))
			output = binary.BigEndian.AppendUint16(output, count)
			output = append(output, payload...)
		}
	}
	return output
}

func compressHDR(input []byte, metaSpan boxSpan, table propertyTable, config image.Config, check func([]byte, []byte) bool) ([]byte, error) {
	meta := input[metaSpan.payload:metaSpan.end]
	iprpSpan, ok := findBox(meta, 4, "iprp")
	if !ok {
		return input, nil
	}
	iprp := meta[iprpSpan.payload:iprpSpan.end]
	ipcoSpan, ok := findBox(iprp, 0, "ipco")
	if !ok {
		return input, nil
	}
	ipco := iprp[ipcoSpan.payload:ipcoSpan.end]
	hvcCSpan, ok := findBox(ipco, 0, "hvcC")
	if !ok {
		return input, nil
	}
	var hasConfig, hasColour bool
	var colour heic.ColorInfo
	for _, association := range table.primary {
		index := int(association & 0x7fff)
		property := table.properties[index-1]
		switch string(property[4:8]) {
		case "hvcC":
			if table.uses[index] != 1 {
				return input, nil
			}
			hasConfig = true
		case "colr":
			if hasColour {
				return input, nil
			}
			hasColour = true
			colour = heic.ColorInfo{
				Primaries: binary.BigEndian.Uint16(property[12:]),
				Transfer:  binary.BigEndian.Uint16(property[14:]),
				Matrix:    binary.BigEndian.Uint16(property[16:]),
				FullRange: property[18]&0x80 != 0,
			}
		}
	}
	if !hasConfig || (hasColour && !isHDR(colour.Primaries, colour.Transfer)) {
		return input, nil
	}
	mdat, ok := findBox(input, 0, "mdat")
	if !ok || mdat.start < metaSpan.end {
		return input, nil
	}
	ilocSpan, ok := findBox(meta, 4, "iloc")
	if !ok {
		return input, nil
	}
	extents, primaryIndex := itemExtents(meta[ilocSpan.payload:ilocSpan.end], mdat, table.itemID)
	if primaryIndex < 0 {
		return input, nil
	}
	item := extents[primaryIndex]
	nals, ok := readHDRSample(ipco[hvcCSpan.payload:hvcCSpan.end], input[item.offset:item.end])
	if !ok {
		return input, nil
	}
	picture := decodeNative(nals.parameters, nals.slices, maxCompressionPixels)
	if picture == nil {
		return input, nil
	}
	defer picture.Release()
	if (picture.BitDepth != 10 && picture.BitDepth != 12) || picture.BitDepthC != picture.BitDepth ||
		picture.CropW != config.Width || picture.CropH != config.Height || picture.ChromaFormat < 1 || picture.ChromaFormat > 3 {
		return input, nil
	}
	nativeColour := heic.ColorInfo{Primaries: picture.ColorPrimaries, Transfer: picture.ColorTransfer, Matrix: picture.ColorMatrix, FullRange: picture.FullRange}
	if !hasColour {
		colour = nativeColour
	}
	if !isHDR(colour.Primaries, colour.Transfer) || (colour.Matrix != 9 && colour.Matrix != 1 && colour.Matrix != 6) ||
		(nativeColour.Primaries != 2 && nativeColour.Primaries != colour.Primaries) ||
		(nativeColour.Transfer != 2 && nativeColour.Transfer != colour.Transfer) ||
		(nativeColour.Matrix != 2 && (nativeColour.Matrix != colour.Matrix || nativeColour.FullRange != colour.FullRange)) {
		return input, nil
	}
	frame := nativeFrame(picture)
	chroma := [...]hevc.ChromaFormat{hevc.Chroma420, hevc.Chroma422, hevc.Chroma444}[picture.ChromaFormat-1]
	for _, quality := range searchQualities {
		encoder, err := hevc.NewEncoder(hevc.EncoderOptions{
			Width: picture.CropW, Height: picture.CropH, Chroma: chroma,
			BitDepth: picture.BitDepth, QP: 51 - quality*50/100,
		})
		if err != nil {
			return input, nil
		}
		encoded, err := encoder.Encode(frame)
		if err != nil || len(encoded) != 4 || encoded[0].Type != hevc.NALVPS ||
			encoded[1].Type != hevc.NALSPS || encoded[2].Type != hevc.NALPPS || !encoded[3].Type.IsIDR() {
			return input, nil
		}
		encoded[1].RBSP = signalHDR(encoded[1].RBSP, colour)
		if encoded[1].RBSP == nil {
			return input, nil
		}
		record := replacementConfig(encoded[:3], nals.configSEI, picture.ChromaFormat, picture.BitDepth)
		if record == nil {
			return input, nil
		}
		var sample []byte
		for _, group := range [][]hevc.NALUnit{nals.prefixSEI, encoded[3:], nals.suffixSEI} {
			for _, nal := range group {
				raw := hevc.MarshalNAL(nal)
				sample = binary.BigEndian.AppendUint32(sample, uint32(len(raw)))
				sample = append(sample, raw...)
			}
		}
		if len(sample) >= item.end-item.offset {
			continue
		}
		candidateProperty := replaceBox(ipco, hvcCSpan, record)
		candidateProperties := replaceBox(iprp, ipcoSpan, candidateProperty)
		candidateMeta := replaceBox(meta, iprpSpan, candidateProperties)
		loc, ok := findBox(candidateMeta, 4, "iloc")
		if !ok {
			return input, nil
		}
		candidate := rebuildHDR(input, metaSpan, mdat, loc, candidateMeta, extents, primaryIndex, sample)
		if len(candidate) >= len(input) {
			continue
		}
		decoded := decodeNative(encoded[:3], encoded[3:], maxCompressionPixels)
		if decoded == nil {
			continue
		}
		valid := decoded.CropW == picture.CropW && decoded.CropH == picture.CropH &&
			decoded.BitDepth == picture.BitDepth && decoded.BitDepthC == picture.BitDepthC &&
			decoded.ChromaFormat == picture.ChromaFormat && decoded.ColorPrimaries == colour.Primaries &&
			decoded.ColorTransfer == colour.Transfer && decoded.ColorMatrix == colour.Matrix && decoded.FullRange == colour.FullRange
		decoded.Release()
		if valid && check(input, candidate) {
			return candidate, nil
		}
	}
	return input, nil
}

// rebuildHDR replaces only the primary coded sample and its configuration.
// All other item bytes and property associations are retained.
func rebuildHDR(input []byte, metaSpan, mdat, loc boxSpan, meta []byte, extents []itemExtent, primaryIndex int, sample []byte) []byte {
	locations := []byte{0, 0, 0, 0, 0x44, 0}
	locations = binary.BigEndian.AppendUint16(locations, uint16(len(extents)))
	locationSize := len(locations) + len(extents)*14
	metaSizeChange := 8 + len(meta) + locationSize - (loc.end - loc.payload) - (metaSpan.end - metaSpan.start)
	oldSample := extents[primaryIndex]
	sampleSizeChange := len(sample) - (oldSample.end - oldSample.offset)
	for i, extent := range extents {
		offset, size := extent.offset+metaSizeChange, extent.end-extent.offset
		if i == primaryIndex {
			size = len(sample)
		} else if extent.offset >= oldSample.end {
			offset += sampleSizeChange
		}
		locations = binary.BigEndian.AppendUint16(locations, extent.id)
		locations = append(locations, 0, 0, 0, 1)
		locations = binary.BigEndian.AppendUint32(locations, uint32(offset))
		locations = binary.BigEndian.AppendUint32(locations, uint32(size))
	}
	meta = replaceBox(meta, loc, locations)
	out := make([]byte, 0, len(input)+metaSizeChange+sampleSizeChange)
	out = append(out, input[:metaSpan.start]...)
	out = appendBox(out, "meta", meta)
	out = append(out, input[metaSpan.end:mdat.start]...)
	out = binary.BigEndian.AppendUint32(out, uint32(mdat.end-mdat.start+sampleSizeChange))
	out = append(out, "mdat"...)
	out = append(out, input[mdat.payload:oldSample.offset]...)
	out = append(out, sample...)
	out = append(out, input[oldSample.end:]...)
	return out
}
