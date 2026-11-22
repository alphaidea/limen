// Package jpegcodec compresses JPEG images in the quantised coefficient domain.
package jpegcodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
)

const maxPixels = 25_000_000

// The private APP15 segment records that visual optimisation has already run.
const visualMarker = "Limen\x00JPEG\x01"

var invalidJPEG = errors.New("limen: invalid JPEG")

type segment struct {
	marker byte
	data   []byte
}

type component struct {
	id, width, height, quantisation byte
	dc, ac                          byte
	luma                            bool
}

type scan struct {
	segments     []segment
	components   []component
	tables       [8]*huffman
	quantisation quantisation
	units        int
	restart      int
	start        int
	visual       bool
	progressive  bool
}

// Compress returns a smaller JPEG when its layout permits coefficient-domain
// optimisation. It retains APP segments and drops comments unless requested.
func Compress(input []byte, preserveMetadata bool, qualityCheck func(original, candidate []byte) bool) ([]byte, error) {
	if output := compressUltraHDR(input, qualityCheck); output != nil {
		return output, nil
	}
	return compressJPEG(input, preserveMetadata, qualityCheck)
}

func compressJPEG(input []byte, preserveMetadata bool, qualityCheck func(original, candidate []byte) bool) ([]byte, error) {
	config, err := jpeg.DecodeConfig(bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxPixels/config.Height {
		return nil, errors.New("limen: JPEG exceeds pixel limit")
	}

	layout, err := inspect(input, config.Width, config.Height)
	if err != nil {
		return nil, err
	}
	if layout == nil {
		// Validate uncommon layouts before returning their original encoding.
		if _, err := jpeg.Decode(bytes.NewReader(input)); err != nil {
			return nil, err
		}
		return input, nil
	}
	width, height := config.Width, config.Height
	if !layout.visual || width*height > maxSearchPixels {
		qualityCheck = nil
	}
	if layout.progressive {
		return layout.compressProgressive(input, width, height, preserveMetadata, qualityCheck)
	}
	output, err := layout.compress(input, preserveMetadata, qualityCheck)
	if err == nil && qualityCheck != nil && !bytes.Contains(output, []byte(visualMarker)) {
		output = progressiveCandidate(output, width, height)
	}
	return output, err
}

func inspect(input []byte, width, height int) (*scan, error) {
	layout := &scan{visual: true}
	var frame []component
	var jfif, adobe bool
	var transform byte
	for offset := 2; offset < len(input); {
		start := offset
		for offset < len(input) && input[offset] == 0xff {
			offset++
		}
		if start == offset || len(input)-offset < 3 {
			return nil, invalidJPEG
		}
		marker := input[offset]
		offset++
		length := int(binary.BigEndian.Uint16(input[offset:]))
		if length < 2 || length > len(input)-offset {
			return nil, invalidJPEG
		}
		data := input[offset+2 : offset+length]
		offset += length
		layout.segments = append(layout.segments, segment{marker, input[start:offset]})
		switch marker {
		case 0xc0, 0xc2:
			if len(frame) != 0 || len(data) < 6 || data[0] != 8 || len(data) != 6+int(data[5])*3 ||
				int(binary.BigEndian.Uint16(data[1:])) != height || int(binary.BigEndian.Uint16(data[3:])) != width {
				return nil, invalidJPEG
			}
			if data[5] != 1 && data[5] != 3 {
				return nil, nil
			}
			layout.progressive = marker == 0xc2
			var ids [256]bool
			for index := 6; index < len(data); index += 3 {
				part := component{id: data[index], width: data[index+1] >> 4, height: data[index+1] & 15, quantisation: data[index+2], luma: index == 6}
				if ids[part.id] || part.width == 0 || part.width > 4 || part.height == 0 || part.height > 4 || part.quantisation > 3 {
					return nil, invalidJPEG
				}
				ids[part.id] = true
				frame = append(frame, part)
			}
		case 0xc4:
			if !layout.readTables(data) {
				return nil, invalidJPEG
			}
		case 0xdb:
			for len(data) != 0 {
				if data[0]>>4 == 1 {
					return nil, nil
				}
				if len(data) < 65 || data[0] > 3 || bytes.IndexByte(data[1:65], 0) >= 0 {
					return nil, invalidJPEG
				}
				copy(layout.quantisation[data[0]][:], data[1:65])
				data = data[65:]
			}
		case 0xdd:
			if len(data) != 2 {
				return nil, invalidJPEG
			}
			layout.restart = int(binary.BigEndian.Uint16(data))
		case 0xda:
			if len(frame) == 0 {
				return nil, invalidJPEG
			}
			layout.start = offset
			layout.visual = layout.visual && (len(frame) == 1 ||
				(!adobe || transform == 1) && (jfif || frame[0].id == 1 && frame[1].id == 2 && frame[2].id == 3))
			if layout.progressive {
				if len(frame) == 1 {
					frame[0].width, frame[0].height = 1, 1
				}
				layout.components = frame
				return layout, nil
			}
			if len(data) < 6 || len(data) != 4+2*int(data[0]) ||
				data[len(data)-3] != 0 || data[len(data)-2] != 63 || data[len(data)-1] != 0 {
				return nil, invalidJPEG
			}
			if int(data[0]) != len(frame) {
				return nil, nil
			}
			if !layout.setComponents(frame, data[1:len(data)-3], width, height) {
				return nil, invalidJPEG
			}
			return layout, nil
		case 0xe0:
			if bytes.HasPrefix(data, []byte("JFIF\x00")) {
				jfif = true
			} else if !bytes.HasPrefix(data, []byte("JFXX\x00")) {
				return nil, nil
			}
		case 0xe1:
			if !bytes.HasPrefix(data, []byte("Exif\x00\x00")) &&
				!bytes.HasPrefix(data, []byte("http://ns.adobe.com/xap/1.0/\x00")) &&
				!bytes.HasPrefix(data, []byte("http://ns.adobe.com/xmp/extension/\x00")) {
				return nil, nil
			}
		case 0xe2:
			if bytes.HasPrefix(data, []byte(isoGainMap)) {
				continue
			}
			if !bytes.HasPrefix(data, []byte("ICC_PROFILE\x00")) {
				// MPF and similar formats can reference absolute offsets.
				return nil, nil
			}
			layout.visual = false
		case 0xec:
			if !bytes.HasPrefix(data, []byte("Ducky")) {
				return nil, nil
			}
		case 0xee:
			if len(data) < 12 || !bytes.HasPrefix(data, []byte("Adobe")) {
				return nil, nil
			}
			adobe, transform = true, data[11]
		case 0xef:
			if string(data) != visualMarker {
				return nil, nil
			}
			layout.visual = false
		case 0xfe:
		default:
			return nil, nil
		}
	}
	return nil, invalidJPEG
}

func (layout *scan) readTables(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for len(data) != 0 {
		if len(data) < 17 || data[0]>>4 > 1 || data[0]&15 > 3 {
			return false
		}
		id := int(data[0]>>4)*4 + int(data[0]&15)
		table := new(huffman)
		copy(table.counts[:], data[1:17])
		data = data[17:]
		size := 0
		for _, count := range table.counts {
			size += int(count)
		}
		if size == 0 || size > 256 || size > len(data) {
			return false
		}
		table.values = data[:size]
		if !table.build() {
			return false
		}
		layout.tables[id] = table
		data = data[size:]
	}
	return true
}

func (layout *scan) setComponents(frame []component, selectors []byte, width, height int) bool {
	maxWidth, maxHeight, blocks := 1, 1, 0
	var used [256]bool
	for _, part := range frame {
		maxWidth = max(maxWidth, int(part.width))
		maxHeight = max(maxHeight, int(part.height))
		blocks += int(part.width) * int(part.height)
	}
	if len(frame) == 1 {
		maxWidth, maxHeight = 1, 1
	} else if blocks > 10 {
		return false
	}
	for i := 0; i < len(selectors); i += 2 {
		id, table := selectors[i], selectors[i+1]
		if used[id] || table>>4 > 1 || table&15 > 1 {
			return false
		}
		for _, part := range frame {
			if part.id != id {
				continue
			}
			part.dc, part.ac = table>>4, 4+table&15
			if layout.tables[part.dc] == nil || layout.tables[part.ac] == nil || layout.quantisation[part.quantisation][0] == 0 {
				return false
			}
			if len(frame) == 1 {
				part.width, part.height = 1, 1
			}
			layout.components = append(layout.components, part)
			used[id] = true
			break
		}
		if !used[id] {
			return false
		}
	}
	layout.units = ((width + 8*maxWidth - 1) / (8 * maxWidth)) * ((height + 8*maxHeight - 1) / (8 * maxHeight))
	return true
}

// WebPSource accepts single-scan SDR images with no presentation metadata.
func WebPSource(input []byte) image.Image {
	config, err := jpeg.DecodeConfig(bytes.NewReader(input))
	if err != nil || config.Height <= 0 || config.Width > maxSearchPixels/config.Height {
		return nil
	}
	layout, err := inspect(input, config.Width, config.Height)
	if err != nil || layout == nil || !layout.visual || layout.progressive {
		return nil
	}
	for _, part := range layout.segments {
		if part.marker >= 0xe0 && part.marker <= 0xef {
			// A plain JFIF header describes square pixels without a thumbnail.
			if part.marker != 0xe0 || len(part.data) != 18 || !bytes.Equal(part.data[4:], []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00")) {
				return nil
			}
		}
	}
	// An additional scan, embedded image or trailer needs separate verification.
	for scan := input[layout.start:]; ; {
		index := bytes.IndexByte(scan, 0xff)
		if index < 0 || index+1 >= len(scan) {
			return nil
		}
		marker := scan[index+1]
		scan = scan[index+2:]
		if marker == 0xd9 && len(scan) == 0 {
			break
		}
		if marker != 0 && (marker < 0xd0 || marker > 0xd7) {
			return nil
		}
	}
	source, err := jpeg.Decode(bytes.NewReader(input))
	if err != nil {
		return nil
	}
	if _, ok := source.(*image.YCbCr); ok {
		rgb := image.NewRGBA(source.Bounds())
		draw.Draw(rgb, rgb.Bounds(), source, source.Bounds().Min, draw.Src)
		return rgb
	}
	return source
}
