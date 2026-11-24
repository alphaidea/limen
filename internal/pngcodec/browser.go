package pngcodec

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"image"
	"image/png"
	"unicode/utf8"
)

// BrowserMetadata contains metadata that can be carried across formats in PNG.
type BrowserMetadata struct {
	ICC, Exif, XMP                    []byte
	ColourPrimaries, TransferFunction byte
	MasteringDisplay, ContentLight    []byte // HEVC mdcv and clli payloads.
	PixelsPerMetre                    [2]uint32
}

// EncodeBrowser encodes pixels losslessly and embeds the supplied metadata.
func EncodeBrowser(source image.Image, metadata BrowserMetadata) ([]byte, error) {
	var output bytes.Buffer
	if err := png.Encode(&output, source); err != nil {
		return nil, err
	}
	data := output.Bytes()
	if len(metadata.ICC) == 0 && len(metadata.Exif) == 0 && len(metadata.XMP) == 0 &&
		metadata.TransferFunction == 0 && metadata.PixelsPerMetre == ([2]uint32{}) {
		return data, nil
	}

	const headerEnd = 8 + 12 + 13 // PNG signature and IHDR chunk.
	result := bytes.Clone(data[:headerEnd])
	if metadata.TransferFunction != 0 {
		if data[25] != 2 && data[25] != 6 {
			return nil, errors.New("png: cICP requires RGB pixels")
		}
		// HEVC decoding has already converted the samples to full-range RGB.
		result = appendAnimationChunk(result, "cICP", []byte{metadata.ColourPrimaries, metadata.TransferFunction, 0, 1})
		if mastering := metadata.MasteringDisplay; len(mastering) == 24 {
			var payload [24]byte
			// HEVC stores G, B, R. PNG stores R, G, B.
			copy(payload[:4], mastering[8:12])
			copy(payload[4:12], mastering[:8])
			copy(payload[12:], mastering[12:])
			result = appendAnimationChunk(result, "mDCV", payload[:])
		}
		if light := metadata.ContentLight; len(light) == 4 {
			const pngLuminanceScale = 10_000
			maximum := uint32(binary.BigEndian.Uint16(light)) * pngLuminanceScale
			average := uint32(binary.BigEndian.Uint16(light[2:])) * pngLuminanceScale
			payload := binary.BigEndian.AppendUint32(nil, maximum)
			payload = binary.BigEndian.AppendUint32(payload, average)
			result = appendAnimationChunk(result, "cLLI", payload)
		}
	}
	if len(metadata.ICC) != 0 {
		if (data[25] != 2 && data[25] != 6) || !ValidRGBProfile(metadata.ICC) {
			return nil, errors.New("png: invalid RGB ICC profile")
		}
		var profile bytes.Buffer
		profile.WriteString("ICC Profile\x00\x00")
		writer := zlib.NewWriter(&profile)
		if _, err := writer.Write(metadata.ICC); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		result = appendAnimationChunk(result, "iCCP", profile.Bytes())
	}
	if metadata.PixelsPerMetre != ([2]uint32{}) {
		density := binary.BigEndian.AppendUint32(nil, metadata.PixelsPerMetre[0])
		density = binary.BigEndian.AppendUint32(density, metadata.PixelsPerMetre[1])
		result = appendAnimationChunk(result, "pHYs", append(density, 1))
	}
	if len(metadata.Exif) != 0 {
		result = appendAnimationChunk(result, "eXIf", metadata.Exif)
	}
	if len(metadata.XMP) != 0 {
		if !utf8.Valid(metadata.XMP) || bytes.IndexByte(metadata.XMP, 0) >= 0 {
			return nil, errors.New("png: XMP must be UTF-8 without NUL bytes")
		}
		payload := append([]byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00"), metadata.XMP...)
		result = appendAnimationChunk(result, "iTXt", payload)
	}
	return append(result, data[headerEnd:]...), nil
}

// ValidRGBProfile checks the structural bounds of an RGB ICC profile.
func ValidRGBProfile(profile []byte) bool {
	if len(profile) < 132 || int(binary.BigEndian.Uint32(profile)) != len(profile) ||
		string(profile[16:20]) != "RGB " || string(profile[36:40]) != "acsp" {
		return false
	}
	count := uint64(binary.BigEndian.Uint32(profile[128:132]))
	tableEnd := 132 + 12*count
	if count == 0 || tableEnd > uint64(len(profile)) {
		return false
	}
	for entry := 132; entry < int(tableEnd); entry += 12 {
		offset := uint64(binary.BigEndian.Uint32(profile[entry+4:]))
		size := uint64(binary.BigEndian.Uint32(profile[entry+8:]))
		if offset%4 != 0 || offset < tableEnd || size < 8 || offset+size > uint64(len(profile)) {
			return false
		}
	}
	return true
}
