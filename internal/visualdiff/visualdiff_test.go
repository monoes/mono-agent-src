package visualdiff

import (
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func solid(w, h int, c color.Color) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestCompareIdentical(t *testing.T) {
	a := solid(10, 8, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	res, err := Compare(a, solid(10, 8, color.NRGBA{R: 10, G: 20, B: 30, A: 255}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 0 || res.Ratio != 0 || res.Total != 80 || res.SizeMismatch {
		t.Fatalf("identical images: %+v", res)
	}
}

func TestCompareCountsChangedPixelsAndMarksThemRed(t *testing.T) {
	a := solid(4, 4, color.White)
	b := solid(4, 4, color.White)
	b.Set(1, 2, color.Black)
	b.Set(3, 3, color.NRGBA{R: 250, G: 255, B: 255, A: 255}) // within threshold 10
	res, err := Compare(a, b, Options{Threshold: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 1 {
		t.Fatalf("changed = %d, want 1", res.Changed)
	}
	if res.Ratio != 1.0/16 {
		t.Fatalf("ratio = %v", res.Ratio)
	}
	if got := res.Diff.NRGBAAt(1, 2); got != changedColor {
		t.Fatalf("changed pixel drawn as %v", got)
	}
	if got := res.Diff.NRGBAAt(0, 0); got.R != 255 || got.G != 255 {
		t.Fatalf("unchanged white pixel drawn as %v", got)
	}
}

func TestCompareThresholdIsPerChannel(t *testing.T) {
	a := solid(1, 1, color.NRGBA{R: 100, G: 100, B: 100, A: 255})
	b := solid(1, 1, color.NRGBA{R: 100, G: 100, B: 120, A: 255})
	for _, tc := range []struct {
		threshold, want int
	}{{0, 1}, {19, 1}, {20, 0}, {255, 0}} {
		res, err := Compare(a, b, Options{Threshold: tc.threshold})
		if err != nil {
			t.Fatal(err)
		}
		if res.Changed != tc.want {
			t.Errorf("threshold %d: changed = %d, want %d", tc.threshold, res.Changed, tc.want)
		}
	}
}

func TestCompareRejectsBadThreshold(t *testing.T) {
	a := solid(1, 1, color.White)
	for _, th := range []int{-1, 256} {
		if _, err := Compare(a, a, Options{Threshold: th}); err == nil {
			t.Errorf("threshold %d accepted", th)
		}
	}
}

func TestCompareSizeMismatch(t *testing.T) {
	a := solid(4, 2, color.White)
	b := solid(2, 3, color.White)
	res, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SizeMismatch || res.Width != 4 || res.Height != 3 || res.Total != 12 {
		t.Fatalf("mismatch result: %+v", res)
	}
	// Overlap is 2x2 = 4 identical pixels; the other 8 exist in only one image.
	if res.Changed != 8 {
		t.Fatalf("changed = %d, want 8", res.Changed)
	}
	if res.SizeA != (Size{4, 2}) || res.SizeB != (Size{2, 3}) {
		t.Fatalf("sizes %v %v", res.SizeA, res.SizeB)
	}
	if got := res.Diff.NRGBAAt(3, 2); got != mismatchColor {
		t.Fatalf("mismatch area drawn as %v", got)
	}
}

func TestCompareEmptyImages(t *testing.T) {
	empty := image.NewNRGBA(image.Rect(0, 0, 0, 0))
	res, err := Compare(empty, empty, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 || res.Ratio != 0 {
		t.Fatalf("empty: %+v", res)
	}
	res, err = Compare(empty, solid(2, 2, color.White), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 4 || res.Ratio != 1 {
		t.Fatalf("empty vs 2x2: %+v", res)
	}
}

func TestCompareTransparentMatchesWhite(t *testing.T) {
	// A fully transparent pixel shows the white page, whatever RGB it carries.
	a := solid(2, 2, color.NRGBA{R: 255, G: 0, B: 0, A: 0})
	b := solid(2, 2, color.White)
	res, err := Compare(a, b, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 0 {
		t.Fatalf("transparent vs white changed = %d", res.Changed)
	}
}

func TestCompareNonZeroBoundsAndOtherModels(t *testing.T) {
	// A sub-image keeps its parent's coordinates; RGBA is premultiplied.
	parent := solid(6, 6, color.White)
	parent.Set(3, 3, color.Black)
	sub := parent.SubImage(image.Rect(2, 2, 5, 5))
	other := image.NewRGBA(image.Rect(0, 0, 3, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			other.Set(x, y, color.White)
		}
	}
	other.Set(1, 1, color.Black)
	res, err := Compare(sub, other, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 0 || res.Total != 9 {
		t.Fatalf("sub-image vs RGBA: %+v", res)
	}
}

func TestCompareFilesPNGAndJPEGAndWriteDiff(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, enc func(*os.File) error) string {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := enc(f); err != nil {
			t.Fatal(err)
		}
		f.Close()
		return p
	}
	img := solid(16, 16, color.NRGBA{R: 200, G: 200, B: 200, A: 255})
	pngPath := write("a.png", func(f *os.File) error { return png.Encode(f, img) })
	jpgPath := write("b.jpg", func(f *os.File) error { return jpeg.Encode(f, img, &jpeg.Options{Quality: 95}) })

	res, err := CompareFiles(pngPath, jpgPath, Options{Threshold: 8})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 0 {
		t.Fatalf("png vs jpeg of a flat image changed = %d", res.Changed)
	}
	out := filepath.Join(dir, "nested", "diff.png")
	if err := res.WriteDiff(out); err != nil {
		t.Fatal(err)
	}
	back, err := Decode(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Bounds().Dx() != 16 || back.Bounds().Dy() != 16 {
		t.Fatalf("diff image is %v", back.Bounds())
	}
}

func TestDecodeErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Decode(filepath.Join(dir, "missing.png")); err == nil {
		t.Fatal("missing file decoded")
	}
	bad := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(bad, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CompareFiles(bad, bad, Options{}); err == nil {
		t.Fatal("garbage decoded")
	}
}

func TestDecodeRefusesAnOversizedHeader(t *testing.T) {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 60000)
	binary.BigEndian.PutUint32(ihdr[4:], 60000)
	ihdr[8], ihdr[9] = 8, 6
	chunk := append([]byte("IHDR"), ihdr...)
	b := []byte("\x89PNG\r\n\x1a\n")
	b = binary.BigEndian.AppendUint32(b, 13)
	b = append(b, chunk...)
	b = binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(chunk))
	path := filepath.Join(t.TempDir(), "bomb.png")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(path); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("Decode err = %v, want too large", err)
	}
}
