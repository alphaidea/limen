package avifcodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"

	"github.com/gen2brain/avif"
)

// IsAVIF recognises a bounded file-type box, including compatible brands.
func IsAVIF(input []byte) bool {
	return hasAVIFBrand(input, "avif") || hasAVIFBrand(input, "avis")
}

func hasAVIFBrand(input []byte, brand string) bool {
	if len(input) < 16 || string(input[4:8]) != "ftyp" {
		return false
	}
	size := uint64(binary.BigEndian.Uint32(input))
	if size < 16 || size > uint64(len(input)) || size%4 != 0 {
		return false
	}
	for offset := 8; offset+4 <= int(size); offset += 4 {
		if offset != 12 && string(input[offset:offset+4]) == brand {
			return true
		}
	}
	return false
}

// supported rejects unhandled presentation properties before decoding. Unknown
// boxes fail closed; this deliberately supports only simple static SDR files.
func supported(data []byte, depth int) bool {
	if depth > 5 {
		return false
	}
	for len(data) > 0 {
		if len(data) < 8 {
			return false
		}
		size := uint64(binary.BigEndian.Uint32(data))
		if size < 8 || size > uint64(len(data)) {
			return false
		}
		kind := string(data[4:8])
		payload := data[8:int(size)]
		switch kind {
		case "ftyp", "mdat", "hdlr", "iloc", "iinf", "ipma", "av1C", "free", "iref":
		case "pitm":
			if len(payload) < 6 || payload[0] > 1 || (payload[0] == 1 && len(payload) < 8) {
				return false
			}
		case "auxC":
			if len(payload) < 5 || string(payload[4:]) != "urn:mpeg:mpegB:cicp:systems:auxiliary:alpha\x00" {
				return false
			}
		case "meta":
			if len(payload) < 4 || !bytes.Equal(payload[:4], []byte{0, 0, 0, 0}) || !supported(payload[4:], depth+1) {
				return false
			}
		case "iprp", "ipco":
			if !supported(payload, depth+1) {
				return false
			}
		case "ispe":
			if len(payload) != 12 {
				return false
			}
			w, h := uint64(binary.BigEndian.Uint32(payload[4:8])), uint64(binary.BigEndian.Uint32(payload[8:]))
			if w == 0 || h == 0 || w > maxPixels/h {
				return false
			}
		case "pixi":
			if len(payload) < 5 || int(payload[4])+5 != len(payload) {
				return false
			}
			for _, bits := range payload[5:] {
				if bits != 8 {
					return false
				}
			}
		case "colr":
			if len(payload) != 11 || string(payload[:4]) != "nclx" {
				return false
			}
			primaries, transfer := binary.BigEndian.Uint16(payload[4:6]), binary.BigEndian.Uint16(payload[6:8])
			if (primaries != 1 && primaries != 2) || (transfer != 13 && transfer != 2) {
				return false
			}
		default:
			return false
		}
		data = data[int(size):]
	}
	return true
}

// Compress keeps the original unless an encoded-file quality gate accepts a
// smaller candidate. Unsupported colour, alpha and presentation structures fall back.
func Compress(input []byte, preserve bool, check func([]byte, []byte) bool) ([]byte, error) {
	if !IsAVIF(input) || len(input) > 64<<20 {
		return nil, errors.New("limen: invalid or oversized AVIF")
	}
	if preserve || check == nil {
		return input, nil
	}
	if hasAVIFBrand(input, "avis") {
		return compressAnimation(input, check), nil
	}
	if !supported(input, 0) {
		return input, nil
	}
	config, err := avif.DecodeConfig(bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxPixels/config.Height {
		return input, nil
	}
	source, err := avif.Decode(bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	best := input
	accept := func(candidate []byte) bool {
		if len(candidate) >= len(best) || !check(input, candidate) {
			return false
		}
		best = candidate
		return true
	}
	baseline, err := Encode(source, func(_ image.Image, candidate []byte) bool { return accept(candidate) })
	if err != nil {
		return input, nil
	}
	accept(baseline)
	return best, nil
}
