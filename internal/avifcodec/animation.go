package avifcodec

import (
	"bytes"
	"encoding/binary"
	"image"
	"math"

	"github.com/gen2brain/avif"
)

const maxAnimationFrames = 256
const maxAnimationPixels = 16_000_000

type animationBox struct {
	kind string
	data []byte
}

func animationBoxes(data []byte, allowed ...string) []animationBox {
	var boxes []animationBox
	seen := make(map[string]bool)
	for len(data) != 0 {
		if len(data) < 8 || len(boxes) >= 256 {
			return nil
		}
		kind := string(data[4:8])
		known := false
		for _, name := range allowed {
			if kind == name {
				known = true
				break
			}
		}
		n := uint64(binary.BigEndian.Uint32(data))
		if !known || n < 8 || n > uint64(len(data)) ||
			(seen[kind] && kind != "trak" && kind != "infe" && kind != "free" && kind != "skip") {
			return nil
		}
		seen[kind] = true
		boxes = append(boxes, animationBox{kind, data[8:int(n)]})
		data = data[int(n):]
	}
	return boxes
}

func animationChild(boxes []animationBox, kind string) []byte {
	var data []byte
	for _, box := range boxes {
		if box.kind == kind {
			if data != nil {
				return nil
			}
			data = box.data
		}
	}
	return data
}

type animationClock struct {
	scale    uint32
	duration uint64
}

func mediaClock(data []byte) (animationClock, []byte) {
	if len(data) < 20 || data[0] > 1 || data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return animationClock{}, nil
	}
	start := 12 + 8*int(data[0])
	size := 8 + 4*int(data[0])
	if len(data) < start+size {
		return animationClock{}, nil
	}
	fields := data[start : start+size]
	c := animationClock{scale: binary.BigEndian.Uint32(fields)}
	if data[0] == 1 {
		c.duration = binary.BigEndian.Uint64(fields[4:])
	} else {
		c.duration = uint64(binary.BigEndian.Uint32(fields[4:]))
		if c.duration == math.MaxUint32 {
			c.duration = math.MaxUint64
		}
	}
	return c, fields
}

type animationRun struct {
	count, ticks uint32
}

type animationTrack struct {
	id, target uint32
	canvas     image.Point
	clock      animationClock
	duration   uint64
	segment    uint64
	repeating  bool
	count      int
	runs       []animationRun
	firstChunk uint64
	firstSize  uint32

	clockFields, durationField, edits, stts []byte
}

var identityMatrix = []byte{
	0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0, 0, 0,
}

func readAnimationTrack(data []byte, movie animationClock) (animationTrack, bool) {
	var track animationTrack
	boxes := animationBoxes(data, "tkhd", "tref", "edts", "mdia", "free")
	tkhd := animationChild(boxes, "tkhd")
	if len(tkhd) < 84 || tkhd[0] > 1 || tkhd[3]&1 == 0 {
		return track, false
	}
	version := int(tkhd[0])
	durationOffset, matrixOffset := 20+8*version, 40+12*version
	if len(tkhd) != matrixOffset+44 || !bytes.Equal(tkhd[matrixOffset:matrixOffset+36], identityMatrix) {
		return track, false
	}
	track.id = binary.BigEndian.Uint32(tkhd[12+8*version:])
	w, h := binary.BigEndian.Uint32(tkhd[matrixOffset+36:]), binary.BigEndian.Uint32(tkhd[matrixOffset+40:])
	if track.id == 0 || w&0xffff != 0 || h&0xffff != 0 {
		return track, false
	}
	track.canvas = image.Pt(int(w>>16), int(h>>16))
	if track.canvas.X <= 0 || track.canvas.Y <= 0 || track.canvas.X > maxPixels/track.canvas.Y {
		return track, false
	}
	track.durationField = tkhd[durationOffset : durationOffset+4+4*version]
	if version == 1 {
		track.duration = binary.BigEndian.Uint64(track.durationField)
	} else {
		track.duration = uint64(binary.BigEndian.Uint32(track.durationField))
		if track.duration == math.MaxUint32 {
			track.duration = math.MaxUint64
		}
	}
	track.segment = track.duration
	if edts := animationChild(boxes, "edts"); edts != nil {
		edit := animationChild(animationBoxes(edts, "elst"), "elst")
		if len(edit) < 20 || edit[0] > 1 || edit[1] != 0 || edit[2] != 0 || edit[3] > 1 ||
			binary.BigEndian.Uint32(edit[4:]) != 1 {
			return track, false
		}
		n := 4 + 4*int(edit[0])
		if len(edit) != 12+2*n || !bytes.Equal(edit[8+n:8+2*n], make([]byte, n)) ||
			binary.BigEndian.Uint32(edit[8+2*n:]) != 0x10000 {
			return track, false
		}
		track.repeating = edit[3] == 1
		if n == 8 {
			track.segment = binary.BigEndian.Uint64(edit[8:])
		} else {
			track.segment = uint64(binary.BigEndian.Uint32(edit[8:]))
		}
		track.edits = edit
	}
	if track.segment == 0 || track.segment > math.MaxUint32 || track.duration != movie.duration ||
		(!track.repeating && track.duration != track.segment) ||
		(track.repeating && track.duration != math.MaxUint64 && (track.duration < track.segment || track.duration%track.segment != 0)) {
		return track, false
	}
	mdia := animationBoxes(animationChild(boxes, "mdia"), "mdhd", "hdlr", "minf")
	track.clock, track.clockFields = mediaClock(animationChild(mdia, "mdhd"))
	if track.clock.scale == 0 || track.clock.duration == 0 || track.clock.duration > math.MaxUint32 ||
		track.clock.duration*uint64(movie.scale) != track.segment*uint64(track.clock.scale) {
		return track, false
	}
	handler := animationChild(mdia, "hdlr")
	if len(handler) < 12 {
		return track, false
	}
	switch string(handler[8:12]) {
	case "pict":
		if animationChild(boxes, "tref") != nil {
			return track, false
		}
	case "auxv":
		aux := animationChild(animationBoxes(animationChild(boxes, "tref"), "auxl"), "auxl")
		if len(aux) != 4 {
			return track, false
		}
		track.target = binary.BigEndian.Uint32(aux)
		if track.target == 0 {
			return track, false
		}
	default:
		return track, false
	}
	minf := animationBoxes(animationChild(mdia, "minf"), "vmhd", "dinf", "stbl")
	dref := animationChild(animationBoxes(animationChild(minf, "dinf"), "dref"), "dref")
	if len(dref) != 20 || !bytes.Equal(dref, []byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 12, 'u', 'r', 'l', ' ', 0, 0, 0, 1}) {
		return track, false
	}
	stbl := animationBoxes(animationChild(minf, "stbl"), "stsd", "stts", "stsc", "stsz", "stco", "co64", "stss", "sdtp")
	stsd := animationChild(stbl, "stsd")
	if len(stsd) < 8 || binary.BigEndian.Uint64(stsd) != 1 {
		return track, false
	}
	entry := animationChild(animationBoxes(stsd[8:], "av01"), "av01")
	if len(entry) < 78 || binary.BigEndian.Uint16(entry[6:]) != 1 ||
		image.Pt(int(binary.BigEndian.Uint16(entry[24:])), int(binary.BigEndian.Uint16(entry[26:]))) != track.canvas {
		return track, false
	}
	properties := animationBoxes(entry[78:], "av1C", "colr", "ccst", "auxi")
	config := animationChild(properties, "av1C")
	if len(config) < 4 || config[0] != 0x81 || config[2]&0x60 != 0 || (config[2]&0x10 != 0) != (track.target != 0) {
		return track, false
	}
	if colr := animationChild(properties, "colr"); colr != nil {
		if len(colr) != 11 || string(colr[:4]) != "nclx" {
			return track, false
		}
		primaries, transfer := binary.BigEndian.Uint16(colr[4:]), binary.BigEndian.Uint16(colr[6:])
		if (primaries != 1 && primaries != 2) || (transfer != 2 && transfer != 13) {
			return track, false
		}
	} else if track.target == 0 {
		return track, false
	}
	if track.target != 0 {
		if !bytes.Equal(animationChild(properties, "auxi"), []byte("\x00\x00\x00\x00urn:mpeg:mpegB:cicp:systems:auxiliary:alpha\x00")) {
			return track, false
		}
	}
	track.stts = animationChild(stbl, "stts")
	if len(track.stts) < 16 || len(track.stts) > 8+maxAnimationFrames*8 || binary.BigEndian.Uint32(track.stts) != 0 ||
		uint64(len(track.stts)) != 8+8*uint64(binary.BigEndian.Uint32(track.stts[4:])) {
		return track, false
	}
	var duration uint64
	for r := track.stts[8:]; len(r) > 0; r = r[8:] {
		count, ticks := binary.BigEndian.Uint32(r), binary.BigEndian.Uint32(r[4:])
		if count == 0 || ticks == 0 || count > maxAnimationFrames || track.count+int(count) > maxAnimationFrames {
			return track, false
		}
		track.count += int(count)
		duration += uint64(ticks) * uint64(count)
		if i := len(track.runs) - 1; i >= 0 && track.runs[i].ticks == ticks {
			track.runs[i].count += count
		} else {
			track.runs = append(track.runs, animationRun{count, ticks})
		}
	}
	if duration != track.clock.duration {
		return track, false
	}
	stsz := animationChild(stbl, "stsz")
	if len(stsz) < 12 || binary.BigEndian.Uint32(stsz) != 0 || binary.BigEndian.Uint32(stsz[8:]) != uint32(track.count) {
		return track, false
	}
	track.firstSize = binary.BigEndian.Uint32(stsz[4:])
	if track.firstSize == 0 {
		if len(stsz) != 12+4*track.count {
			return track, false
		}
		track.firstSize = binary.BigEndian.Uint32(stsz[12:])
	}
	stco, co64 := animationChild(stbl, "stco"), animationChild(stbl, "co64")
	if stco != nil && co64 == nil && len(stco) >= 12 {
		track.firstChunk = uint64(binary.BigEndian.Uint32(stco[8:]))
	} else if stco == nil && len(co64) >= 16 {
		track.firstChunk = binary.BigEndian.Uint64(co64[8:])
	} else {
		return track, false
	}
	return track, track.firstSize != 0 && track.firstChunk != 0
}

type animationLayout struct {
	movie         animationClock
	movieFields   []byte
	colour, alpha animationTrack
}

func readAnimation(input []byte) (animationLayout, bool) {
	var layout animationLayout
	top := animationBoxes(input, "ftyp", "meta", "moov", "mdat", "free", "skip")
	movie := animationBoxes(animationChild(top, "moov"), "mvhd", "trak", "free")
	layout.movie, layout.movieFields = mediaClock(animationChild(movie, "mvhd"))
	if layout.movie.scale == 0 {
		return layout, false
	}
	for _, box := range movie {
		if box.kind != "trak" {
			continue
		}
		track, ok := readAnimationTrack(box.data, layout.movie)
		if !ok {
			return layout, false
		}
		if track.target == 0 && layout.colour.id == 0 {
			layout.colour = track
		} else if track.target != 0 && layout.alpha.id == 0 {
			layout.alpha = track
		} else {
			return layout, false
		}
	}
	colour, alpha := layout.colour, layout.alpha
	if colour.id == 0 || colour.count < 2 || colour.canvas.X*colour.canvas.Y > maxAnimationPixels/colour.count {
		return layout, false
	}
	if alpha.id != 0 {
		if alpha.id == colour.id || alpha.target != colour.id || alpha.canvas != colour.canvas || alpha.repeating != colour.repeating ||
			alpha.segment != colour.segment || alpha.clock != colour.clock || len(alpha.runs) != len(colour.runs) {
			return layout, false
		}
		for i, run := range colour.runs {
			if alpha.runs[i] != run {
				return layout, false
			}
		}
	}
	if meta := animationChild(top, "meta"); meta != nil && !animationPoster(meta, colour, alpha) {
		return layout, false
	}
	return layout, true
}

// The optional primary and alpha items must point to the same first samples
// as the tracks, so the fallback still cannot be silently replaced.
func animationPoster(meta []byte, colour, alpha animationTrack) bool {
	if len(meta) < 4 || binary.BigEndian.Uint32(meta) != 0 || !supported(meta[4:], 1) {
		return false
	}
	boxes := animationBoxes(meta[4:], "hdlr", "pitm", "iloc", "iinf", "iref", "iprp", "free")
	pitm, loc, iinf := animationChild(boxes, "pitm"), animationChild(boxes, "iloc"), animationChild(boxes, "iinf")
	count := 1
	if alpha.id != 0 {
		count++
	}
	if len(pitm) != 6 || binary.BigEndian.Uint32(pitm) != 0 ||
		len(loc) != 8+14*count || !bytes.Equal(loc[:6], []byte{0, 0, 0, 0, 0x44, 0}) ||
		int(binary.BigEndian.Uint16(loc[6:])) != count || len(iinf) < 6 || binary.BigEndian.Uint32(iinf) != 0 ||
		int(binary.BigEndian.Uint16(iinf[4:])) != count {
		return false
	}
	primary := binary.BigEndian.Uint16(pitm[4:])
	handler := animationChild(boxes, "hdlr")
	if len(handler) < 12 || string(handler[8:12]) != "pict" {
		return false
	}
	info := animationBoxes(iinf[6:], "infe")
	if len(info) != count {
		return false
	}
	ids := make(map[uint16]bool, count)
	for _, entry := range info {
		p := entry.data
		if len(p) < 13 || p[0] != 2 || binary.BigEndian.Uint16(p[6:]) != 0 || string(p[8:12]) != "av01" {
			return false
		}
		id := binary.BigEndian.Uint16(p[4:])
		if id == 0 || ids[id] {
			return false
		}
		ids[id] = true
	}
	ref := animationChild(boxes, "iref")
	if count == 1 && ref != nil {
		return false
	}
	if count == 2 {
		if len(ref) < 4 || binary.BigEndian.Uint32(ref) != 0 {
			return false
		}
		aux := animationChild(animationBoxes(ref[4:], "auxl"), "auxl")
		if len(aux) != 6 || binary.BigEndian.Uint16(aux[2:]) != 1 || binary.BigEndian.Uint16(aux[4:]) != primary {
			return false
		}
		id := binary.BigEndian.Uint16(aux)
		if !ids[id] || id == primary {
			return false
		}
	}
	seen := make(map[uint16]bool)
	for p := loc[8:]; len(p) != 0; p = p[14:] {
		id := binary.BigEndian.Uint16(p)
		if !ids[id] || seen[id] || binary.BigEndian.Uint32(p[2:]) != 1 {
			return false
		}
		seen[id] = true
		track := alpha
		if id == primary {
			track = colour
		}
		if uint64(binary.BigEndian.Uint32(p[6:])) != track.firstChunk || binary.BigEndian.Uint32(p[10:]) != track.firstSize {
			return false
		}
	}
	return seen[primary]
}

func applyAnimationTiming(from, to animationLayout) bool {
	if len(to.movieFields) != 12 {
		return false
	}
	binary.BigEndian.PutUint32(to.movieFields, from.movie.scale)
	binary.BigEndian.PutUint64(to.movieFields[4:], from.movie.duration)
	apply := func(source, output animationTrack) bool {
		if source.id == 0 || output.id == 0 {
			return source.id == output.id
		}
		if source.canvas != output.canvas || len(source.runs) != len(output.runs) ||
			len(output.clockFields) != 12 || len(output.durationField) != 8 || len(output.edits) != 28 ||
			len(output.stts) != 8+8*len(source.runs) {
			return false
		}
		for i, run := range source.runs {
			if run.count != output.runs[i].count {
				return false
			}
			binary.BigEndian.PutUint32(output.stts[12+8*i:], run.ticks)
		}
		binary.BigEndian.PutUint32(output.clockFields, source.clock.scale)
		binary.BigEndian.PutUint64(output.clockFields[4:], source.clock.duration)
		binary.BigEndian.PutUint64(output.durationField, source.duration)
		binary.BigEndian.PutUint64(output.edits[8:], source.segment)
		output.edits[3] = 0
		if source.repeating {
			output.edits[3] = 1
		}
		return true
	}
	alpha := from.alpha
	if alpha.id == 0 && to.alpha.id != 0 {
		// The backend can emit an alpha track for opaque input. The decoded
		// candidate must still match the original alpha plane for every frame.
		alpha = from.colour
	}
	return apply(from.colour, to.colour) && apply(alpha, to.alpha)
}

func matchesAnimation(decoded *avif.AVIF, layout animationLayout) bool {
	track := layout.colour
	if decoded == nil || len(decoded.Image) != track.count || len(decoded.Delay) != track.count {
		return false
	}
	frame := 0
	for _, run := range track.runs {
		delay := float64(run.ticks) / float64(track.clock.scale)
		for range run.count {
			img, ok := decoded.Image[frame].(*image.RGBA)
			if !ok || img.Bounds().Size() != track.canvas || math.IsNaN(decoded.Delay[frame]) ||
				math.Abs(decoded.Delay[frame]-delay) > 1e-12 {
				return false
			}
			frame++
		}
	}
	return true
}

func compressAnimation(input []byte, check func([]byte, []byte) bool) []byte {
	layout, ok := readAnimation(input)
	if !ok {
		return input
	}
	source, err := avif.DecodeAll(bytes.NewReader(input))
	if err != nil || !matchesAnimation(source, layout) {
		return input
	}
	// Distinct adjacent runs stay distinct through the millisecond encoder.
	// The actual rational clock and edit-list timing are restored afterwards.
	delays := make([]float64, 0, layout.colour.count)
	for i, run := range layout.colour.runs {
		for range run.count {
			delays = append(delays, float64(40+i%2)/1000)
		}
	}
	best := input
	for _, quality := range []int{98, 94, 90} {
		var out bytes.Buffer
		if err := avif.EncodeAll(&out, &avif.AVIF{Image: source.Image, Delay: delays, LoopCount: 1}, avif.Options{
			Quality: quality, QualityAlpha: 100, Speed: 7, ChromaSubsampling: image.YCbCrSubsampleRatio444,
		}); err != nil || out.Len() >= len(best) {
			continue
		}
		candidate := out.Bytes()
		output, ok := readAnimation(candidate)
		if !ok || !applyAnimationTiming(layout, output) {
			continue
		}
		output, ok = readAnimation(candidate)
		if !ok {
			continue
		}
		decoded, err := avif.DecodeAll(bytes.NewReader(candidate))
		if err != nil || !matchesAnimation(decoded, output) {
			continue
		}
		exactAlpha := true
		for i, img := range source.Image {
			if !equivalent(img, decoded.Image[i], false) {
				exactAlpha = false
				break
			}
		}
		if exactAlpha && check(input, candidate) {
			best = candidate
		}
	}
	return best
}
