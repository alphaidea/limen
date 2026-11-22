package pngcodec

import (
	"cmp"
	"image"
	"image/color"
	"slices"
)

type colourSample struct {
	value color.NRGBA
	count int
	index uint8
}

func searchPalettes(original image.Image, experimental bool, consider func(image.Image, uint8) error) error {
	bounds := original.Bounds()
	limit := 256
	if experimental {
		limit = 65_536
	}
	lookup := make(map[color.NRGBA]*colourSample)
	var samples []*colourSample
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			value := pixelColour(original, x, y)
			sample := lookup[value]
			if sample == nil {
				if len(samples) == limit {
					return nil
				}
				sample = &colourSample{value: value}
				lookup[value] = sample
				samples = append(samples, sample)
			}
			sample.count++
		}
	}

	indexed := image.NewPaletted(bounds, nil)
	emit := func(boxes []colourBox, tolerance uint8) error {
		slices.SortStableFunc(boxes, func(a, b colourBox) int {
			// PNG can omit trailing opaque entries from tRNS.
			return compareColours(a.mean, b.mean)
		})
		indexed.Palette = make(color.Palette, len(boxes))
		for index, box := range boxes {
			indexed.Palette[index] = box.mean
			for _, sample := range box.samples {
				sample.index = uint8(index)
			}
		}
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			row := indexed.Pix[indexed.PixOffset(bounds.Min.X, y):]
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				row[x-bounds.Min.X] = lookup[pixelColour(original, x, y)].index
			}
		}
		return consider(indexed, tolerance)
	}

	if len(samples) <= 256 {
		boxes := make([]colourBox, len(samples))
		for i, sample := range samples {
			boxes[i] = colourBox{samples: samples[i : i+1], mean: sample.value}
		}
		if err := emit(boxes, 0); err != nil {
			return err
		}
	}
	if !experimental || len(samples) <= 2 {
		return nil
	}
	for _, tolerance := range []int{1, 2, 4, paletteTolerance} {
		if bounded := boundedPaletteWithTolerance(samples, bounds.Dx()*bounds.Dy(), tolerance); bounded != nil {
			if err := emit(bounded, uint8(tolerance)); err != nil {
				return err
			}
		}
	}

	slices.SortFunc(samples, func(a, b *colourSample) int { return compareColours(a.value, b.value) })
	var boxes []colourBox
	for start := 0; start < len(samples); {
		end := start + 1
		for end < len(samples) && samples[end].value.A == samples[start].value.A {
			end++
		}
		boxes = append(boxes, measureBox(samples[start:end]))
		start = end
	}

	for _, target := range []int{4, 16, 64, 128, 256} {
		if len(boxes) > target || target >= len(samples) {
			continue
		}
		for len(boxes) < target {
			largest := 0
			for i := range boxes {
				if boxes[i].priority > boxes[largest].priority {
					largest = i
				}
			}
			left, right := boxes[largest].split()
			boxes[largest] = left
			boxes = append(boxes, right)
		}
		// Preserve box ordering between successive splits.
		if err := emit(slices.Clone(boxes), paletteTolerance); err != nil {
			return err
		}
	}
	return nil
}

func pixelColour(img image.Image, x, y int) color.NRGBA {
	if rgba, ok := img.(*image.NRGBA); ok {
		value := rgba.NRGBAAt(x, y)
		if value.A == 0 {
			return color.NRGBA{}
		}
		return value
	}
	value := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	if value.A == 0 {
		return color.NRGBA{}
	}
	return value
}

func reduceColourPrecision(original image.Image, step int) *image.NRGBA {
	bounds := original.Bounds()
	result := image.NewNRGBA(bounds)
	var levels [256]uint8
	for i := range levels {
		levels[i] = uint8(min(255, ((i+step/2)/step)*step))
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			value := pixelColour(original, x, y)
			result.SetNRGBA(x, y, color.NRGBA{R: levels[value.R], G: levels[value.G], B: levels[value.B], A: value.A})
		}
	}
	return result
}

func compareColours(a, b color.NRGBA) int {
	key := func(c color.NRGBA) uint32 {
		return uint32(c.A)<<24 | uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
	}
	return cmp.Compare(key(a), key(b))
}

func rgb(value color.NRGBA) [3]int {
	return [3]int{int(value.R), int(value.G), int(value.B)}
}

type colourBox struct {
	samples  []*colourSample
	mean     color.NRGBA
	axis     int
	weight   int
	priority int64
}

func measureBox(samples []*colourSample) colourBox {
	box := colourBox{samples: samples}
	var sums, squares [3]int64
	for _, sample := range samples {
		box.weight += sample.count
		for channel, value := range rgb(sample.value) {
			sums[channel] += int64(value) * int64(sample.count)
			squares[channel] += int64(value*value) * int64(sample.count)
		}
	}
	weight := int64(box.weight)
	var mean [3]uint8
	var largest int64
	for channel := range sums {
		mean[channel] = uint8((sums[channel] + weight/2) / weight)
		variance := squares[channel] - sums[channel]*sums[channel]/weight
		if variance > largest {
			box.axis, largest = channel, variance
		}
		box.priority += variance
	}
	box.mean = color.NRGBA{R: mean[0], G: mean[1], B: mean[2], A: samples[0].value.A}
	return box
}

func (box colourBox) split() (colourBox, colourBox) {
	slices.SortStableFunc(box.samples, func(a, b *colourSample) int {
		return cmp.Compare(rgb(a.value)[box.axis], rgb(b.value)[box.axis])
	})
	index, count := 0, 0
	for index < len(box.samples)-1 && count*2 < box.weight {
		count += box.samples[index].count
		index++
	}
	return measureBox(box.samples[:index]), measureBox(box.samples[index:])
}

// Check the decoded candidate's exact alpha, peak channel error and local RMS
// channel error. Bounding straight RGB error also bounds composited sRGB error.
func checkQuantisation(original, candidate image.Image, tolerance uint8) bool {
	bounds := original.Bounds()
	const tile = 16
	for top := bounds.Min.Y; top < bounds.Max.Y; top += tile {
		for left := bounds.Min.X; left < bounds.Max.X; left += tile {
			bottom, right := min(top+tile, bounds.Max.Y), min(left+tile, bounds.Max.X)
			var squareError [3]int
			for y := top; y < bottom; y++ {
				for x := left; x < right; x++ {
					a, b := pixelColour(original, x, y), pixelColour(candidate, x, y)
					if a.A != b.A {
						return false
					}
					before, after := rgb(a), rgb(b)
					for channel := range before {
						delta := before[channel] - after[channel]
						if abs(delta) > int(tolerance) {
							return false
						}
						squareError[channel] += delta * delta
					}
				}
			}
			for _, squared := range squareError {
				if squared > 9*(bottom-top)*(right-left) {
					return false
				}
			}
		}
	}
	return true
}
