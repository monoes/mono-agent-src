// Package imagecheck reads an image's own header before anything is made of it:
// the size it claims, and whether it is animated. A few bytes can claim a
// picture that takes hundreds of megabytes to decode.
package imagecheck

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers the decoder for DecodeConfig
	_ "image/png"  // registers the decoder for DecodeConfig

	_ "golang.org/x/image/webp" // registers the decoder for DecodeConfig
)

const (
	// MaxSide is the longest side, in pixels, a picture may claim.
	MaxSide = 8192
	// MaxPixels is width times height a picture may claim (25 megapixels).
	MaxPixels = 25_000_000
)

// Check refuses a picture whose header cannot be read, that claims more than
// MaxSide or MaxPixels, or that is animated (a GIF with several frames, a WebP
// with the animation flag).
func Check(data []byte) error {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("its size cannot be read: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return errors.New("it claims no size")
	}
	if cfg.Width > MaxSide || cfg.Height > MaxSide || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return fmt.Errorf("it claims %dx%d pixels; the limit is %d a side and %d megapixels", cfg.Width, cfg.Height, MaxSide, MaxPixels/1_000_000)
	}
	switch format {
	case "png":
		return checkPNG(data)
	case "gif":
		return checkGIF(data)
	case "webp":
		// RIFF....WEBPVP8X<size>flags: bit 1 is the animation flag.
		if len(data) >= 21 && string(data[12:16]) == "VP8X" && data[20]&0x02 != 0 {
			return errors.New("animated pictures are not taken")
		}
	}
	return nil
}

// checkPNG walks the chunk list up to the first IDAT, without reading any pixel
// data, and refuses an animated PNG: png.DecodeConfig ignores its acTL chunk.
func checkPNG(data []byte) error {
	bad := errors.New("it cannot be read: the PNG is cut short")
	pos := 8 // signature; DecodeConfig has already matched it and read IHDR
	for pos != len(data) { // a header with no chunks after it is left to the size caps
		if len(data)-pos < 8 {
			return bad
		}
		n := int64(binary.BigEndian.Uint32(data[pos:]))
		switch string(data[pos+4 : pos+8]) {
		case "acTL":
			return errors.New("animated pictures are not taken")
		case "IDAT", "IEND":
			return nil
		}
		next := int64(pos) + 8 + n + 4 // length, type, data, CRC
		if next > int64(len(data)) {
			return bad
		}
		pos = int(next)
	}
	return nil
}

// checkGIF walks the GIF's block structure without decoding any pixel data: a
// frame can be as large as the logical screen, and decoding many of them takes
// gigabytes. It refuses more than one image, a frame past the caps, and a
// structure that is cut short.
func checkGIF(data []byte) error {
	bad := errors.New("it cannot be read: the GIF is cut short")
	if len(data) < 13 {
		return bad
	}
	pos := 13
	if data[10]&0x80 != 0 {
		pos += 3 << (data[10]&7 + 1)
	}
	skipSubBlocks := func() bool {
		for pos < len(data) {
			n := int(data[pos])
			pos += 1 + n
			if n == 0 {
				return pos <= len(data)
			}
		}
		return false
	}
	frames := 0
	for pos < len(data) {
		switch data[pos] {
		case 0x3b:
			if frames == 0 {
				return errors.New("it cannot be read: the GIF has no image")
			}
			return nil
		case 0x21:
			pos += 2
			if !skipSubBlocks() {
				return bad
			}
		case 0x2c:
			if pos+10 > len(data) {
				return bad
			}
			w := int(binary.LittleEndian.Uint16(data[pos+5:]))
			h := int(binary.LittleEndian.Uint16(data[pos+7:]))
			if w > MaxSide || h > MaxSide || int64(w)*int64(h) > MaxPixels {
				return fmt.Errorf("a frame claims %dx%d pixels; the limit is %d a side and %d megapixels", w, h, MaxSide, MaxPixels/1_000_000)
			}
			frames++
			if frames > 1 {
				return errors.New("animated pictures are not taken")
			}
			packed := data[pos+9]
			pos += 10
			if packed&0x80 != 0 {
				pos += 3 << (packed&7 + 1)
			}
			pos++ // LZW minimum code size
			if !skipSubBlocks() {
				return bad
			}
		default:
			return errors.New("it cannot be read: unknown GIF block")
		}
	}
	return bad
}
