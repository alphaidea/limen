package jpegcodec

// pruneBlock removes small high-frequency AC terms. Parseval's identity gives
// the energy lost within a block. The sum of absolute magnitudes bounds the
// pointwise error because each normalised AC basis value is at most 1/4.
func pruneBlock(block []coefficient, quantisation *[64]byte, luma bool) []coefficient {
	if len(block) == 0 || block[len(block)-1].position < 10 {
		return block
	}

	var activity int
	var totalEnergy int64
	var magnitudes [63]int
	for i, value := range block {
		absolute := magnitude(value, quantisation)
		totalEnergy += int64(absolute) * int64(absolute)
		if value.position < 10 {
			activity += absolute
		}
		magnitudes[i] = absolute
	}
	// Limit the aligned block's contrast and structure loss to 1%.
	// 3745 is 64 * (0.03 * 255)^2 rounded down, the SSIM stabiliser
	// expressed in the coefficient domain.
	energy := (2*totalEnergy + 3745) / 101
	peak := 3 + min(4, activity/80)
	if !luma {
		peak = min(peak, 4)
	}
	amplitude := 4 * peak

	changed := false
	for i := len(block) - 1; i >= 0 && block[i].position >= 10; i-- {
		value := magnitudes[i]
		if value <= amplitude && int64(value*value) <= energy {
			energy -= int64(value * value)
			amplitude -= value
			block[i].size = 0
			changed = true
		}
	}
	if !changed {
		return block
	}
	kept := 0
	for _, value := range block {
		if value.size != 0 {
			block[kept] = value
			kept++
		}
	}
	return block[:kept]
}

func magnitude(value coefficient, quantisation *[64]byte) int {
	amplitude := int(value.bits)
	if amplitude < 1<<(value.size-1) {
		amplitude = (1<<value.size - 1) - amplitude
	}
	return amplitude * int(quantisation[value.position])
}
