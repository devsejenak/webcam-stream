package detect

import (
	"image"
	"image/color"
	"image/draw"
)

// Annotate returns a copy of img with a rectangle drawn around each detection.
func Annotate(img image.Image, dets []Detection) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)

	thick := max(2, b.Dx()/320)
	for _, d := range dets {
		c := color.RGBA{0, 220, 80, 255}
		if d.Class == 0 {
			c = color.RGBA{255, 50, 50, 255} // person in red
		}
		r := d.Box.Sub(b.Min)
		fill := func(rect image.Rectangle) {
			draw.Draw(out, rect.Intersect(out.Bounds()), &image.Uniform{c}, image.Point{}, draw.Src)
		}
		fill(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+thick)) // top
		fill(image.Rect(r.Min.X, r.Max.Y-thick, r.Max.X, r.Max.Y)) // bottom
		fill(image.Rect(r.Min.X, r.Min.Y, r.Min.X+thick, r.Max.Y)) // left
		fill(image.Rect(r.Max.X-thick, r.Min.Y, r.Max.X, r.Max.Y)) // right
	}
	return out
}
