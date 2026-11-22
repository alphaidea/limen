package heiccodec

import (
	"bytes"
	"encoding/binary"
)

type boxSpan struct {
	start, payload, end int
}

func nextBox(data []byte, offset int) (boxSpan, bool) {
	if len(data)-offset < 8 {
		return boxSpan{}, false
	}
	size := uint64(binary.BigEndian.Uint32(data[offset:]))
	header := 8
	if size == 1 && len(data)-offset >= 16 {
		size = binary.BigEndian.Uint64(data[offset+8:])
		header = 16
	}
	if size < uint64(header) || size > uint64(len(data)-offset) {
		return boxSpan{}, false
	}
	return boxSpan{offset, offset + header, offset + int(size)}, true
}

func findBox(data []byte, start int, kind string) (boxSpan, bool) {
	var found boxSpan
	for start < len(data) {
		box, ok := nextBox(data, start)
		if !ok {
			return boxSpan{}, false
		}
		if string(data[box.start+4:box.start+8]) == kind {
			if found.end != 0 {
				return boxSpan{}, false
			}
			found = box
		}
		start = box.end
	}
	return found, found.end != 0
}

func appendBox(data []byte, kind string, payload []byte) []byte {
	data = binary.BigEndian.AppendUint32(data, uint32(8+len(payload)))
	data = append(data, kind...)
	return append(data, payload...)
}

func replaceBox(data []byte, span boxSpan, payload []byte) []byte {
	out := make([]byte, 0, len(data)-(span.end-span.payload)+len(payload))
	out = append(out, data[:span.start]...)
	out = appendBox(out, string(data[span.start+4:span.start+8]), payload)
	return append(out, data[span.end:]...)
}

type itemProperty struct {
	box       []byte
	essential bool
}

type propertyTable struct {
	properties [][]byte
	primary    []uint16
	uses       map[int]int
	itemID     uint32
	items      map[uint32][]uint16
	order      []uint32
}

func readProperties(meta []byte) (propertyTable, bool) {
	return readItemProperties(meta, false)
}

func readItemProperties(meta []byte, allowAuxiliaryTransforms bool) (propertyTable, bool) {
	var table propertyTable
	pitm, ok := findBox(meta, 4, "pitm")
	if !ok {
		return table, false
	}
	var id uint32
	switch p := meta[pitm.payload:pitm.end]; len(p) {
	case 6:
		if p[0] != 0 {
			return table, false
		}
		id = uint32(binary.BigEndian.Uint16(p[4:]))
	case 8:
		if p[0] != 1 {
			return table, false
		}
		id = binary.BigEndian.Uint32(p[4:])
	default:
		return table, false
	}
	iprp, ok := findBox(meta, 4, "iprp")
	if !ok {
		return table, false
	}
	data := meta[iprp.payload:iprp.end]
	ipco, ok := findBox(data, 0, "ipco")
	if !ok {
		return table, false
	}
	for p := ipco.payload; p < ipco.end; {
		box, ok := nextBox(data[:ipco.end], p)
		if !ok || len(table.properties) == 32767 || box.payload-box.start != 8 {
			return table, false
		}
		table.properties = append(table.properties, data[box.start:box.end])
		p = box.end
	}
	ipma, ok := findBox(data, 0, "ipma")
	if !ok {
		return table, false
	}
	associations := data[ipma.payload:ipma.end]
	if len(associations) < 8 || associations[0] > 1 || associations[1] != 0 ||
		associations[2] != 0 || associations[3] > 1 {
		return table, false
	}
	count := binary.BigEndian.Uint32(associations[4:])
	if count == 0 || count > 256 {
		return table, false
	}
	table.itemID = id
	table.uses = make(map[int]int)
	table.items = make(map[uint32][]uint16)
	wide := associations[3] == 1
	idSize := 2 + 2*int(associations[0])
	associations = associations[8:]
	for range count {
		if len(associations) <= idSize {
			return table, false
		}
		item := uint32(binary.BigEndian.Uint16(associations))
		if idSize == 4 {
			item = binary.BigEndian.Uint32(associations)
		}
		n := int(associations[idSize])
		if _, exists := table.items[item]; exists || item == 0 {
			return table, false
		}
		table.order = append(table.order, item)
		table.items[item] = nil
		associations = associations[idSize+1:]
		for range n {
			if len(associations) == 0 || (wide && len(associations) < 2) {
				return table, false
			}
			index := uint16(associations[0]&0x7f) | uint16(associations[0]&0x80)<<8
			if wide {
				index = binary.BigEndian.Uint16(associations)
				associations = associations[1:]
			}
			associations = associations[1:]
			address := int(index & 0x7fff)
			if address > len(table.properties) {
				return table, false
			}
			table.items[item] = append(table.items[item], index)
			if address == 0 {
				continue
			}
			table.uses[address]++
			kind := string(table.properties[address-1][4:8])
			switch kind {
			case "irot", "imir", "mdcv", "clli":
				if item != id && !allowAuxiliaryTransforms {
					return table, false
				}
			}
			if item == id {
				table.primary = append(table.primary, index)
			}
		}
	}
	return table, len(associations) == 0 && len(table.primary) != 0
}

func (table propertyTable) presentation() ([]itemProperty, bool) {
	var properties []itemProperty
	seen := make(map[string]bool)
	for _, index := range table.primary {
		property := table.properties[(index&0x7fff)-1]
		kind := string(property[4:8])
		switch kind {
		case "irot", "imir", "mdcv", "clli":
			if seen[kind] || (kind == "irot" && seen["imir"]) {
				return nil, false
			}
			seen[kind] = true
			properties = append(properties, itemProperty{property, index&0x8000 != 0})
		}
	}
	return properties, true
}

// attachProperties adds primary item properties to a file produced by the
// pinned HEIC encoder and shifts its absolute iloc offsets by the box growth.
func attachProperties(file []byte, properties []itemProperty) []byte {
	metaSpan, ok := findBox(file, 0, "meta")
	if !ok {
		return nil
	}
	meta := file[metaSpan.payload:metaSpan.end]
	iprpSpan, ok := findBox(meta, 4, "iprp")
	if !ok {
		return nil
	}
	iprp := meta[iprpSpan.payload:iprpSpan.end]
	ipcoSpan, ok := findBox(iprp, 0, "ipco")
	if !ok {
		return nil
	}
	ipmaSpan, ok := findBox(iprp, 0, "ipma")
	if !ok || ipcoSpan.end > ipmaSpan.start {
		return nil
	}
	propertyData := iprp[ipcoSpan.payload:ipcoSpan.end]
	count := 0
	for p := 0; p < len(propertyData); {
		box, ok := nextBox(propertyData, p)
		if !ok {
			return nil
		}
		p = box.end
		count++
	}
	associationData := iprp[ipmaSpan.payload:ipmaSpan.end]
	// Encoder associations use 16-bit item IDs, 7-bit property indices and
	// put the primary image (ID 1) first.
	if len(associationData) < 11 || !bytes.Equal(associationData[:4], []byte{0, 0, 0, 0}) ||
		binary.BigEndian.Uint16(associationData[8:]) != 1 ||
		int(associationData[10])+11 > len(associationData) || count+len(properties) > 127 ||
		int(associationData[10])+len(properties) > 255 {
		return nil
	}
	updated := bytes.Clone(propertyData)
	n := int(associationData[10]) + 11
	associations := append(make([]byte, 0, len(associationData)+len(properties)), associationData[:n]...)
	associations[10] += byte(len(properties))
	for _, property := range properties {
		updated = append(updated, property.box...)
		count++
		index := byte(count)
		if property.essential {
			index |= 0x80
		}
		associations = append(associations, index)
	}
	associations = append(associations, associationData[n:]...)
	iprp = replaceBox(iprp, ipmaSpan, associations)
	iprp = replaceBox(iprp, ipcoSpan, updated)
	meta = replaceBox(meta, iprpSpan, iprp)
	ilocSpan, ok := findBox(meta, 4, "iloc")
	if !ok {
		return nil
	}
	loc := meta[ilocSpan.payload:ilocSpan.end]
	if len(loc) < 8 || !bytes.Equal(loc[:6], []byte{0, 0, 0, 0, 0x44, 0}) ||
		len(loc) != 8+14*int(binary.BigEndian.Uint16(loc[6:8])) {
		return nil
	}
	growth := uint32(len(meta) - (metaSpan.end - metaSpan.payload))
	for p := 8; p < len(loc); p += 14 {
		if !bytes.Equal(loc[p+2:p+6], []byte{0, 0, 0, 1}) {
			return nil
		}
		offset := binary.BigEndian.Uint32(loc[p+6:])
		if uint64(offset) < uint64(metaSpan.end) || uint64(offset)+uint64(growth) > 1<<32-1 {
			return nil
		}
		binary.BigEndian.PutUint32(loc[p+6:], offset+growth)
	}
	return replaceBox(file, metaSpan, meta)
}
