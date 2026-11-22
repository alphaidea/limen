package jpegcodec

import "math/bits"

type quantisation [4][64]byte

// The profiles increase high-frequency steps sooner than low-frequency steps.
// Integer multiples of the input step avoid moving coefficients already on the
// new grid. The DC step stays fixed to protect block means and gradients.
func (layout *scan) quantisationFor(strength int) quantisation {
	tables := layout.quantisation
	var used [4]bool
	for _, part := range layout.components {
		index := part.quantisation
		if used[index] {
			continue
		}
		used[index] = true
		for position := 1; position < 64; position++ {
			step := int(tables[index][position])
			target := 1 + strength*(position+5)/24
			if target >= 2*step {
				tables[index][position] = byte(min(255, target/step*step))
			}
		}
	}
	return tables
}

func requantise(block []coefficient, original, target *[64]byte) []coefficient {
	kept := 0
	for _, term := range block {
		before, after := int(original[term.position]), int(target[term.position])
		if before != after {
			// Round exact ties towards zero to favour a longer zero run.
			value := int(term.bits)
			negative := value < 1<<(term.size-1)
			if negative {
				value = (1<<term.size - 1) - value
			}
			value = (value*before + (after-1)/2) / after
			if value == 0 {
				continue
			}
			term.size = uint(bits.Len(uint(value)))
			term.bits = uint32(value)
			if negative {
				term.bits = (1<<term.size - 1) - term.bits
			}
		}
		block[kept] = term
		kept++
	}
	return block[:kept]
}

func appendQuantisation(output []byte, tables *quantisation) []byte {
	start := len(output)
	output = append(output, 0xff, 0xdb, 0, 0)
	for id, table := range tables {
		if table[0] != 0 {
			output = append(output, byte(id))
			output = append(output, table[:]...)
		}
	}
	length := len(output) - start - 2
	output[start+2], output[start+3] = byte(length>>8), byte(length)
	return output
}

// frequencyQuantisation uses the radial frequency of each term. Its shape
// leaves more room for fine quantisation at low frequencies than a zigzag ramp.
func (layout *scan) frequencyQuantisation(lumaStrength, chromaStrength int) quantisation {
	return layout.radialQuantisation(8*lumaStrength, chromaStrength, 0)
}

// radialQuantisation uses eighths of a luma step. Luma tilt increases the
// step towards the highest frequencies without coarsening low frequencies.
func (layout *scan) radialQuantisation(lumaStrength, chromaStrength, lumaTilt int) quantisation {
	tables := layout.quantisation
	var used [4]bool
	for _, part := range layout.components {
		index := part.quantisation
		if used[index] {
			continue
		}
		used[index] = true
		for position := 1; position < 64; position++ {
			step := int(tables[index][position])
			x, y := int(zigzag[position]&7), int(zigzag[position]>>3)
			frequency := x*x + y*y + x*y/2
			strength := lumaStrength + frequency*lumaTilt/96
			if !part.luma {
				strength = (chromaStrength * 3 / 2) * 8
			}
			target := (16 + frequency) * strength / 320
			if target > step {
				tables[index][position] = byte(min(target, 255))
			}
		}
	}
	return tables
}

var zigzag = [64]byte{
	0, 1, 8, 16, 9, 2, 3, 10, 17, 24, 32, 25, 18, 11, 4, 5,
	12, 19, 26, 33, 40, 48, 41, 34, 27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36, 29, 22, 15, 23, 30, 37, 44, 51,
	58, 59, 52, 45, 38, 31, 39, 46, 53, 60, 61, 54, 47, 55, 62, 63,
}
