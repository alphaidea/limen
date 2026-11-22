// Package tiffcodec provides bounded, pixel-equivalent TIFF recompression.
package tiffcodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"

	"github.com/alphaidea/limen/internal/pixelcheck"
	"golang.org/x/image/tiff"
)

// IsTIFF recognises classic TIFF and BigTIFF byte-order signatures.
func IsTIFF(b []byte) bool {
	return len(b) >= 4 && (string(b[:4]) == "II*\x00" || string(b[:4]) == "MM\x00*" || string(b[:4]) == "II+\x00" || string(b[:4]) == "MM\x00+")
}

// Compress retains unsupported presentation structures unchanged. Supported
// single-page images use lossless DEFLATE with decoded-pixel verification.
func Compress(b []byte, preserve bool) ([]byte, error) {
	if preserve && IsTIFF(b) && len(b) >= 8 {
		return b, nil
	}
	source, err := decodeSinglePage(b, false)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return b, nil
	}
	best := b
	{
		var out bytes.Buffer
		if err := tiff.Encode(&out, source, &tiff.Options{Compression: tiff.Deflate}); err != nil {
			return nil, err
		}
		if out.Len() >= len(best) {
			return best, nil
		}
		decoded, err := tiff.Decode(bytes.NewReader(out.Bytes()))
		if err != nil {
			return nil, err
		}
		if pixelcheck.IdenticalPacked(source, decoded) {
			return out.Bytes(), nil
		}
		equal := decoded.Bounds() == source.Bounds()
		for y := 0; equal && y < source.Bounds().Dy(); y++ {
			for x := 0; x < source.Bounds().Dx(); x++ {
				r, g, bl, a := source.At(x, y).RGBA()
				rr, gg, bb, aa := decoded.At(x, y).RGBA()
				if r != rr || g != gg || bl != bb || a != aa {
					equal = false
					break
				}
			}
		}
		if equal {
			best = out.Bytes()
		}
	}
	return best, nil
}

// WebPSource returns a single page without resolution or presentation tags.
func WebPSource(b []byte) image.Image {
	source, _ := decodeSinglePage(b, true)
	return source
}

// BrowserSource returns a supported single page, allowing standard 72 DPI tags.
func BrowserSource(b []byte) image.Image {
	source, _ := decodeSinglePage(b, false)
	return source
}

func decodeSinglePage(b []byte, forWebP bool) (image.Image, error) {
	if !IsTIFF(b) || len(b) < 8 {
		return nil, errors.New("tiff: invalid header")
	}
	if len(b) > 64<<20 || b[2] == 43 || b[3] == 43 {
		return nil, nil
	}
	var order binary.ByteOrder = binary.LittleEndian
	if b[0] == 'M' {
		order = binary.BigEndian
	}
	offset := uint64(order.Uint32(b[4:]))
	if offset < 8 || offset+2 > uint64(len(b)) {
		return nil, errors.New("tiff: invalid directory")
	}
	count := uint64(order.Uint16(b[offset:]))
	end := offset + 2 + 12*count
	if end+4 > uint64(len(b)) {
		return nil, errors.New("tiff: truncated directory")
	}
	if order.Uint32(b[end:]) != 0 {
		return nil, nil
	}
	seen := map[uint16]bool{}
	for p := offset + 2; p < end; p += 12 {
		tag := order.Uint16(b[p:])
		if seen[tag] {
			return nil, nil
		}
		seen[tag] = true
		switch tag {
		case 256, 257, 258, 259, 262, 273, 277, 278, 279, 317, 320, 338:
		case 282, 283:
			if forWebP {
				return nil, nil
			}
			if order.Uint16(b[p+2:]) != 5 || order.Uint32(b[p+4:]) != 1 {
				return nil, nil
			}
			q := uint64(order.Uint32(b[p+8:]))
			if q+8 > uint64(len(b)) {
				return nil, errors.New("tiff: invalid resolution")
			}
			if uint64(order.Uint32(b[q:])) != 72*uint64(order.Uint32(b[q+4:])) || order.Uint32(b[q+4:]) == 0 {
				return nil, nil
			}
		case 296:
			if forWebP {
				return nil, nil
			}
			if order.Uint16(b[p+2:]) != 3 || order.Uint32(b[p+4:]) != 1 || order.Uint16(b[p+8:]) != 2 {
				return nil, nil
			}
		default:
			return nil, nil
		}
	}
	config, err := tiff.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > 65535 || config.Height > 65535 || uint64(config.Width)*uint64(config.Height) > 4_000_000 {
		return nil, nil
	}
	source, err := tiff.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return source, nil
}
