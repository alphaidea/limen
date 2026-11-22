package heiccodec

import (
	"bytes"
	"encoding/binary"

	"github.com/gen2brain/h265/heic"
)

const maxLargeHEICPixels = 50_000_000
const maxLargeHEICCodedPixels = 64_000_000

func itemDimensions(table propertyTable, id uint32) (width, height uint64) {
	for _, index := range table.items[id] {
		if index&0x7fff == 0 {
			continue
		}
		p := table.properties[(index&0x7fff)-1]
		if string(p[4:8]) == "ispe" {
			if len(p) != 20 || width != 0 || binary.BigEndian.Uint32(p[8:]) != 0 {
				return 0, 0
			}
			width, height = uint64(binary.BigEndian.Uint32(p[12:])), uint64(binary.BigEndian.Uint32(p[16:]))
			if width == 0 || height == 0 {
				return 0, 0
			}
		}
	}
	return width, height
}

func largePrimary(meta []byte) bool {
	table, ok := readItemProperties(meta, true)
	if !ok {
		return false
	}
	width, height := itemDimensions(table, table.itemID)
	return height != 0 && width > maxCompressionPixels/height
}

func validPrimaryGrid(input, meta []byte, idat boxSpan, grid itemExtent, table propertyTable, tiles []uint32) bool {
	if grid.end == 0 {
		return false
	}
	data := input[grid.offset:grid.end]
	if grid.inMetadata {
		data = meta[idat.payload+grid.offset : idat.payload+grid.end]
	}
	if len(data) < 8 || data[0] != 0 {
		return false
	}
	rows, columns := uint64(data[2])+1, uint64(data[3])+1
	var width, height uint64
	switch {
	case data[1] == 0 && len(data) == 8:
		width, height = uint64(binary.BigEndian.Uint16(data[4:])), uint64(binary.BigEndian.Uint16(data[6:]))
	case data[1] == 1 && len(data) == 12:
		width, height = uint64(binary.BigEndian.Uint32(data[4:])), uint64(binary.BigEndian.Uint32(data[8:]))
	default:
		return false
	}
	w, h := itemDimensions(table, table.itemID)
	if width != w || height != h || width == 0 || height == 0 || width > maxLargeHEICPixels/height ||
		rows*columns != uint64(len(tiles)) {
		return false
	}
	tileWidth, tileHeight := itemDimensions(table, tiles[0])
	if tileWidth == 0 || tileHeight == 0 ||
		width <= (columns-1)*tileWidth || width > columns*tileWidth ||
		height <= (rows-1)*tileHeight || height > rows*tileHeight {
		return false
	}
	for _, tile := range tiles[1:] {
		w, h := itemDimensions(table, tile)
		if w != tileWidth || h != tileHeight {
			return false
		}
	}
	return true
}

func sameHEICConfig(input, candidate []byte) bool {
	a, err := heic.DecodeConfig(bytes.NewReader(input))
	if err != nil || a.Width <= 0 || a.Height <= 0 || a.Width > maxLargeHEICPixels/a.Height {
		return false
	}
	b, err := heic.DecodeConfig(bytes.NewReader(candidate))
	return err == nil && a == b
}
