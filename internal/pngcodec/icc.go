package pngcodec

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"io"
)

// Recognise the Little CMS v4 sRGB matrix/TRC transform by its encoded
// parameters. The profile name and descriptive text are not colour data.
func isSRGBProfile(chunk []byte) bool {
	nameEnd := bytes.IndexByte(chunk, 0)
	if nameEnd < 1 || nameEnd > 79 || nameEnd+2 >= len(chunk) || chunk[nameEnd+1] != 0 {
		return false
	}
	compressed := bytes.NewReader(chunk[nameEnd+2:])
	reader, err := zlib.NewReader(compressed)
	if err != nil {
		return false
	}
	defer reader.Close()

	const maxProfileSize = 16 << 10
	profile, err := io.ReadAll(io.LimitReader(reader, maxProfileSize+1))
	if err != nil || compressed.Len() != 0 || len(profile) < 132 || len(profile) > maxProfileSize ||
		binary.BigEndian.Uint32(profile) != uint32(len(profile)) ||
		profile[8] != 4 || string(profile[12:24]) != "mntrRGB XYZ " ||
		string(profile[36:40]) != "acsp" || binary.BigEndian.Uint32(profile[64:68]) > 3 ||
		hex.EncodeToString(profile[68:80]) != "0000f6d6000100000000d32d" {
		return false
	}

	const toneCurve = "706172610000000000030000000266660000f2a700000d59000013d000000a5b"
	required := map[string]string{
		"wtpt": "58595a20000000000000f6d6000100000000d32d",
		"chad": "736633320000000000010c42000005defffff325000007930000fd90fffffba1fffffda2000003dc0000c06e",
		"rXYZ": "58595a200000000000006fa0000038f500000390",
		"gXYZ": "58595a2000000000000062970000b787000018d9",
		"bXYZ": "58595a20000000000000249f00000f840000b6c3",
		"rTRC": toneCurve,
		"gTRC": toneCurve,
		"bTRC": toneCurve,
	}
	const chromaticity = "6368726d00000000000300000000a3d70000547b00004ccd0000999a0000266600000f5c"
	count := binary.BigEndian.Uint32(profile[128:])
	if count < uint32(len(required)) || count > uint32(len(required)+3) || count > uint32((len(profile)-132)/12) {
		return false
	}
	tableEnd := 132 + 12*int(count)
	seen := make(map[string]bool, count)
	for entry := 132; entry < tableEnd; entry += 12 {
		tag := string(profile[entry : entry+4])
		offset := binary.BigEndian.Uint32(profile[entry+4:])
		size := binary.BigEndian.Uint32(profile[entry+8:])
		if seen[tag] || offset%4 != 0 || offset < uint32(tableEnd) || offset > uint32(len(profile)) ||
			size < 8 || size > uint32(len(profile))-offset {
			return false
		}
		seen[tag] = true
		payload := profile[offset : offset+size]
		switch tag {
		case "desc", "cprt":
			continue
		case "chrm":
			if hex.EncodeToString(payload) != chromaticity {
				return false
			}
		default:
			if expected, ok := required[tag]; !ok || hex.EncodeToString(payload) != expected {
				return false
			}
			delete(required, tag)
		}
	}
	return len(required) == 0
}
