package imagecheck

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"runtime"
	"strings"
	"testing"
	"time"
)

func pngHeaderClaiming(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 1, 0 // 1 bit, grayscale
	binary.Write(&b, binary.BigEndian, uint32(13))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func TestCheck(t *testing.T) {
	small := image.NewGray(image.Rect(0, 0, 4, 4))
	var okPNG, okJPG, oneGIF, twoGIF bytes.Buffer
	png.Encode(&okPNG, small)
	jpeg.Encode(&okJPG, small, nil)
	pal := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	gif.Encode(&oneGIF, pal, nil)
	gif.EncodeAll(&twoGIF, &gif.GIF{Image: []*image.Paletted{pal, pal}, Delay: []int{1, 1}})

	cases := []struct {
		name string
		data []byte
		ok   bool
	}{
		{"png", okPNG.Bytes(), true},
		{"jpeg", okJPG.Bytes(), true},
		{"gif one frame", oneGIF.Bytes(), true},
		{"gif animated", twoGIF.Bytes(), false},
		{"60000x60000 png header", pngHeaderClaiming(60000, 60000), false},
		{"9000x10 png header", pngHeaderClaiming(9000, 10), false},
		{"8192x8192 png header (over 25 MP)", pngHeaderClaiming(8192, 8192), false},
		{"5000x5000 png header", pngHeaderClaiming(5000, 5000), true},
		{"magic only", []byte("\x89PNG\r\n\x1a\n"), false},
		{"unreadable webp", []byte("RIFF\x04\x00\x00\x00WEBPVP8 "), false},
		{"animated webp flag", append([]byte("RIFF\x20\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x02\x00\x00\x00\x09\x00\x00\x09\x00\x00"), make([]byte, 8)...), false},
	}
	for _, c := range cases {
		if err := Check(c.data); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// gifFrames builds a GIF of n frames, each as large as the w x h logical screen, by hand:
// the pixel data is a stub, so building it costs nothing, while decoding it would allocate
// a full frame per image.
func gifFrames(w, h, n int) []byte {
	var b bytes.Buffer
	b.WriteString("GIF89a")
	binary.Write(&b, binary.LittleEndian, uint16(w))
	binary.Write(&b, binary.LittleEndian, uint16(h))
	b.Write([]byte{0x80, 0, 0, 0, 0, 0, 0xff, 0xff, 0xff}) // 2-colour global table
	for i := 0; i < n; i++ {
		b.Write([]byte{0x21, 0xf9, 4, 0, 1, 0, 0, 0}) // graphic control extension
		b.WriteByte(0x2c)
		binary.Write(&b, binary.LittleEndian, [4]uint16{0, 0, uint16(w), uint16(h)})
		b.Write([]byte{0, 2, 2, 0x44, 0x01, 0}) // LZW size, one sub-block, end
	}
	b.WriteByte(0x3b)
	return b.Bytes()
}

func TestCheckDoesNotDecodeGIFPixels(t *testing.T) {
	data := gifFrames(8192, 3051, 60)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	err := Check(data)
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "animated") {
		t.Fatalf("err = %v, want the animated refusal", err)
	}
	if took > 200*time.Millisecond {
		t.Errorf("took %v, want < 200ms", took)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 4<<20 {
		t.Errorf("allocated %d bytes, want < 4 MiB", grown)
	}
}

func TestCheckGIFStructure(t *testing.T) {
	if err := Check(gifFrames(100, 100, 1)); err != nil {
		t.Errorf("one frame: %v", err)
	}
	if err := Check(gifFrames(100, 100, 0)); err == nil {
		t.Error("a GIF with no frame was taken")
	}
	truncated := gifFrames(100, 100, 1)
	if err := Check(truncated[:len(truncated)-6]); err == nil {
		t.Error("a truncated GIF was taken")
	}
	// A frame bigger than the caps, inside a small logical screen.
	big := gifFrames(100, 100, 1)
	i := bytes.IndexByte(big[13+6:], 0x2c) + 13 + 6
	binary.LittleEndian.PutUint16(big[i+5:], 9000)
	if err := Check(big); err == nil {
		t.Error("an oversize frame was taken")
	}
}

// spliceChunk inserts a PNG chunk (valid CRC) right after IHDR.
func spliceChunk(t *testing.T, src []byte, typ string, body []byte) []byte {
	t.Helper()
	const at = 8 + 12 + 13 // signature + IHDR chunk
	var c bytes.Buffer
	binary.Write(&c, binary.BigEndian, uint32(len(body)))
	chunk := append([]byte(typ), body...)
	c.Write(chunk)
	binary.Write(&c, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	out := append([]byte{}, src[:at]...)
	out = append(out, c.Bytes()...)
	return append(out, src[at:]...)
}

func TestCheckAnimatedPNG(t *testing.T) {
	var plain bytes.Buffer
	png.Encode(&plain, image.NewGray(image.Rect(0, 0, 4, 4)))
	if err := Check(plain.Bytes()); err != nil {
		t.Fatalf("plain PNG refused: %v", err)
	}
	if err := Check(spliceChunk(t, plain.Bytes(), "tEXt", []byte("a\x00b"))); err != nil {
		t.Fatalf("PNG with an ancillary chunk refused: %v", err)
	}
	apng := spliceChunk(t, plain.Bytes(), "acTL", make([]byte, 8))
	if err := Check(apng); err == nil || !strings.Contains(err.Error(), "animated") {
		t.Fatalf("APNG not refused as animated: %v", err)
	}
}

func TestCheckPNGChunkListCutShort(t *testing.T) {
	var plain bytes.Buffer
	png.Encode(&plain, image.NewGray(image.Rect(0, 0, 4, 4)))
	full := spliceChunk(t, plain.Bytes(), "tEXt", []byte("abc"))
	for n := 8 + 12 + 13; n < len(full); n++ {
		Check(full[:n]) // must not panic
	}
	huge := append([]byte{}, full[:8+12+13]...)
	huge = append(huge, 0xff, 0xff, 0xff, 0xff, 't', 'E', 'X', 't')
	if err := Check(huge); err == nil {
		t.Fatal("chunk claiming 4 GiB accepted")
	}
}

func FuzzCheckPNG(f *testing.F) {
	var plain bytes.Buffer
	png.Encode(&plain, image.NewGray(image.Rect(0, 0, 4, 4)))
	f.Add(plain.Bytes())
	f.Fuzz(func(t *testing.T, b []byte) { Check(b) })
}
