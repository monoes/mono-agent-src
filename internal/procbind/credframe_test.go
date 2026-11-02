package procbind

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func tok(prefix string, n int) []byte { return []byte(prefix + strings.Repeat("x", n)) }

func TestCredFrameRoundTrips(t *testing.T) {
	for name, f := range map[string]CredFrame{
		"session only":              {SessionToken: tok("mst-", 40)},
		"task only":                 {TaskToken: tok("mat-", 40)},
		"mission confirmation only": {MissionConfirmation: tok("c", 90)},
		"session and task":          {SessionToken: tok("mst-", 40), TaskToken: tok("mat-", 40)},
		"all three":                 {SessionToken: tok("mst-", 40), TaskToken: tok("mat-", 40), MissionConfirmation: tok("c", 90)},
		"one byte values":           {SessionToken: []byte("a"), TaskToken: []byte("b")},
	} {
		b, err := f.Encode()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got, err := DecodeCredFrame(b)
		if err != nil || !bytes.Equal(got.SessionToken, f.SessionToken) || !bytes.Equal(got.TaskToken, f.TaskToken) || !bytes.Equal(got.MissionConfirmation, f.MissionConfirmation) {
			t.Errorf("%s: decoded %+v (%v)", name, got, err)
		}
		// a value that was absent stays nil, not empty
		if f.TaskToken == nil && got.TaskToken != nil || f.SessionToken == nil && got.SessionToken != nil {
			t.Errorf("%s: an absent entry came back present", name)
		}
		// through a reader that gives one byte at a time too
		if got, err := ReadCredFrame(iotest.OneByteReader(bytes.NewReader(b))); err != nil || !bytes.Equal(got.SessionToken, f.SessionToken) {
			t.Errorf("%s: one-byte reader: %+v %v", name, got, err)
		}
	}
}

func TestCredFrameEncodeRefuses(t *testing.T) {
	for name, f := range map[string]CredFrame{
		"nothing":             {},
		"an empty entry":      {SessionToken: []byte{}},
		"larger than a pipe":  {SessionToken: tok("mst-", PipeBuf)},
		"two that add up":     {SessionToken: tok("m", 2500), TaskToken: tok("t", 2500)},
		"just over the limit": {SessionToken: bytes.Repeat([]byte("m"), MaxFrame-credHeader-3+1)},
	} {
		if b, err := f.Encode(); !errors.Is(err, ErrCredFrame) || b != nil {
			t.Errorf("%s: encoded (%d bytes, %v)", name, len(b), err)
		}
	}
	// exactly at the limit is fine
	b, err := CredFrame{SessionToken: bytes.Repeat([]byte("m"), MaxFrame-credHeader-3)}.Encode()
	if err != nil || len(b) != MaxFrame {
		t.Errorf("a frame of exactly %d bytes: %d %v", MaxFrame, len(b), err)
	}
}

func goodFrame(t *testing.T) []byte {
	t.Helper()
	b, err := CredFrame{SessionToken: tok("mst-", 20), MissionConfirmation: tok("c", 10)}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Every way a frame can be wrong is refused: by DecodeCredFrame and by ReadCredFrame.
func TestCredFrameDecoderRefuses(t *testing.T) {
	good := goodFrame(t)
	patch := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	cases := map[string][]byte{
		"empty":         nil,
		"short header":  good[:5],
		"wrong magic":   patch(func(b []byte) []byte { b[0] = 'X'; return b }),
		"wrong version": patch(func(b []byte) []byte { b[4] = 2; return b }),
		"total too big": patch(func(b []byte) []byte { b[5], b[6] = 0xff, 0xff; return b }),
		"total smaller": patch(func(b []byte) []byte { b[6]--; return b }),
		"total larger":  patch(func(b []byte) []byte { b[6]++; return b }),
		"trailing byte": append(append([]byte(nil), good...), 0),
		"unknown kind":  patch(func(b []byte) []byte { b[7] = 9; return b }),
		"kind zero":     patch(func(b []byte) []byte { b[7] = 0; return b }),
		"entry length past the end": patch(func(b []byte) []byte {
			b[9] = 0xff
			return b
		}),
		"zero length entry": patch(func(b []byte) []byte { b[8], b[9] = 0, 0; return b }),
		"no entries":        append(append([]byte(nil), good[:5]...), 0, 7),
	}
	// a repeated kind: two session tokens, built by hand
	rep := append([]byte(nil), "MAFR"...)
	rep = append(rep, 1, 0, 0)
	rep = append(rep, 1, 0, 1, 'a', 1, 0, 1, 'b')
	rep[6] = byte(len(rep))
	cases["repeated kind"] = rep
	// task token before the session token: out of order
	ooo := append([]byte(nil), "MAFR"...)
	ooo = append(ooo, 1, 0, 0)
	ooo = append(ooo, 2, 0, 1, 'a', 1, 0, 1, 'b')
	ooo[6] = byte(len(ooo))
	cases["out of order kinds"] = ooo
	// an unknown kind that is in order (after the known ones): refused for being unknown, not for its place
	unk := append([]byte(nil), "MAFR"...)
	unk = append(unk, 1, 0, 0)
	unk = append(unk, 1, 0, 1, 'a', 4, 0, 1, 'b')
	unk[6] = byte(len(unk))
	cases["unknown kind in order"] = unk
	for name, b := range cases {
		if _, err := DecodeCredFrame(b); !errors.Is(err, ErrCredFrame) {
			t.Errorf("DecodeCredFrame %s: %v", name, err)
		}
		if _, err := ReadCredFrame(bytes.NewReader(b)); !errors.Is(err, ErrCredFrame) {
			t.Errorf("ReadCredFrame %s: %v", name, err)
		}
	}
}

// Truncating a good frame at every offset is a refusal, never a partial success, and never a panic.
func TestCredFrameTruncatedAtEveryOffset(t *testing.T) {
	good := goodFrame(t)
	for n := 0; n < len(good); n++ {
		if _, err := DecodeCredFrame(good[:n]); !errors.Is(err, ErrCredFrame) {
			t.Errorf("decode of %d bytes: %v", n, err)
		}
		if _, err := ReadCredFrame(bytes.NewReader(good[:n])); !errors.Is(err, ErrCredFrame) {
			t.Errorf("read of %d bytes: %v", n, err)
		}
	}
}

// Anything after the frame, and a stream that does not end, are refused by the reader.
func TestCredFrameReaderRequiresTheEnd(t *testing.T) {
	good := goodFrame(t)
	if _, err := ReadCredFrame(io.MultiReader(bytes.NewReader(good), strings.NewReader("x"))); !errors.Is(err, ErrCredFrame) {
		t.Errorf("a byte after the frame: %v", err)
	}
	// a writer that never closes: the read of the end fails with the reader's own error (a timeout on a real pipe)
	stuck := io.MultiReader(bytes.NewReader(good), iotest.ErrReader(io.ErrNoProgress))
	if _, err := ReadCredFrame(stuck); !errors.Is(err, ErrCredFrame) {
		t.Errorf("no end of stream: %v", err)
	}
}

// A declared length is checked before anything is read or allocated for it: a header that promises 65535 bytes is
// refused at once, with only the header consumed.
func TestCredFrameReaderRefusesAnImplausibleLengthBeforeReading(t *testing.T) {
	hdr := []byte("MAFR\x01\xff\xff")
	r := &countingReader{r: io.MultiReader(bytes.NewReader(hdr), neverReader{})}
	if _, err := ReadCredFrame(r); !errors.Is(err, ErrCredFrame) {
		t.Fatalf("%v", err)
	}
	if r.n != len(hdr) {
		t.Errorf("read %d bytes, want only the %d of the header", r.n, len(hdr))
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

type neverReader struct{}

func (neverReader) Read([]byte) (int, error) { panic("read past the header of an implausible frame") }

// The decoder copies: changing the input afterwards does not change the decoded frame.
func TestCredFrameDecoderCopiesItsInput(t *testing.T) {
	b := goodFrame(t)
	f, err := DecodeCredFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	want := string(f.SessionToken)
	for i := range b {
		b[i] = 0
	}
	if string(f.SessionToken) != want {
		t.Error("the decoded token aliases the input")
	}
}

func FuzzDecodeCredFrame(f *testing.F) {
	b, _ := CredFrame{SessionToken: tok("mst-", 20), TaskToken: tok("mat-", 20), MissionConfirmation: tok("c", 10)}.Encode()
	f.Add(b)
	f.Add([]byte("MAFR\x01\x00\x07"))
	f.Fuzz(func(t *testing.T, in []byte) {
		got, err := DecodeCredFrame(in)
		if err != nil {
			return
		}
		// what decodes encodes back to the same bytes
		again, err := got.Encode()
		if err != nil || !bytes.Equal(again, in) {
			t.Fatalf("decoded but does not re-encode to the same bytes: %v", err)
		}
	})
}
