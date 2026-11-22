package jpegcodec

import "math/bits"

// walkRefinement emits an AC refinement scan. Existing nonzero coefficients
// contribute correction bits. Newly nonzero coefficients use run/size codes.
func (plane progressivePlane) walkRefinement(shift uint, emit func(int, uint32, uint)) {
	const rawBits = -1
	var corrections [4096 + 64]byte
	pending, eob := 0, 0
	flush := func() {
		if eob == 0 {
			return
		}
		size := uint(bits.Len(uint(eob)) - 1)
		emit(int(size<<4), uint32(eob)-(1<<size), size)
		for _, bit := range corrections[:pending] {
			emit(rawBits, uint32(bit), 1)
		}
		pending, eob = 0, 0
	}
	for y := range plane.rows {
		for x := range plane.columns {
			block := &plane.blocks[y*plane.stride+x]
			lastNew := 0
			for position := 63; position > 0; position-- {
				if coefficientMagnitude(block[position])>>shift == 1 {
					lastNew = position
					break
				}
			}
			if lastNew != 0 {
				flush()
			}
			run, correctionCount := 0, uint(0)
			var correctionBits uint64
			for position := 1; position <= lastNew; position++ {
				value := block[position]
				magnitude := coefficientMagnitude(value) >> shift
				switch {
				case magnitude > 1:
					correctionBits = correctionBits<<1 | uint64(magnitude&1)
					correctionCount++
					continue
				case magnitude == 1:
					sign := uint32(0)
					if value > 0 {
						sign = 1
					}
					emit(run<<4|1, sign, 1)
				default:
					run++
					if run < 16 {
						continue
					}
					emit(0xf0, 0, 0)
				}
				for correctionCount != 0 {
					correctionCount--
					emit(rawBits, uint32(correctionBits>>correctionCount)&1, 1)
				}
				run, correctionBits = 0, 0
			}
			if lastNew < 63 {
				eob++
				for _, value := range block[lastNew+1:] {
					magnitude := coefficientMagnitude(value) >> shift
					if magnitude > 1 {
						corrections[pending] = byte(magnitude & 1)
						pending++
					}
				}
				if eob == 32767 || pending >= 4096 {
					flush()
				}
			}
		}
	}
	flush()
}

func coefficientMagnitude(value int16) int {
	return max(int(value), -int(value))
}
