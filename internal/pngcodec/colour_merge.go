package pngcodec

import (
	"cmp"
	"image"
	"image/color"
	"slices"
)

type rgbFrequency struct {
	value color.NRGBA
	count int
}

// Merge rare RGB colours into nearby frequent ones. Alpha remains in its own
// channel, so a shade can be reused across many transparency levels.
func searchMergedColours(original image.Image, consider func(image.Image, uint8) error) error {
	bounds := original.Bounds()
	const maxColours = 4096
	frequencies := make(map[color.NRGBA]int)
	var colours []rgbFrequency
	translucent := false
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			value := pixelColour(original, x, y)
			if value.A == 0 {
				continue
			}
			translucent = translucent || value.A < 255
			value.A = 255
			if frequencies[value] == 0 {
				if len(colours) == maxColours {
					return nil
				}
				colours = append(colours, rgbFrequency{value: value})
			}
			frequencies[value]++
		}
	}
	if !translucent || len(colours) < 2 {
		return nil
	}
	for i := range colours {
		colours[i].count = frequencies[colours[i].value]
	}
	slices.SortStableFunc(colours, func(a, b rgbFrequency) int { return cmp.Compare(b.count, a.count) })

	candidate := image.NewNRGBA(bounds)
	for _, tolerance := range []uint8{3, 4} {
		replacements := mergeColours(colours, int(tolerance))
		if len(replacements) == 0 {
			continue
		}
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				value := pixelColour(original, x, y)
				if value.A != 0 {
					alpha := value.A
					value.A = 255
					if replacement, ok := replacements[value]; ok {
						value = replacement
					}
					value.A = alpha
				}
				candidate.SetNRGBA(x, y, value)
			}
		}
		if err := consider(candidate, tolerance); err != nil {
			return err
		}
	}
	return nil
}

func mergeColours(colours []rgbFrequency, tolerance int) map[color.NRGBA]color.NRGBA {
	replacements := make(map[color.NRGBA]color.NRGBA)
	var representatives []color.NRGBA
	for _, entry := range colours {
		value := entry.value
		closest := value
		distance := 3*tolerance*tolerance + 1
		for _, representative := range representatives {
			red := int(value.R) - int(representative.R)
			green := int(value.G) - int(representative.G)
			blue := int(value.B) - int(representative.B)
			if max(abs(red), abs(green), abs(blue)) <= tolerance {
				if squared := red*red + green*green + blue*blue; squared < distance {
					closest, distance = representative, squared
				}
			}
		}
		if closest == value {
			representatives = append(representatives, value)
		} else {
			replacements[value] = closest
		}
	}
	return replacements
}
