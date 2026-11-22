package pixelcheck

import (
	"bytes"
	"image"
	"image/color"
)

// Equivalent compares dimensions and alpha, and optionally visible colours.
func Equivalent(source, candidate image.Image, colours bool) bool {
	if source.Bounds().Size() != candidate.Bounds().Size() {
		return false
	}
	if colours && IdenticalPacked(source, candidate) {
		return true
	}
	leftPixels, leftStride, leftModel := packed(source)
	rightPixels, rightStride, rightModel := packed(candidate)
	if !colours && leftModel != nil && rightModel != nil {
		width := 4 * source.Bounds().Dx()
		for y := range source.Bounds().Dy() {
			left := leftPixels[y*leftStride : y*leftStride+width]
			right := rightPixels[y*rightStride : y*rightStride+width]
			for x := 3; x < width; x += 4 {
				if left[x] != right[x] {
					return false
				}
			}
		}
		return true
	}
	for y := range source.Bounds().Dy() {
		for x := range source.Bounds().Dx() {
			a := color.NRGBA64Model.Convert(source.At(x+source.Bounds().Min.X, y+source.Bounds().Min.Y)).(color.NRGBA64)
			b := color.NRGBA64Model.Convert(candidate.At(x+candidate.Bounds().Min.X, y+candidate.Bounds().Min.Y)).(color.NRGBA64)
			if a.A != b.A || colours && a.A != 0 && (a.R != b.R || a.G != b.G || a.B != b.B) {
				return false
			}
		}
	}
	return true
}

// IdenticalPacked reports whether two packed images have identical stored samples.
func IdenticalPacked(a, b image.Image) bool {
	left, ls, lm := packed(a)
	right, rs, rm := packed(b)
	if lm == nil || lm != rm || a.Bounds().Size() != b.Bounds().Size() {
		return false
	}
	width := a.Bounds().Dx() * 4
	for y := range a.Bounds().Dy() {
		if !bytes.Equal(left[y*ls:y*ls+width], right[y*rs:y*rs+width]) {
			return false
		}
	}
	return true
}

func packed(img image.Image) ([]byte, int, color.Model) {
	switch pixels := img.(type) {
	case *image.RGBA:
		return pixels.Pix, pixels.Stride, color.RGBAModel
	case *image.NRGBA:
		return pixels.Pix, pixels.Stride, color.NRGBAModel
	default:
		return nil, 0, nil
	}
}
