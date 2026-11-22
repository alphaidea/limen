package heiccodec

import (
	"bytes"
	"encoding/binary"
	"sort"

	"github.com/gen2brain/h265/heic"
	"github.com/gen2brain/h265/hevc"
)

const appleGainMap = "urn:com:apple:photo:2020:aux:hdrgainmap"

type itemReference struct {
	kind string
	from uint32
	to   []uint32
}

func itemReferences(data []byte) ([]itemReference, bool) {
	if len(data) < 4 || data[0] > 1 || data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return nil, false
	}
	width := 2 + 2*int(data[0])
	read := func(p []byte) uint32 {
		if width == 2 {
			return uint32(binary.BigEndian.Uint16(p))
		}
		return binary.BigEndian.Uint32(p)
	}
	var refs []itemReference
	for pos := 4; pos < len(data); {
		box, ok := nextBox(data, pos)
		if !ok || len(refs) >= 256 {
			return nil, false
		}
		kind := string(data[pos+4 : pos+8])
		p := data[box.payload:box.end]
		if len(p) < width+2 {
			return nil, false
		}
		count := int(binary.BigEndian.Uint16(p[width:]))
		if count == 0 || count > 256 || len(p) != width+2+width*count {
			return nil, false
		}
		switch kind {
		case "auxl", "dimg", "cdsc", "thmb", "prem":
		default:
			return nil, false
		}
		ref := itemReference{kind: kind, from: read(p)}
		p = p[width+2:]
		for range count {
			ref.to = append(ref.to, read(p))
			p = p[width:]
		}
		refs = append(refs, ref)
		pos = box.end
	}
	return refs, true
}

func readItemTypes(data []byte) map[uint32]string {
	if len(data) < 6 || data[0] > 1 {
		return nil
	}
	count := int(binary.BigEndian.Uint16(data[4:6]))
	start := 6
	if data[0] == 1 {
		if len(data) < 8 || binary.BigEndian.Uint32(data[4:8]) > 256 {
			return nil
		}
		count, start = int(binary.BigEndian.Uint32(data[4:8])), 8
	}
	if count == 0 || count > 256 {
		return nil
	}
	items := make(map[uint32]string, count)
	for range count {
		box, ok := nextBox(data, start)
		if !ok || string(data[start+4:start+8]) != "infe" {
			return nil
		}
		p := data[box.payload:box.end]
		if len(p) < 13 || (p[0] != 2 && p[0] != 3) {
			return nil
		}
		id := uint32(binary.BigEndian.Uint16(p[4:6]))
		if p[0] == 3 {
			if len(p) < 15 {
				return nil
			}
			id = binary.BigEndian.Uint32(p[4:8])
			p = p[2:]
		}
		if id == 0 || items[id] != "" || p[6] != 0 || p[7] != 0 {
			return nil
		}
		kind := string(p[8:12])
		switch kind {
		case "hvc1", "grid", "Exif", "mime", "tmap":
		default:
			return nil
		}
		items[id] = kind
		start = box.end
	}
	if start != len(data) {
		return nil
	}
	return items
}

func baseImageTargets(table propertyTable, items map[uint32]string, refs []itemReference, extents map[uint32]itemExtent, requireGainMap bool) []uint32 {
	primary := table.itemID
	imageItem := func(id uint32) bool { return items[id] == "grid" || items[id] == "hvc1" }
	if !imageItem(primary) {
		return nil
	}
	var targets []uint32
	gainMaps := make(map[uint32]bool)
	for _, ref := range refs {
		if items[ref.from] == "" {
			return nil
		}
		for _, to := range ref.to {
			if items[to] == "" {
				return nil
			}
		}
		if ref.kind == "auxl" && ref.from != primary && imageItem(ref.from) && len(ref.to) == 1 && ref.to[0] == primary {
			for _, index := range table.items[ref.from] {
				if index&0x7fff == 0 {
					continue
				}
				p := table.properties[(index&0x7fff)-1]
				if len(p) >= 13 && string(p[4:8]) == "auxC" &&
					bytes.HasPrefix(p[12:], []byte(appleGainMap+"\x00")) && extents[ref.from].end != 0 {
					gainMaps[ref.from] = true
				}
			}
		}
		if ref.kind == "dimg" {
			if ref.from == primary {
				if targets != nil {
					return nil
				}
				targets = ref.to
			}
			if items[ref.from] == "tmap" && len(ref.to) == 2 && ref.to[0] == primary &&
				ref.to[1] != primary && imageItem(ref.to[1]) && extents[ref.from].end != 0 && extents[ref.to[1]].end != 0 {
				gainMaps[ref.to[1]] = true
			}
		}
	}
	if requireGainMap && len(gainMaps) == 0 {
		return nil
	}
	if items[primary] == "hvc1" {
		if targets != nil {
			return nil
		}
		return []uint32{primary}
	}
	selected := make(map[uint32]bool)
	for _, id := range targets {
		if items[id] != "hvc1" || selected[id] || gainMaps[id] {
			return nil
		}
		selected[id] = true
	}
	// A tile shared by a different derivation is not exclusive to the base.
	for _, ref := range refs {
		if ref.kind == "dimg" && ref.from != primary {
			for _, id := range ref.to {
				if selected[id] {
					return nil
				}
			}
		}
	}
	return targets
}

type baseImageEdit struct {
	extent   itemExtent
	config   []byte
	sample   []byte
	property int
}

func encodeBaseSample(record, input []byte, width, height, limit int) (config, sample []byte) {
	nals, ok := readHDRSample(record, input)
	if !ok {
		return nil, nil
	}
	p := decodeNative(nals.parameters, nals.slices, limit)
	if p == nil {
		return nil, nil
	}
	defer p.Release()
	if p.CropW != width || p.CropH != height || (p.BitDepth != 8 && p.BitDepth != 10 && p.BitDepth != 12) ||
		p.BitDepth != p.BitDepthC || p.ChromaFormat < 0 || p.ChromaFormat > 3 {
		return nil, nil
	}
	format := [...]hevc.ChromaFormat{hevc.ChromaMono, hevc.Chroma420, hevc.Chroma422, hevc.Chroma444}[p.ChromaFormat]
	encoder, err := hevc.NewEncoder(hevc.EncoderOptions{
		Width: p.CropW, Height: p.CropH, Chroma: format, BitDepth: p.BitDepth, QP: 18,
	})
	if err != nil {
		return nil, nil
	}
	encoded, err := encoder.Encode(nativeFrame(p))
	if err != nil || len(encoded) != 4 || encoded[1].Type != hevc.NALSPS || !encoded[3].Type.IsIDR() {
		return nil, nil
	}
	if p.ColorPrimaries != 2 || p.ColorTransfer != 2 || p.ColorMatrix != 2 || p.FullRange {
		encoded[1].RBSP = signalHDR(encoded[1].RBSP, heic.ColorInfo{
			Primaries: p.ColorPrimaries, Transfer: p.ColorTransfer, Matrix: p.ColorMatrix, FullRange: p.FullRange,
		})
		if encoded[1].RBSP == nil {
			return nil, nil
		}
	}
	for _, group := range [][]hevc.NALUnit{nals.prefixSEI, encoded[3:], nals.suffixSEI} {
		for _, nal := range group {
			raw := hevc.MarshalNAL(nal)
			sample = binary.BigEndian.AppendUint32(sample, uint32(len(raw)))
			sample = append(sample, raw...)
		}
	}
	if len(sample) >= len(input) {
		return nil, nil
	}
	decoded := decodeNative(encoded[:3], encoded[3:], limit)
	if decoded == nil {
		return nil, nil
	}
	defer decoded.Release()
	if decoded.CropW != p.CropW || decoded.CropH != p.CropH || decoded.BitDepth != p.BitDepth ||
		decoded.BitDepthC != p.BitDepthC || decoded.ChromaFormat != p.ChromaFormat || decoded.FullRange != p.FullRange ||
		decoded.ColorPrimaries != p.ColorPrimaries || decoded.ColorTransfer != p.ColorTransfer || decoded.ColorMatrix != p.ColorMatrix {
		return nil, nil
	}
	config = replacementConfig(encoded[:3], nals.configSEI, p.ChromaFormat, p.BitDepth)
	return config, sample
}

func allowedBoxes(data []byte, start int, allowed ...string) bool {
	seen := make(map[string]bool)
	for start < len(data) {
		box, ok := nextBox(data, start)
		if !ok {
			return false
		}
		kind := string(data[start+4 : start+8])
		known := false
		for _, name := range allowed {
			if kind == name {
				known = true
				break
			}
		}
		if !known || (seen[kind] && kind != "free" && kind != "skip") {
			return false
		}
		seen[kind] = true
		start = box.end
	}
	return true
}

// compressBaseItems replaces eligible base-image samples while keeping all
// auxiliary items, metadata, and item relationships.
func compressBaseItems(input []byte, metaSpan boxSpan, large bool, check func([]byte, []byte) bool) []byte {
	if !allowedBoxes(input, 0, "ftyp", "meta", "mdat", "free", "skip") {
		return input
	}
	mdat, ok := findBox(input, 0, "mdat")
	if !ok || mdat.start < metaSpan.end {
		return input
	}
	meta := input[metaSpan.payload:metaSpan.end]
	if len(meta) < 4 || binary.BigEndian.Uint32(meta) != 0 ||
		!allowedBoxes(meta, 4, "hdlr", "pitm", "iinf", "iloc", "iref", "iprp", "idat", "grpl", "dinf", "free") {
		return input
	}
	iinf, ok := findBox(meta, 4, "iinf")
	if !ok {
		return input
	}
	items := readItemTypes(meta[iinf.payload:iinf.end])
	var refs []itemReference
	if iref, found := findBox(meta, 4, "iref"); found {
		refs, ok = itemReferences(meta[iref.payload:iref.end])
		if !ok {
			return input
		}
	}
	table, ok := readItemProperties(meta, true)
	if !ok {
		return input
	}
	loc, ok := findBox(meta, 4, "iloc")
	if !ok {
		return input
	}
	idat, _ := findBox(meta, 4, "idat")
	extents := readItemExtents(meta[loc.payload:loc.end], mdat, idat)
	if len(extents) != len(items) {
		return input
	}
	byID := make(map[uint32]itemExtent, len(extents))
	for _, extent := range extents {
		if items[uint32(extent.id)] == "" {
			return input
		}
		byID[uint32(extent.id)] = extent
	}
	targets := baseImageTargets(table, items, refs, byID, !large)
	if len(targets) == 0 {
		return input
	}
	var edits []baseImageEdit
	pixelsLeft := uint64(maxCompressionPixels)
	if large {
		width, height := itemDimensions(table, table.itemID)
		if height == 0 || width > maxLargeHEICPixels/height ||
			(items[table.itemID] == "hvc1" && width > hevc.MaxLumaSamples/height) {
			return input
		}
		pixelsLeft = maxLargeHEICCodedPixels
		if items[table.itemID] == "grid" && !validPrimaryGrid(input, meta, idat, byID[table.itemID], table, targets) {
			return input
		}
	}
	for _, id := range targets {
		extent, ok := byID[id]
		if !ok || extent.inMetadata {
			continue
		}
		for _, other := range extents {
			if other.id != extent.id && !other.inMetadata && other.offset < extent.end && other.end > extent.offset {
				return input
			}
		}
		property := -1
		width, height := itemDimensions(table, id)
		for i, index := range table.items[id] {
			if index&0x7fff == 0 {
				continue
			}
			p := table.properties[(index&0x7fff)-1]
			if string(p[4:8]) == "hvcC" {
				if property != -1 {
					return input
				}
				property = i
			}
		}
		if property == -1 || width == 0 {
			continue
		}
		if width > pixelsLeft/height {
			return input
		}
		// Reserve coded padding before decoding so malformed tiles also
		// consume their share of the work budget.
		padded := ((width + 63) &^ 63) * ((height + 63) &^ 63)
		codedLimit := uint64(maxCompressionPixels)
		if large && len(targets) == 1 {
			codedLimit = hevc.MaxLumaSamples
		}
		limit := min(codedLimit, padded)
		budget := width * height
		if large {
			budget = padded
		}
		if budget > pixelsLeft {
			return input
		}
		pixelsLeft -= budget
		index := table.items[id][property] & 0x7fff
		record := table.properties[index-1][8:]
		config, sample := encodeBaseSample(record, input[extent.offset:extent.end], int(width), int(height), int(limit))
		if config != nil {
			edits = append(edits, baseImageEdit{extent, config, sample, property})
		}
	}
	if len(edits) == 0 {
		return input
	}
	candidate := rebuildBaseItems(input, metaSpan, mdat, meta, table, extents, edits)
	if candidate != nil && len(candidate) < len(input) && (!large || sameHEICConfig(input, candidate)) && check(input, candidate) {
		return candidate
	}
	return input
}

func rebuildBaseItems(input []byte, metaSpan, mdat boxSpan, meta []byte, table propertyTable, extents []itemExtent, edits []baseImageEdit) []byte {
	iprpSpan, ok := findBox(meta, 4, "iprp")
	if !ok {
		return nil
	}
	iprp := meta[iprpSpan.payload:iprpSpan.end]
	ipco, ok := findBox(iprp, 0, "ipco")
	if !ok {
		return nil
	}
	ipma, ok := findBox(iprp, 0, "ipma")
	if !ok || !allowedBoxes(iprp, 0, "ipco", "ipma") || ipco.end > ipma.start {
		return nil
	}
	properties := bytes.Clone(iprp[ipco.payload:ipco.end])
	newIndices := make(map[string]uint16)
	for _, edit := range edits {
		key := string(edit.config)
		index, ok := newIndices[key]
		if !ok {
			next := len(table.properties) + len(newIndices) + 1
			if next > 32767 {
				return nil
			}
			index = uint16(next)
			newIndices[key] = index
			properties = appendBox(properties, "hvcC", edit.config)
		}
		entries := table.items[uint32(edit.extent.id)]
		entries[edit.property] = index | entries[edit.property]&0x8000
	}
	header := iprp[ipma.payload : ipma.payload+4]
	wide := header[3] == 1 || len(table.properties)+len(newIndices) > 127
	associations := binary.BigEndian.AppendUint32(bytes.Clone(header), uint32(len(table.order)))
	if wide {
		associations[3] = 1
	}
	for _, id := range table.order {
		indices := table.items[id]
		if header[0] == 1 {
			associations = binary.BigEndian.AppendUint32(associations, id)
		} else {
			associations = binary.BigEndian.AppendUint16(associations, uint16(id))
		}
		associations = append(associations, byte(len(indices)))
		for _, index := range indices {
			if wide {
				associations = binary.BigEndian.AppendUint16(associations, index)
			} else {
				associations = append(associations, byte(index&0x7f|index>>8&0x80))
			}
		}
	}
	iprp = replaceBox(iprp, ipma, associations)
	iprp = replaceBox(iprp, ipco, properties)
	meta = replaceBox(meta, iprpSpan, iprp)
	loc, ok := findBox(meta, 4, "iloc")
	if !ok {
		return nil
	}
	locations := binary.BigEndian.AppendUint16([]byte{1, 0, 0, 0, 0x44, 0}, uint16(len(extents)))
	metaChange := len(meta) + 8 + len(locations) + 16*len(extents) - (loc.end - loc.payload) - (metaSpan.end - metaSpan.start)
	sort.Slice(edits, func(i, j int) bool { return edits[i].extent.offset < edits[j].extent.offset })
	mdatChange := 0
	for _, edit := range edits {
		mdatChange += len(edit.sample) - (edit.extent.end - edit.extent.offset)
	}
	if metaChange+mdatChange >= 0 {
		return nil
	}
	for _, extent := range extents {
		offset, size, method := extent.offset, extent.end-extent.offset, byte(0)
		if extent.inMetadata {
			method = 1
		} else {
			offset += metaChange
			for _, edit := range edits {
				if edit.extent.id == extent.id {
					size = len(edit.sample)
					break
				}
				if edit.extent.end <= extent.offset {
					offset += len(edit.sample) - (edit.extent.end - edit.extent.offset)
				}
			}
		}
		locations = binary.BigEndian.AppendUint16(locations, extent.id)
		locations = append(locations, 0, method, 0, 0, 0, 1)
		locations = binary.BigEndian.AppendUint32(locations, uint32(offset))
		locations = binary.BigEndian.AppendUint32(locations, uint32(size))
	}
	meta = replaceBox(meta, loc, locations)
	out := make([]byte, 0, len(input)+metaChange+mdatChange)
	out = append(out, input[:metaSpan.start]...)
	out = appendBox(out, "meta", meta)
	out = append(out, input[metaSpan.end:mdat.start]...)
	mdatSize := mdat.end - mdat.start + mdatChange
	if mdat.payload-mdat.start == 16 {
		out = append(out, input[mdat.start:mdat.start+8]...)
		out = binary.BigEndian.AppendUint64(out, uint64(mdatSize))
	} else {
		out = binary.BigEndian.AppendUint32(out, uint32(mdatSize))
		out = append(out, "mdat"...)
	}
	offset := mdat.payload
	for _, edit := range edits {
		out = append(out, input[offset:edit.extent.offset]...)
		out = append(out, edit.sample...)
		offset = edit.extent.end
	}
	return append(out, input[offset:]...)
}
