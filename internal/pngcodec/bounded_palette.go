package pngcodec

import (
	"cmp"
	"image/color"
	"slices"
)

const paletteTolerance = 8

type boundedColourGroup struct {
	box   colourBox
	lower [3]int
	upper [3]int
	sum   [3]int
}

// Build a palette by intersecting the RGB ranges each sample can tolerate.
// Frequent colours stay exact. Groups never mix different alpha samples.
func boundedPalette(samples []*colourSample, pixelCount int) []colourBox {
	return boundedPaletteWithTolerance(samples, pixelCount, paletteTolerance)
}

func boundedPaletteWithTolerance(samples []*colourSample, pixelCount, limit int) []colourBox {
	const maxSamples = 4096
	if len(samples) <= 256 || len(samples) > maxSamples {
		return nil
	}
	ordered := slices.Clone(samples)
	slices.SortStableFunc(ordered, func(a, b *colourSample) int { return cmp.Compare(b.count, a.count) })

	var groups []boundedColourGroup
	var byAlpha [256][]int
	frequent := max(8, pixelCount/1024)
	for _, sample := range ordered {
		channels := rgb(sample.value)
		tolerance := limit
		if sample.count >= frequent {
			tolerance = 0
		}
		best := -1
		bestDistance := 3*255*255 + 1
		for _, index := range byAlpha[sample.value.A] {
			group := &groups[index]
			centre := rgb(group.box.mean)
			var distance int
			for channel, value := range channels {
				if value+tolerance < group.lower[channel] || value-tolerance > group.upper[channel] {
					distance = bestDistance
					break
				}
				delta := value - centre[channel]
				distance += delta * delta
			}
			if distance < bestDistance {
				best, bestDistance = index, distance
			}
		}
		if best < 0 {
			if len(groups) == 256 {
				return nil
			}
			best = len(groups)
			byAlpha[sample.value.A] = append(byAlpha[sample.value.A], best)
			groups = append(groups, boundedColourGroup{upper: [3]int{255, 255, 255}})
		}
		groups[best].add(sample, channels, tolerance)
	}

	boxes := make([]colourBox, len(groups))
	for i, group := range groups {
		boxes[i] = group.box
	}
	return boxes
}

func (group *boundedColourGroup) add(sample *colourSample, channels [3]int, tolerance int) {
	group.box.samples = append(group.box.samples, sample)
	group.box.weight += sample.count
	weight := group.box.weight
	var centre [3]uint8
	for channel, value := range channels {
		group.lower[channel] = max(group.lower[channel], value-tolerance)
		group.upper[channel] = min(group.upper[channel], value+tolerance)
		group.sum[channel] += value * sample.count
		mean := (group.sum[channel] + weight/2) / weight
		centre[channel] = uint8(min(group.upper[channel], max(group.lower[channel], mean)))
	}
	group.box.mean = color.NRGBA{R: centre[0], G: centre[1], B: centre[2], A: sample.value.A}
}
