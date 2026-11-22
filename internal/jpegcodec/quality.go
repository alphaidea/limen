package jpegcodec

import (
	"image"
	"image/color"
	"math"
)

const (
	minimumMultiscaleSSIM = 0.90
	minimumLocalSSIM      = 0.980
	windowRadius          = 5
	localWindow           = 32
	localStep             = 16
)

type qualityReference struct {
	bounds image.Rectangle
	luma   []float32
	cb, cr []byte
}

func newQualityReference(source image.Image) qualityReference {
	bounds := source.Bounds()
	luma := make([]float32, bounds.Dx()*bounds.Dy())
	cb, cr := make([]byte, len(luma)), make([]byte, len(luma))
	for y := range bounds.Dy() {
		for x := range bounds.Dx() {
			lightness, blue, red := sample(source, x, y)
			index := y*bounds.Dx() + x
			luma[index], cb[index], cr[index] = lightness, byte(blue), byte(red)
		}
	}
	return qualityReference{bounds, luma, cb, cr}
}

func sample(source image.Image, x, y int) (float32, int, int) {
	switch source := source.(type) {
	case *image.Gray:
		return float32(source.GrayAt(x, y).Y), 128, 128
	case *image.YCbCr:
		c := source.YCbCrAt(x, y)
		r, g, b := color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
		return 0.2126*float32(r) + 0.7152*float32(g) + 0.0722*float32(b), int(c.Cb), int(c.Cr)
	default:
		r, g, b, _ := source.At(x, y).RGBA()
		y, cb, cr := color.RGBToYCbCr(byte(r>>8), byte(g>>8), byte(b>>8))
		return float32(y), int(cb), int(cr)
	}
}

func (reference qualityReference) accepts(candidate image.Image) bool {
	bounds := reference.bounds
	width, height := bounds.Dx(), bounds.Dy()
	if candidate.Bounds() != bounds || min(width, height) < 11 {
		return false
	}

	// Mean and RMS chroma error in each 8x8 region catch colour shifts and
	// ringing that luma SSIM alone would miss.
	luma := make([]float32, len(reference.luma))
	for top := 0; top < height; top += 8 {
		for left := 0; left < width; left += 8 {
			var cbMean, crMean, chromaEnergy int
			var lumaMean float32
			rows, columns := min(8, height-top), min(8, width-left)
			for y := top; y < top+rows; y++ {
				for x := left; x < left+columns; x++ {
					index := y*width + x
					lightness, cb, cr := sample(candidate, x, y)
					difference := lightness - reference.luma[index]
					if difference < -24 || difference > 24 {
						return false
					}
					lumaMean += difference
					cb, cr = cb-int(reference.cb[index]), cr-int(reference.cr[index])
					if cb < -16 || cb > 16 || cr < -16 || cr > 16 {
						return false
					}
					chromaEnergy += cb*cb + cr*cr
					cbMean += cb
					crMean += cr
					luma[index] = lightness
				}
			}
			count := rows * columns
			if lumaMean < -2*float32(count) || lumaMean > 2*float32(count) ||
				cbMean < -2*count || cbMean > 2*count || crMean < -2*count || crMean > 2*count || chromaEnergy > 32*count {
				return false
			}
		}
	}
	return multiscaleSSIM(reference.luma, luma, width, height)
}

type moments struct {
	a, b, aa, bb, ab float64
}

func (m *moments) add(other moments, weight float64) {
	m.a += other.a * weight
	m.b += other.b * weight
	m.aa += other.aa * weight
	m.bb += other.bb * weight
	m.ab += other.ab * weight
}

var gaussian = func() [11]float64 {
	var kernel [11]float64
	var total float64
	for i := range kernel {
		distance := float64(i - windowRadius)
		kernel[i] = math.Exp(-distance * distance / 4.5)
		total += kernel[i]
	}
	for i := range kernel {
		kernel[i] /= total
	}
	return kernel
}()

func multiscaleSSIM(a, b []float32, width, height int) bool {
	weights := [...]float64{0.0448, 0.2856, 0.3001, 0.2363, 0.1333}
	var weightedLog, totalWeight float64
	for scale, weight := range weights {
		last := scale == len(weights)-1 || min((width+1)/2, (height+1)/2) < 11
		score := scaleSSIM(a, b, width, height, scale == 0, last)
		if score <= 0 {
			return false
		}
		weightedLog += weight * math.Log(score)
		totalWeight += weight
		if last {
			return math.Exp(weightedLog/totalWeight) >= minimumMultiscaleSSIM
		}
		a = halfLuma(a, width, height)
		b = halfLuma(b, width, height)
		width, height = (width+1)/2, (height+1)/2
	}
	return false
}

func halfLuma(input []float32, width, height int) []float32 {
	output := make([]float32, (width+1)/2*((height+1)/2))
	for y, i := 0, 0; y < height; y += 2 {
		top, bottom := y*width, min(y+1, height-1)*width
		for x := 0; x < width; x += 2 {
			right := min(x+1, width-1)
			output[i] = (input[top+x] + input[top+right] + input[bottom+x] + input[bottom+right]) * 0.25
			i++
		}
	}
	return output
}

func scaleSSIM(a, b []float32, width, height int, local, last bool) float64 {
	const c1, c2 = 6.5025, 58.5225
	const diameter = 2*windowRadius + 1
	columns, rows := width-diameter+1, height-diameter+1
	horizontal := make([]moments, diameter*columns)
	var integral []float64
	if local {
		integral = make([]float64, (columns+1)*(localWindow+1))
	}
	var sum float64
	for y := range height {
		line := horizontal[y%diameter*columns:][:columns]
		rowA, rowB := a[y*width:][:width], b[y*width:][:width]
		for x := range columns {
			p, q := float64(rowA[x+windowRadius]), float64(rowB[x+windowRadius])
			weight := gaussian[windowRadius]
			value := moments{p * weight, q * weight, p * p * weight, q * q * weight, p * q * weight}
			for i := range windowRadius {
				j := diameter - 1 - i
				p, q := float64(rowA[x+i]), float64(rowB[x+i])
				r, s := float64(rowA[x+j]), float64(rowB[x+j])
				value.add(moments{p + r, q + s, p*p + r*r, q*q + s*s, p*q + r*s}, gaussian[i])
			}
			line[x] = value
		}
		if y < diameter-1 {
			continue
		}
		var localSum float64
		row := y - diameter + 1
		var lines [diameter][]moments
		for i := range lines {
			lines[i] = horizontal[(row+i)%diameter*columns:][:columns]
		}
		for x := range columns {
			var value moments
			value.add(lines[windowRadius][x], gaussian[windowRadius])
			for i := range windowRadius {
				p, q := lines[i][x], lines[diameter-1-i][x]
				value.add(moments{p.a + q.a, p.b + q.b, p.aa + q.aa, p.bb + q.bb, p.ab + q.ab}, gaussian[i])
			}
			structure := (2*(value.ab-value.a*value.b) + c2) /
				(value.aa - value.a*value.a + value.bb - value.b*value.b + c2)
			lightness := (2*value.a*value.b + c1) / (value.a*value.a + value.b*value.b + c1)
			if last {
				sum += structure * lightness
			} else {
				sum += structure
			}
			if local {
				localSum += structure * lightness
				above, current := row%(localWindow+1)*(columns+1), (row+1)%(localWindow+1)*(columns+1)
				integral[current+x+1] = integral[above+x+1] + localSum
			}
		}
		if local && !localSSIMRow(integral, columns, rows, row+1) {
			return 0
		}
	}
	return sum / float64(columns*rows)
}

func localSSIMRow(integral []float64, columns, rows, bottom int) bool {
	height, width := min(localWindow, rows), min(localWindow, columns)
	if bottom < height || (bottom-height)%localStep != 0 && bottom != rows {
		return true
	}
	stride := columns + 1
	top := (bottom - height) % (localWindow + 1) * stride
	end := bottom % 33 * stride
	for x := 0; ; x = min(x+localStep, columns-width) {
		right := x + width
		sum := integral[end+right] - integral[end+x] - integral[top+right] + integral[top+x]
		if sum < minimumLocalSSIM*float64(height*width) {
			return false
		}
		if right == columns {
			return true
		}
	}
}
