package heiccodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"

	"github.com/gen2brain/h265/heic"
)

const maxCompressionPixels = 16_000_000

// IsHEIC recognises a bounded file-type box, including compatible brands.
func IsHEIC(input []byte) bool {
	b := brands(input)
	for offset := 0; offset+4 <= len(b); offset += 4 {
		if offset == 4 {
			continue
		}
		switch string(b[offset : offset+4]) {
		case "heic", "heix", "heim", "heis", "hevc", "hevx", "hevm", "hevs":
			return true
		}
	}
	return false
}

// supported rejects unhandled presentation properties before decoding. Unknown
// boxes fail closed; sequence and unknown auxiliary properties are preserved.
func supported(data []byte, depth int) bool {
	if depth > 5 {
		return false
	}
	var primary, visible uint32
	seenMeta := false
	for len(data) > 0 {
		if len(data) < 8 {
			return false
		}
		size := uint64(binary.BigEndian.Uint32(data))
		header := 8
		kind := string(data[4:8])
		if size == 1 && kind == "ftyp" && depth == 0 && len(data) >= 16 {
			size, header = binary.BigEndian.Uint64(data[8:16]), 16
		}
		if size < uint64(header) || size > uint64(len(data)) {
			return false
		}
		payload := data[header:int(size)]
		switch kind {
		case "ftyp", "mdat", "hdlr", "iloc", "ipma", "hvcC", "free", "idat":
		case "iinf":
			if depth != 1 || visible != 0 {
				return false
			}
			visible = singlePrimary(payload)
			if visible == 0 {
				return false
			}
		case "iref":
			if !supportedReferences(payload) {
				return false
			}
		case "pitm":
			if depth != 1 || primary != 0 || len(payload) < 6 || payload[1] != 0 || payload[2] != 0 || payload[3] != 0 {
				return false
			}
			switch {
			case payload[0] == 0 && len(payload) == 6:
				primary = uint32(binary.BigEndian.Uint16(payload[4:]))
			case payload[0] == 1 && len(payload) == 8:
				primary = binary.BigEndian.Uint32(payload[4:])
			default:
				return false
			}
			if primary == 0 {
				return false
			}
		case "auxC":
			if len(payload) < 5 || string(payload[4:]) != "urn:mpeg:hevc:2015:auxid:1\x00" {
				return false
			}
		case "meta":
			if depth != 0 || seenMeta || len(payload) < 4 || !bytes.Equal(payload[:4], []byte{0, 0, 0, 0}) || !supported(payload[4:], depth+1) {
				return false
			}
			seenMeta = true
		case "iprp", "ipco":
			if !supported(payload, depth+1) {
				return false
			}
		case "ispe":
			if len(payload) != 12 {
				return false
			}
			w, h := uint64(binary.BigEndian.Uint32(payload[4:8])), uint64(binary.BigEndian.Uint32(payload[8:]))
			if w == 0 || h == 0 || w > maxCompressionPixels/h {
				return false
			}
		case "pixi":
			if len(payload) < 5 || int(payload[4])+5 != len(payload) {
				return false
			}
			for _, bits := range payload[5:] {
				if bits != 8 && bits != 10 && bits != 12 {
					return false
				}
			}
		case "colr":
			if len(payload) != 11 || string(payload[:4]) != "nclx" || payload[10]&0x7f != 0 {
				return false
			}
			primaries, transfer := binary.BigEndian.Uint16(payload[4:6]), binary.BigEndian.Uint16(payload[6:8])
			if !isSDR(heic.ColorInfo{Primaries: primaries, Transfer: transfer}) && !isHDR(primaries, transfer) {
				return false
			}
		case "irot", "imir":
			if depth != 3 || len(payload) != 1 || (kind == "irot" && payload[0] > 3) ||
				(kind == "imir" && payload[0] > 1) {
				return false
			}
		case "mdcv", "clli":
			if depth != 3 || (kind == "mdcv" && len(payload) != 24) || (kind == "clli" && len(payload) != 4) {
				return false
			}
		default:
			return false
		}
		data = data[int(size):]
	}
	switch depth {
	case 0:
		return seenMeta
	case 1:
		return primary != 0 && primary == visible
	default:
		return true
	}
}

// Compress keeps the original unless an encoded-file quality gate accepts a
// smaller candidate. Unsupported colour, alpha and presentation structures fall back.
func Compress(input []byte, preserve bool, check func([]byte, []byte) bool) ([]byte, error) {
	if !IsHEIC(input) || len(input) > 64<<20 {
		return nil, errors.New("limen: invalid or oversized HEIC")
	}
	if check == nil {
		return input, nil
	}
	if meta, ok := findBox(input, 0, "meta"); ok {
		metadata := input[meta.payload:meta.end]
		large := largePrimary(metadata)
		gainMap := bytes.Contains(metadata, []byte(appleGainMap)) || bytes.Contains(metadata, []byte("tmap"))
		if large || gainMap {
			candidate := compressBaseItems(input, meta, large, check)
			if large || len(candidate) < len(input) {
				return candidate, nil
			}
		}
	}
	if preserve || !supported(input, 0) {
		return input, nil
	}
	config, err := heic.DecodeConfig(bytes.NewReader(input))
	if errors.Is(err, heic.ErrUnsupported) {
		return input, nil
	}
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxCompressionPixels/config.Height {
		return input, nil
	}
	meta, ok := findBox(input, 0, "meta")
	if !ok {
		return input, nil
	}
	table, ok := readProperties(input[meta.payload:meta.end])
	if !ok {
		return input, nil
	}
	properties, ok := table.presentation()
	if !ok {
		return input, nil
	}
	if config.ColorModel == color.NRGBA64Model {
		return compressHDR(input, meta, table, config, check)
	}
	exif, err := heic.DecodeExif(bytes.NewReader(input))
	if err != nil && !errors.Is(err, heic.ErrNoExif) {
		return input, nil
	}
	var rawExif []byte
	if exif != nil && exif.Orientation != 1 {
		if exif.Orientation < 1 || exif.Orientation > 8 {
			return input, nil
		}
		rawExif, err = heic.RawExif(bytes.NewReader(input))
		if err != nil {
			return input, nil
		}
	}
	for _, property := range properties {
		if kind := string(property.box[4:8]); kind == "mdcv" || kind == "clli" {
			return input, nil
		}
	}
	decode := func(data []byte) (image.Image, heic.ColorInfo, error) {
		return heic.DecodeColor(bytes.NewReader(data), heic.Options{FrameSizeLimit: maxCompressionPixels, Threads: 2})
	}
	source, colour, err := decode(input)
	if err != nil {
		return nil, err
	}
	// The encoder emits 8-bit SDR. Some sources signal colour and bit depth
	// only in HEVC, so checking the container properties alone is insufficient.
	if _, ok := source.(*image.NRGBA); !ok || !isSDR(colour) {
		return input, nil
	}
	originalBounds := image.Pt(config.Width, config.Height)
	best := input
	accept := func(candidate []byte) bool {
		if len(candidate) >= len(best) {
			return false
		}
		config, err := heic.DecodeConfig(bytes.NewReader(candidate))
		if err != nil || image.Pt(config.Width, config.Height) != originalBounds {
			return false
		}
		decoded, candidateColour, err := decode(candidate)
		if err != nil || !isSDR(candidateColour) || !equivalent(source, decoded, false) || !check(input, candidate) {
			return false
		}
		best = candidate
		return true
	}
	for _, quality := range searchQualities {
		var candidate bytes.Buffer
		if err := heic.Encode(&candidate, source, heic.EncodeOptions{Quality: quality, Chroma: heic.Chroma444, Exif: rawExif}); err != nil {
			return input, nil
		}
		if candidate.Len() >= len(best) {
			continue
		}
		data := candidate.Bytes()
		if len(properties) != 0 {
			data = attachProperties(data, properties)
			if data == nil {
				return input, nil
			}
		}
		if accept(data) {
			break
		}
	}
	return best, nil
}

func isSDR(colour heic.ColorInfo) bool {
	return len(colour.ICCP) == 0 && (colour.Primaries == 1 || colour.Primaries == 2) &&
		(colour.Transfer == 13 || colour.Transfer == 2)
}
