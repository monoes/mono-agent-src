package procbind

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

const secretText = "mst-SUPERSECRETVALUE0123456789abcdefghijk"

func hexOf(s string) string { return fmt.Sprintf("%x", s) }

// A frame, a pointer to one and a secret print as redacted under every verb, in logs, in JSON and in an error that
// formats one: none of the forms shows the bytes, in plain text, quoted or as hex or decimal bytes.
func TestCredFrameAndSecretNeverPrintTheirBytes(t *testing.T) {
	f := CredFrame{SessionToken: Secret(secretText), TaskToken: Secret("mat-TASKSECRET"), MissionConfirmation: Secret("7.NONCEVALUE")}
	values := map[string]any{
		"frame": f, "frame pointer": &f, "secret": f.SessionToken, "secret pointer": &f.SessionToken,
		"slice of secrets": []Secret{f.SessionToken, f.TaskToken}, "map of frames": map[string]CredFrame{"k": f},
	}
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T%v"}
	bad := []string{secretText, "TASKSECRET", "NONCEVALUE", hexOf(secretText), strings.ToUpper(hexOf(secretText)), fmt.Sprint([]byte(secretText))}
	check := func(what, out string) {
		for _, b := range bad {
			if strings.Contains(out, b) {
				t.Errorf("%s shows a credential (%q in %q)", what, b, out)
			}
		}
	}
	for name, v := range values {
		for _, verb := range verbs {
			check(name+" "+verb, fmt.Sprintf(verb, v))
		}
		check(name+" Sprint", fmt.Sprint(v))
		check(name+" Errorf", fmt.Errorf("while reading: %v", v).Error())
		check(name+" Errorf %w-less wrap", fmt.Errorf("%s: %+v", "x", v).Error())
		if j, err := json.Marshal(v); err == nil {
			check(name+" json", string(j))
		}
	}
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))
	l.Info("turn", "frame", f, "token", f.SessionToken, "group", slog.GroupValue(slog.Any("t", f.TaskToken)))
	check("slog json", buf.String())
	buf.Reset()
	slog.New(slog.NewTextHandler(&buf, nil)).Info("turn", "frame", f, "token", f.SessionToken)
	check("slog text", buf.String())
	// a frame prints as one redaction, not field by field
	for _, verb := range verbs[:6] {
		if got := fmt.Sprintf(verb, f); got != "CredFrame{[redacted]}" {
			t.Errorf("a frame under %s prints %q", verb, got)
		}
	}
	// the redaction does not stand in the way of the real bytes
	if string(f.SessionToken) != secretText || !bytes.Equal(f.SessionToken, []byte(secretText)) {
		t.Error("the bytes of a secret are not readable by converting")
	}
}

func TestCredFrameZeroClearsEveryEntry(t *testing.T) {
	st, tt, mc := []byte("mst-aaaaaaaa"), []byte("mat-bbbbbbbb"), []byte("1.cccc")
	f := CredFrame{SessionToken: st, TaskToken: tt, MissionConfirmation: mc}
	f.Zero()
	for name, b := range map[string][]byte{"session": st, "task": tt, "confirmation": mc} {
		if !bytes.Equal(b, make([]byte, len(b))) {
			t.Errorf("%s was not overwritten: %q", name, b)
		}
	}
	if f.SessionToken != nil || f.TaskToken != nil || f.MissionConfirmation != nil {
		t.Error("the entries are still referenced")
	}
}

// The reader's buffer is zeroed: the bytes it held are not left behind. (The decoded copy is the caller's.)
func TestTheReaderLeavesNoCopyOfTheCredentialsBehind(t *testing.T) {
	good := goodFrame(t)
	// a reader that records the slices it was asked to fill
	rec := &recordingReader{r: bytes.NewReader(good)}
	if _, err := ReadCredFrame(rec); err != nil {
		t.Fatal(err)
	}
	for _, b := range rec.bufs {
		if bytes.Contains(b, []byte("mst-")) {
			t.Errorf("a buffer the reader used still holds a token: %q", b)
		}
	}
}

type recordingReader struct {
	r    io.Reader
	bufs [][]byte
}

func (r *recordingReader) Read(p []byte) (int, error) {
	r.bufs = append(r.bufs, p)
	return r.r.Read(p)
}

type errAfter struct {
	r   io.Reader
	err error
}

func (e errAfter) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		return n, e.err
	}
	return n, err
}

// Read failures keep their class for errors.Is (a deadline, an end of stream, a truncation, no progress) and never
// carry the reader's own text.
func TestCredFrameReadErrorsKeepTheirClassAndNotTheReadersText(t *testing.T) {
	good := goodFrame(t)
	leaky := errors.New("secret path /home/user/.keys and token mst-LEAK")
	cases := map[string]struct {
		r     io.Reader
		class error
	}{
		"deadline in the header":   {errAfter{bytes.NewReader(nil), os.ErrDeadlineExceeded}, os.ErrDeadlineExceeded},
		"deadline in the body":     {errAfter{bytes.NewReader(good[:10]), os.ErrDeadlineExceeded}, os.ErrDeadlineExceeded},
		"deadline at the end":      {errAfter{bytes.NewReader(good), os.ErrDeadlineExceeded}, os.ErrDeadlineExceeded},
		"empty stream":             {bytes.NewReader(nil), io.EOF},
		"truncated header":         {bytes.NewReader(good[:3]), io.ErrUnexpectedEOF},
		"truncated body":           {bytes.NewReader(good[:len(good)-2]), io.ErrUnexpectedEOF},
		"a reader that never ends": {zeroReader{}, io.ErrNoProgress},
	}
	for name, c := range cases {
		_, err := ReadCredFrame(c.r)
		if !errors.Is(err, ErrCredFrame) || !errors.Is(err, c.class) {
			t.Errorf("%s: %v (class %v)", name, err, c.class)
		}
	}
	// an unknown reader error is opaque: still a frame error, with none of its text
	_, err := ReadCredFrame(errAfter{bytes.NewReader(nil), leaky})
	if !errors.Is(err, ErrCredFrame) || errors.Is(err, leaky) || strings.Contains(err.Error(), "LEAK") || strings.Contains(err.Error(), "/home") {
		t.Errorf("an unknown reader error leaked: %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read([]byte) (int, error) { return 0, nil }

// A reader may return (0, nil) now and then: a valid frame followed by such a read and then the end is accepted,
// and a reader that only ever does that is given up on.
func TestCredFrameReaderToleratesEmptyReads(t *testing.T) {
	good := goodFrame(t)
	if _, err := ReadCredFrame(&gappyReader{data: good, gaps: 3}); err != nil {
		t.Errorf("a frame with empty reads between its parts: %v", err)
	}
	if _, err := ReadCredFrame(io.MultiReader(bytes.NewReader(good), zeroReader{})); !errors.Is(err, io.ErrNoProgress) {
		t.Errorf("a stream that never ends with empty reads: %v", err)
	}
}

// gappyReader returns one (0, nil) before each of its reads, gaps times, then behaves.
type gappyReader struct {
	data []byte
	gaps int
	n    int
	pos  int
}

func (g *gappyReader) Read(p []byte) (int, error) {
	if g.n < g.gaps {
		g.n++
		return 0, nil
	}
	if g.pos >= len(g.data) {
		if g.n < g.gaps*2 { // one more empty read at the end before EOF
			g.n++
			return 0, nil
		}
		return 0, io.EOF
	}
	m := copy(p, g.data[g.pos:g.pos+1])
	g.pos += m
	return m, nil
}

// A frame that is refused leaves nothing of any credential: not in the value returned (it is the zero frame), and
// not in any copy made on the way (the decoder looks at every entry before it copies one, so there is none to
// clear). A valid session token followed by an unknown, repeated, out of order or truncated entry is such a frame.
func TestARefusedFrameHandsBackAndKeepsNoCredential(t *testing.T) {
	session := append([]byte{1, 0, byte(len(secretText))}, secretText...)
	build := func(rest ...byte) []byte {
		body := append(append([]byte(nil), session...), rest...)
		b := append([]byte("MAFR\x01"), 0, 0)
		b = append(b, body...)
		b[5], b[6] = byte((len(b))>>8), byte(len(b))
		return b
	}
	cases := map[string][]byte{
		"unknown kind after":     build(9, 0, 1, 'x'),
		"repeated kind after":    build(1, 0, 1, 'x'),
		"out of order is caught": build(0, 0, 1, 'x'),
		"truncated entry after":  build(3, 0, 9, 'x'),
		"zero length after":      build(3, 0, 0),
		"short entry header":     build(3, 0),
	}
	for name, b := range cases {
		f, err := DecodeCredFrame(b)
		if !errors.Is(err, ErrCredFrame) {
			t.Errorf("%s: accepted: %v", name, err)
			continue
		}
		if f.SessionToken != nil || f.TaskToken != nil || f.MissionConfirmation != nil {
			t.Errorf("%s: a refused frame handed back credentials", name)
		}
		if _, err := ReadCredFrame(bytes.NewReader(b)); !errors.Is(err, ErrCredFrame) {
			t.Errorf("%s (reader): %v", name, err)
		}
	}
}
