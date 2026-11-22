// Package visualdiff compares two screenshots pixel by pixel: how many pixels
// changed beyond a per-channel threshold, and an image that shows where.
//
// It backs `monoagentcli browser diff`, which a coder uses to check a
// recreated element against the element.png of a style bundle. Pixels are
// composited over white before they are compared, so two renderings that look
// the same on a white page count as the same even when a transparent pixel
// hides a different color. Images of different sizes are compared over the
// larger of the two: every pixel only one of them has counts as changed.
package visualdiff

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg" // Decode reads JPEG screenshots too.
	"image/png"
	"io"
	"os"
	"path/filepath"
)

// MaxPixels bounds the size of an image Decode accepts.
const MaxPixels = 64 << 20

// MaxThreshold is the largest per-channel threshold; at it nothing changes.
const MaxThreshold = 255

// Options tune a comparison.
type Options struct {
	// Threshold is the per-channel tolerance, 0–255: a pixel counts as changed
	// when its red, green or blue differs by more than this. 0 is exact.
	Threshold int
}

// Size is an image's width and height in pixels.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Result is what a comparison found.
type Result struct {
	Changed      int     `json:"changed"`
	Total        int     `json:"total"`
	Ratio        float64 `json:"ratio"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	SizeMismatch bool    `json:"sizeMismatch"`
	SizeA        Size    `json:"sizeA"`
	SizeB        Size    `json:"sizeB"`
	// Diff shows image A faded to light gray, changed pixels in red and the
	// area only one image covers in magenta.
	Diff *image.NRGBA `json:"-"`
}

var (
	changedColor  = color.NRGBA{R: 255, A: 255}
	mismatchColor = color.NRGBA{R: 255, B: 255, A: 255}
)

// Compare compares a and b.
func Compare(a, b image.Image, o Options) (*Result, error) {
	if o.Threshold < 0 || o.Threshold > MaxThreshold {
		return nil, fmt.Errorf("threshold %d is outside 0–%d", o.Threshold, MaxThreshold)
	}
	na, nb := toNRGBA(a), toNRGBA(b)
	sa := Size{na.Rect.Dx(), na.Rect.Dy()}
	sb := Size{nb.Rect.Dx(), nb.Rect.Dy()}
	w, h := max(sa.Width, sb.Width), max(sa.Height, sb.Height)
	res := &Result{
		Width: w, Height: h, Total: w * h,
		SizeA: sa, SizeB: sb, SizeMismatch: sa != sb,
		Diff: image.NewNRGBA(image.Rect(0, 0, w, h)),
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			inA := x < sa.Width && y < sa.Height
			inB := x < sb.Width && y < sb.Height
			if !inA || !inB {
				res.Changed++
				res.Diff.SetNRGBA(x, y, mismatchColor)
				continue
			}
			pa, pb := overWhite(na, x, y), overWhite(nb, x, y)
			if differs(pa, pb, o.Threshold) {
				res.Changed++
				res.Diff.SetNRGBA(x, y, changedColor)
				continue
			}
			res.Diff.SetNRGBA(x, y, faded(pa))
		}
	}
	if res.Total > 0 {
		res.Ratio = float64(res.Changed) / float64(res.Total)
	}
	return res, nil
}

// CompareFiles decodes two PNG or JPEG files and compares them.
func CompareFiles(pathA, pathB string, o Options) (*Result, error) {
	a, err := Decode(pathA)
	if err != nil {
		return nil, err
	}
	b, err := Decode(pathB)
	if err != nil {
		return nil, err
	}
	return Compare(a, b, o)
}

// Decode reads a PNG or JPEG file.
func Decode(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// The header alone says how much memory the decode will take; refuse a
	// decompression bomb before allocating for it.
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxPixels/cfg.Height {
		return nil, fmt.Errorf("decode %s: image %dx%d is too large (max %d pixels)", path, cfg.Width, cfg.Height, MaxPixels)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return img, nil
}

// WriteDiff writes the diff image as a PNG, creating its directory.
func (r *Result) WriteDiff(path string) error {
	if r.Diff == nil {
		return fmt.Errorf("no diff image")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, r.Diff); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// toNRGBA returns img as an NRGBA whose bounds start at 0,0, so pixels are
// read straight from Pix rather than through the slow image.Image interface.
func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok && n.Rect.Min == (image.Point{}) {
		return n
	}
	b := img.Bounds()
	n := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(n, n.Rect, img, b.Min, draw.Src)
	return n
}

// overWhite returns the pixel at x,y composited over an opaque white page.
func overWhite(img *image.NRGBA, x, y int) [3]uint8 {
	i := img.PixOffset(x, y)
	p := img.Pix[i : i+4 : i+4]
	a := int(p[3])
	var out [3]uint8
	for c := 0; c < 3; c++ {
		out[c] = uint8((int(p[c])*a + 255*(255-a) + 127) / 255)
	}
	return out
}

func differs(a, b [3]uint8, threshold int) bool {
	for c := 0; c < 3; c++ {
		d := int(a[c]) - int(b[c])
		if d < 0 {
			d = -d
		}
		if d > threshold {
			return true
		}
	}
	return false
}

// faded renders an unchanged pixel as a light gray of its luminance, so the
// red changes stand out while the layout stays recognizable.
func faded(p [3]uint8) color.NRGBA {
	lum := (299*int(p[0]) + 587*int(p[1]) + 114*int(p[2])) / 1000
	g := uint8(255 - (255-lum)/4)
	return color.NRGBA{R: g, G: g, B: g, A: 255}
}
