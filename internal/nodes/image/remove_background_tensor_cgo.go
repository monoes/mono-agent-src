//go:build cgo

package image

import (
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"github.com/disintegration/imaging"
)

// ── preprocessing ──────────────────────────────────────────────────────────────

// preprocess converts img to the [1,3,320,320] float32 tensor expected by u2net.
// Matches rembg's base.py preprocessing exactly:
//  1. Resize to 320×320 with Lanczos
//  2. Convert to RGB float32 [0,1] then normalise per-channel
//  3. Lay out as NCHW
func preprocess(img image.Image, dst []float32) {
	resized := imaging.Resize(img, u2netSize, u2netSize, imaging.Lanczos)

	// Find max pixel value for safe normalization (rembg: divide by max)
	maxVal := 1.0
	bounds := resized.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := resized.At(x, y).RGBA()
			if v := float64(r >> 8); v > maxVal {
				maxVal = v
			}
			if v := float64(g >> 8); v > maxVal {
				maxVal = v
			}
			if v := float64(b >> 8); v > maxVal {
				maxVal = v
			}
		}
	}
	if maxVal < 1e-6 {
		maxVal = 1e-6
	}

	h := bounds.Max.Y - bounds.Min.Y
	w := bounds.Max.X - bounds.Min.X

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := resized.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			rf := float64(r>>8) / maxVal
			gf := float64(g>>8) / maxVal
			bf := float64(b>>8) / maxVal

			dst[0*h*w+y*w+x] = float32((rf - meanR) / stdR)
			dst[1*h*w+y*w+x] = float32((gf - meanG) / stdG)
			dst[2*h*w+y*w+x] = float32((bf - meanB) / stdB)
		}
	}
}

// ── post-processing ────────────────────────────────────────────────────────────

// postprocess builds the alpha mask from the raw model output.
// Matches rembg:
//  1. Normalize to [0,1] via (v - min) / (max - min)
//  2. Clip to [0,1]
//  3. Return as float32 slice sized [320×320]
func postprocess(raw []float32) []float32 {
	n := len(raw)
	minV, maxV := raw[0], raw[0]
	for _, v := range raw[1:] {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	span := maxV - minV
	if span < 1e-8 {
		span = 1e-8
	}
	out := make([]float32, n)
	for i, v := range raw {
		norm := (v - minV) / span
		if norm < 0 {
			norm = 0
		}
		if norm > 1 {
			norm = 1
		}
		out[i] = norm
	}
	return out
}

// applyMask composites the original image with the mask (resized to original
// dimensions) to produce an RGBA image with transparent background.
func applyMask(orig image.Image, maskFloats []float32, origW, origH int) *image.NRGBA {
	// Build a grayscale mask image at 320×320
	maskImg := image.NewGray(image.Rect(0, 0, u2netSize, u2netSize))
	for i, v := range maskFloats {
		y := i / u2netSize
		x := i % u2netSize
		maskImg.SetGray(x, y, color.Gray{Y: uint8(math.Round(float64(v) * 255))})
	}

	// Resize mask back to original dimensions; imaging returns *image.NRGBA
	resizedMask := imaging.Resize(maskImg, origW, origH, imaging.Lanczos)

	// Composite — use the red channel of the NRGBA mask as alpha (gray images
	// are stored with R==G==B after imaging.Resize, so any channel works)
	out := image.NewNRGBA(image.Rect(0, 0, origW, origH))
	for y := 0; y < origH; y++ {
		for x := 0; x < origW; x++ {
			r, g, b, _ := orig.At(x, y).RGBA()
			mc := resizedMask.NRGBAAt(x, y)
			out.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r >> 8),
				G: uint8(g >> 8),
				B: uint8(b >> 8),
				A: mc.R, // R==G==B for a grayscale source
			})
		}
	}
	return out
}

// flattenOntoColor composites the RGBA image onto a solid background colour.
// hexColor is a 3- or 6-digit hex string (with or without leading #).
func flattenOntoColor(img *image.NRGBA, hexColor string) (*image.NRGBA, error) {
	hexColor = strings.TrimPrefix(hexColor, "#")
	if len(hexColor) == 3 {
		hexColor = string([]byte{hexColor[0], hexColor[0], hexColor[1], hexColor[1], hexColor[2], hexColor[2]})
	}
	if len(hexColor) != 6 {
		return nil, fmt.Errorf("hex color must be 3 or 6 digits, got %q", hexColor)
	}
	rgbBytes, err := hex.DecodeString(hexColor)
	if err != nil {
		return nil, fmt.Errorf("invalid hex color %q: %w", hexColor, err)
	}
	br, bg, bb := rgbBytes[0], rgbBytes[1], rgbBytes[2]
	bounds := img.Bounds()
	out := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			a := float64(c.A) / 255.0
			out.SetNRGBA(x, y, color.NRGBA{
				R: uint8(float64(c.R)*a + float64(br)*(1-a)),
				G: uint8(float64(c.G)*a + float64(bg)*(1-a)),
				B: uint8(float64(c.B)*a + float64(bb)*(1-a)),
				A: 255,
			})
		}
	}
	return out, nil
}
