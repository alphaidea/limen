package heiccodec

import (
	"bytes"
	"encoding/binary"
)

// singlePrimary permits one visible image plus hidden grid tiles, alpha and
// descriptive items. Other item types can affect how the file is presented.
func singlePrimary(data []byte) uint32 {
	if len(data) < 6 || data[0] > 1 || data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return 0
	}
	count := uint32(binary.BigEndian.Uint16(data[4:6]))
	if data[0] == 1 {
		if len(data) < 8 {
			return 0
		}
		count = binary.BigEndian.Uint32(data[4:8])
		data = data[8:]
	} else {
		data = data[6:]
	}
	if count == 0 || count > 256 {
		return 0
	}
	var visible uint32
	for range count {
		if len(data) < 21 || string(data[4:8]) != "infe" {
			return 0
		}
		size := uint64(binary.BigEndian.Uint32(data))
		if size < 21 || size > uint64(len(data)) {
			return 0
		}
		entry := data[:int(size)]
		itemType := 16
		id := uint32(binary.BigEndian.Uint16(entry[12:14]))
		switch entry[8] {
		case 2:
		case 3:
			itemType += 2
			id = binary.BigEndian.Uint32(entry[12:16])
		default:
			return 0
		}
		if len(entry) <= itemType+4 || entry[9] != 0 || entry[10] != 0 || entry[11]&^byte(1) != 0 ||
			entry[itemType-2] != 0 || entry[itemType-1] != 0 {
			return 0
		}
		_, extra, ok := bytes.Cut(entry[itemType+4:], []byte{0})
		if !ok {
			return 0
		}
		switch string(entry[itemType : itemType+4]) {
		case "hvc1", "grid":
			if entry[11] == 0 {
				if visible != 0 || id == 0 {
					return 0
				}
				visible = id
			}
		case "Exif":
		case "mime":
			contentType, _, ok := bytes.Cut(extra, []byte{0})
			if !ok || string(contentType) != "application/rdf+xml" {
				return 0
			}
		default:
			return 0
		}
		data = data[int(size):]
	}
	if len(data) != 0 {
		return 0
	}
	return visible
}

func supportedReferences(data []byte) bool {
	if len(data) < 4 || data[0] > 1 || data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return false
	}
	idSize := 2 + 2*int(data[0])
	data = data[4:]
	for len(data) >= 8+idSize+2 {
		size := uint64(binary.BigEndian.Uint32(data))
		count := int(binary.BigEndian.Uint16(data[8+idSize:]))
		if count == 0 || count > 256 || size != uint64(8+idSize+2+count*idSize) || size > uint64(len(data)) {
			return false
		}
		switch string(data[4:8]) {
		case "dimg", "auxl", "cdsc":
		default:
			return false
		}
		data = data[int(size):]
	}
	return len(data) == 0
}
