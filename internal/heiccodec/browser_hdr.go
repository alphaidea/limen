package heiccodec

import (
	"bytes"

	"github.com/gen2brain/h265/heic"
	"github.com/gen2brain/h265/hevc"
)

// BrowserHDR contains colour codes and static metadata in HEVC representation.
type BrowserHDR struct {
	Primaries, Transfer            byte
	MasteringDisplay, ContentLight []byte
}

func browserHDR(input []byte, meta boxSpan, table propertyTable, colour heic.ColorInfo) *BrowserHDR {
	// Only matrices supported by the existing HDR decode path are accepted.
	if colour.Matrix != 9 && colour.Matrix != 1 && colour.Matrix != 6 {
		return nil
	}
	metadata := &BrowserHDR{Primaries: byte(colour.Primaries), Transfer: byte(colour.Transfer)}
	var configuration []byte
	for _, association := range table.primary {
		index := int(association & 0x7fff)
		property := table.properties[index-1]
		switch string(property[4:8]) {
		case "hvcC":
			if configuration != nil || table.uses[index] != 1 {
				return nil
			}
			configuration = property[8:]
		case "mdcv":
			metadata.MasteringDisplay = property[8:]
		case "clli":
			metadata.ContentLight = property[8:]
		}
	}
	// Each accepted still has one primary coded item whose SEI can be
	// inspected. Grid tiles and alternate presentations require separate checks.
	mdat, ok := findBox(input, 0, "mdat")
	if configuration == nil || !ok {
		return nil
	}
	metaData := input[meta.payload:meta.end]
	location, ok := findBox(metaData, 4, "iloc")
	if !ok {
		return nil
	}
	extents, primary := itemExtents(metaData[location.payload:location.end], mdat, table.itemID)
	if primary < 0 {
		return nil
	}
	item := extents[primary]
	sample, ok := readHDRSample(configuration, input[item.offset:item.end])
	if !ok {
		return nil
	}
	for _, group := range [][]hevc.NALUnit{sample.configSEI, sample.prefixSEI, sample.suffixSEI} {
		for _, nal := range group {
			if !metadata.mergeSEI(nal.RBSP) {
				return nil
			}
		}
	}
	return metadata
}

func (metadata *BrowserHDR) mergeSEI(data []byte) bool {
	// readHDRSample has already checked the size and type of every message.
	for len(data) > 1 {
		end := 2 + int(data[1])
		payload := data[2:end]
		switch data[0] {
		case 137:
			if metadata.MasteringDisplay != nil && !bytes.Equal(metadata.MasteringDisplay, payload) {
				return false
			}
			metadata.MasteringDisplay = payload
		case 144:
			if metadata.ContentLight != nil && !bytes.Equal(metadata.ContentLight, payload) {
				return false
			}
			metadata.ContentLight = payload
		case 147:
			if payload[0] != metadata.Transfer {
				return false
			}
		}
		data = data[end:]
	}
	return true
}
