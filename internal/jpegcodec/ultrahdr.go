package jpegcodec

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"image/jpeg"
	"io"
	"strconv"
)

const isoGainMap = "urn:iso:std:iso:ts:21496:-1\x00"
const hdrGainMapNamespace = "http://ns.adobe.com/hdr-gain-map/1.0/"
const containerNamespace = "http://ns.google.com/photos/1.0/container/"
const containerItemNamespace = "http://ns.google.com/photos/1.0/container/item/"
const xmpPrefix = "http://ns.adobe.com/xap/1.0/\x00"

type jpegHeaderSegment struct {
	marker              byte
	start, end, payload int
}

// jpegHeader visits marker segments before the first scan.
func jpegHeader(data []byte, visit func(jpegHeaderSegment) bool) bool {
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xd8 {
		return false
	}
	for pos := 2; pos+3 < len(data); {
		start := pos
		if data[pos] != 0xff {
			return false
		}
		for pos < len(data) && data[pos] == 0xff {
			pos++
		}
		if pos+3 > len(data) {
			return false
		}
		marker, size := data[pos], int(binary.BigEndian.Uint16(data[pos+1:]))
		if size < 2 || size > len(data)-pos-1 || marker == 0 || marker == 0xd8 || marker == 0xd9 ||
			marker == 1 || (marker >= 0xd0 && marker <= 0xd7) {
			return false
		}
		if marker == 0xda {
			return true
		}
		end := pos + 1 + size
		if !visit(jpegHeaderSegment{marker, start, end, pos + 3}) {
			return false
		}
		pos = end
	}
	return false
}

type gainMapIndex struct {
	segment                                 jpegHeaderSegment
	order                                   binary.ByteOrder
	entries, primaryEnd, gainStart, gainEnd int
	recoveredPrimaryEnd                     bool
}

func readGainMapIndex(data []byte, segment jpegHeaderSegment) (gainMapIndex, bool) {
	raw := data[segment.payload+4 : segment.end]
	index := gainMapIndex{segment: segment}
	if len(raw) < 14 {
		return index, false
	}
	switch string(raw[:4]) {
	case "II*\x00":
		index.order = binary.LittleEndian
	case "MM\x00*":
		index.order = binary.BigEndian
	default:
		return index, false
	}
	order := index.order
	offset := uint64(order.Uint32(raw[4:]))
	if offset < 8 || offset > uint64(len(raw)-6) {
		return index, false
	}
	dir := raw[int(offset):]
	n := int(order.Uint16(dir))
	if n != 3 || len(dir) < 2+12*n+4 || order.Uint32(dir[2+12*n:]) != 0 {
		return index, false
	}
	var entries int
	var version, count bool
	for p := dir[2 : 2+12*n]; len(p) != 0; p = p[12:] {
		switch order.Uint16(p) {
		case 0xb000:
			if version || order.Uint16(p[2:]) != 7 || order.Uint32(p[4:]) != 4 || string(p[8:12]) != "0100" {
				return index, false
			}
			version = true
		case 0xb001:
			if count || order.Uint16(p[2:]) != 4 || order.Uint32(p[4:]) != 1 || order.Uint32(p[8:]) != 2 {
				return index, false
			}
			count = true
		case 0xb002:
			entryOffset := uint64(order.Uint32(p[8:]))
			if entries != 0 || order.Uint16(p[2:]) != 7 || order.Uint32(p[4:]) != 32 ||
				entryOffset < offset+uint64(2+12*n+4) || entryOffset+32 > uint64(len(raw)) {
				return index, false
			}
			entries = int(entryOffset)
		default:
			return index, false
		}
	}
	if !version || !count || entries == 0 {
		return index, false
	}
	mp := raw[entries : entries+32]
	if order.Uint32(mp[8:]) != 0 || order.Uint32(mp[12:]) != 0 || order.Uint32(mp[28:]) != 0 {
		return index, false
	}
	primaryEnd := uint64(order.Uint32(mp[4:]))
	gainStart := uint64(segment.payload+4) + uint64(order.Uint32(mp[24:]))
	gainEnd := gainStart + uint64(order.Uint32(mp[20:]))
	if gainStart < uint64(segment.end+2) || gainEnd > uint64(len(data)) || gainEnd-gainStart < 4 ||
		!bytes.HasSuffix(data[gainStart:gainEnd], []byte{0xff, 0xd9}) {
		return index, false
	}
	endsImage := func(end uint64) bool {
		return end >= uint64(segment.end+2) && end <= gainStart && bytes.HasSuffix(data[:end], []byte{0xff, 0xd9})
	}
	if !endsImage(primaryEnd) {
		// Some camera files have a stale MPF primary size. Recover an
		// adjacent boundary only when the XMP directory also confirms it.
		if !endsImage(gainStart) {
			return index, false
		}
		primaryEnd = gainStart
		index.recoveredPrimaryEnd = true
	}
	index.entries = 4 + segment.payload - segment.start + entries
	index.primaryEnd, index.gainStart, index.gainEnd = int(primaryEnd), int(gainStart), int(gainEnd)
	return index, true
}

func gainMapXMP(data []byte, index gainMapIndex, fileSize int) (directory, version, valid bool) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var items [2]map[string]string
	depth, directoryDepth, count := 0, 0, 0
	for {
		token, err := decoder.Token()
		if err != nil {
			if err != io.EOF || depth != 0 {
				return false, false, false
			}
			break
		}
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth > 32 {
				return false, false, false
			}
			if token.Name == (xml.Name{Space: containerNamespace, Local: "Directory"}) {
				if directory {
					return false, false, false
				}
				directory, directoryDepth = true, depth
			}
			attrs := make(map[string]string)
			for _, attr := range token.Attr {
				if attr.Name == (xml.Name{Space: hdrGainMapNamespace, Local: "Version"}) && attr.Value == "1.0" {
					version = true
				}
				if attr.Name.Space == containerItemNamespace && directoryDepth != 0 {
					if _, exists := attrs[attr.Name.Local]; exists {
						return false, false, false
					}
					attrs[attr.Name.Local] = attr.Value
				}
			}
			if len(attrs) != 0 {
				if count == len(items) {
					return false, false, false
				}
				items[count] = attrs
				count++
			}
		case xml.EndElement:
			if depth == directoryDepth {
				directoryDepth = 0
			}
			depth--
		}
	}
	if !directory {
		return false, version, true
	}
	matches := func(value string, expected int, optional bool) bool {
		if value == "" {
			return optional && expected == 0
		}
		n, err := strconv.ParseUint(value, 10, 32)
		return err == nil && n == uint64(expected)
	}
	a, b := items[0], items[1]
	valid = count == 2 && a["Semantic"] == "Primary" && b["Semantic"] == "GainMap" &&
		a["Mime"] == "image/jpeg" && b["Mime"] == "image/jpeg" && a["URI"] == "" && b["URI"] == "" &&
		matches(a["Length"], 0, true) && matches(a["Padding"], index.gainStart-index.primaryEnd, true) &&
		matches(b["Length"], index.gainEnd-index.gainStart, false) && matches(b["Padding"], fileSize-index.gainEnd, true)
	return directory, version, valid
}

// compressUltraHDR operates on a two-image JPEG. MPF is detached while the
// primary is compressed and reattached with offsets relative to its new location.
func compressUltraHDR(input []byte, check func([]byte, []byte) bool) []byte {
	if len(input) > 64<<20 {
		return nil
	}
	var mpf jpegHeaderSegment
	var primaryXMP [][]byte
	var primaryISO bool
	if !jpegHeader(input, func(s jpegHeaderSegment) bool {
		p := input[s.payload:s.end]
		switch {
		case s.marker == 0xe2 && bytes.HasPrefix(p, []byte("MPF\x00")):
			if mpf.end != 0 {
				return false
			}
			mpf = s
		case s.marker == 0xe2 && bytes.HasPrefix(p, []byte(isoGainMap)):
			primaryISO = true
		case s.marker == 0xe1 && bytes.HasPrefix(p, []byte(xmpPrefix)):
			primaryXMP = append(primaryXMP, p[len(xmpPrefix):])
		}
		return true
	}) || mpf.end == 0 {
		return nil
	}
	index, ok := readGainMapIndex(input, mpf)
	if !ok {
		return nil
	}
	var directory, primaryVersion bool
	for _, packet := range primaryXMP {
		hasDirectory, hasVersion, valid := gainMapXMP(packet, index, len(input))
		if !valid || (directory && hasDirectory) {
			return nil
		}
		directory = directory || hasDirectory
		primaryVersion = primaryVersion || hasVersion
	}
	if index.recoveredPrimaryEnd && !directory {
		return nil
	}
	gain := input[index.gainStart:index.gainEnd]
	var gainISO, gainVersion bool
	if !jpegHeader(gain, func(s jpegHeaderSegment) bool {
		p := gain[s.payload:s.end]
		if s.marker == 0xe2 && bytes.HasPrefix(p, []byte(isoGainMap)) && len(p) > len(isoGainMap)+4 {
			gainISO = true
		}
		if s.marker == 0xe1 && bytes.HasPrefix(p, []byte(xmpPrefix)) {
			_, version, valid := gainMapXMP(p[len(xmpPrefix):], index, len(input))
			gainVersion = gainVersion || version && valid
		}
		return true
	}) || !(primaryISO && gainISO || (directory || primaryVersion) && gainVersion) {
		return nil
	}
	primary := make([]byte, 0, index.primaryEnd-(mpf.end-mpf.start))
	primary = append(primary, input[:mpf.start]...)
	primary = append(primary, input[mpf.end:index.primaryEnd]...)
	assemble := func(candidate []byte) []byte {
		insert := 2
		apps := true
		if !jpegHeader(candidate, func(s jpegHeaderSegment) bool {
			apps = apps && s.marker >= 0xe0 && s.marker <= 0xef
			if apps {
				insert = s.end
			}
			return true
		}) {
			return nil
		}
		primaryEnd := len(candidate) + mpf.end - mpf.start
		tiffStart := insert + mpf.payload - mpf.start + 4
		out := make([]byte, 0, primaryEnd+len(input)-index.primaryEnd)
		out = append(out, candidate[:insert]...)
		out = append(out, input[mpf.start:mpf.end]...)
		index.order.PutUint32(out[insert+index.entries+4:], uint32(primaryEnd))
		index.order.PutUint32(out[insert+index.entries+24:], uint32(primaryEnd+index.gainStart-index.primaryEnd-tiffStart))
		out = append(out, candidate[insert:]...)
		return append(out, input[index.primaryEnd:]...)
	}
	var accepted, acceptedPrimary []byte
	var gate func([]byte, []byte) bool
	if check != nil {
		gate = func(_, candidate []byte) bool {
			full := assemble(candidate)
			if full == nil || len(full) >= len(input) || !check(input, full) {
				return false
			}
			accepted = full
			acceptedPrimary = candidate
			return true
		}
	}
	candidate, err := compressJPEG(primary, true, gate)
	if err != nil {
		return nil
	}
	if accepted != nil && bytes.Equal(acceptedPrimary, candidate) {
		return accepted
	}
	if config, err := jpeg.DecodeConfig(bytes.NewReader(candidate)); err == nil &&
		config.Width > 0 && config.Height > 0 && config.Width <= maxSearchPixels/config.Height {
		candidate = progressiveCandidate(candidate, config.Width, config.Height)
	}
	if len(candidate) >= len(primary) {
		return input
	}
	originalImage, originalErr := jpeg.Decode(bytes.NewReader(primary))
	candidateImage, candidateErr := jpeg.Decode(bytes.NewReader(candidate))
	if originalErr != nil || candidateErr != nil || !equalJPEG(originalImage, candidateImage) {
		return input
	}
	if full := assemble(candidate); full != nil {
		return full
	}
	return input
}
