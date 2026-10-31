package procbind

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// The typed credential frame is how a launched turn receives its credentials over the pipe the host holds: one atomic write,
// read once, never sniffed. Layout (big endian):
//
//	magic(4) "MAFR" | version(1) | total length(2) | entries...
//	entry: kind(1) | length(2) | value
//
// total length is the length of the whole frame, header included. Kinds are the session token, the task token and the
// mission confirmation; each occurs at most once. The whole frame stays within PipeBuf, so one write(2) delivers it
// whole or not at all, and the decoder rejects anything that is not exactly a frame.

// The prefixes of v2 token secrets. They differ from the legacy ones ("mst-", "mat-") on purpose: a legacy verifier,
// including an older binary, never resolves a v2 secret, and a v2 verifier looks a v2 secret up only in the v2 tables.
const (
	SessionPrefixV2 = "mst2-"
	TaskPrefixV2    = "mat2-"
)

// CredKind names an entry of a frame.
type CredKind byte

const (
	CredSessionToken        CredKind = 1
	CredTaskToken           CredKind = 2
	CredMissionConfirmation CredKind = 3
)

const (
	credMagic   = "MAFR"
	credVersion = 1
	credHeader  = 4 + 1 + 2
	// PipeBuf is the largest write to a pipe that is atomic (PIPE_BUF on Linux and the other systems the host runs
	// on is at least 4096; POSIX guarantees 512, Linux gives 4096).
	PipeBuf = 4096
	// MaxFrame is the largest frame; it is the atomic write size.
	MaxFrame = PipeBuf
)

var (
	// ErrCredFrame is every refusal of a frame: the cause is in the text, never the bytes.
	ErrCredFrame = errors.New("procbind: malformed credential frame")
)

// Secret is a credential's bytes. It prints as "[redacted]" under every fmt verb (%v %+v %#v %s %q %x %d), as a
// structured log value and as JSON/text, so a frame or a field of it that reaches a log, an error or a test failure
// message shows nothing. The bytes are read by converting (string(s), []byte(s)); Zero clears them.
type Secret []byte

const redacted = "[redacted]"

// Format implements fmt.Formatter: every verb prints the same redaction.
func (Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

func (Secret) String() string               { return redacted }
func (Secret) GoString() string             { return redacted }
func (Secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// Zero overwrites the bytes. Call it when the credential has been handed on.
func (s Secret) Zero() {
	for i := range s {
		s[i] = 0
	}
}

// CredFrame is what a turn is handed. A nil value is an entry that is not present; a present entry is never empty.
// It prints as redacted under every verb. The decoder and the reader hand out copies the caller owns: call Zero when
// the credentials have been used (the reader's own buffer is zeroed before it returns).
type CredFrame struct {
	SessionToken        Secret
	TaskToken           Secret
	MissionConfirmation Secret
}

// Format implements fmt.Formatter: a frame prints as redacted whatever the verb, and so does a pointer to one.
func (CredFrame) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "CredFrame{"+redacted+"}") }

func (CredFrame) String() string       { return "CredFrame{" + redacted + "}" }
func (CredFrame) GoString() string     { return "CredFrame{" + redacted + "}" }
func (CredFrame) LogValue() slog.Value { return slog.StringValue("CredFrame{" + redacted + "}") }

// Zero overwrites every entry and forgets them.
func (f *CredFrame) Zero() {
	f.SessionToken.Zero()
	f.TaskToken.Zero()
	f.MissionConfirmation.Zero()
	*f = CredFrame{}
}

func (f CredFrame) entries() [][2]any {
	return [][2]any{{CredSessionToken, []byte(f.SessionToken)}, {CredTaskToken, []byte(f.TaskToken)}, {CredMissionConfirmation, []byte(f.MissionConfirmation)}}
}

// Encode returns the frame's bytes, or an error if an entry is too long or the frame would exceed MaxFrame. An
// entry that is nil is left out; one that is empty but not nil is refused.
func (f CredFrame) Encode() ([]byte, error) {
	total := credHeader
	for _, e := range f.entries() {
		v := e[1].([]byte)
		if v == nil {
			continue
		}
		if len(v) == 0 {
			return nil, fmt.Errorf("%w: an entry is empty", ErrCredFrame)
		}
		total += 3 + len(v)
	}
	if total > MaxFrame {
		return nil, fmt.Errorf("%w: %d bytes exceed the %d an atomic write holds", ErrCredFrame, total, MaxFrame)
	}
	if total == credHeader {
		return nil, fmt.Errorf("%w: no entries", ErrCredFrame)
	}
	out := make([]byte, 0, total)
	out = append(out, credMagic...)
	out = append(out, credVersion)
	out = binary.BigEndian.AppendUint16(out, uint16(total))
	for _, e := range f.entries() {
		v := e[1].([]byte)
		if v == nil {
			continue
		}
		out = append(out, byte(e[0].(CredKind)))
		out = binary.BigEndian.AppendUint16(out, uint16(len(v)))
		out = append(out, v...)
	}
	return out, nil
}

// DecodeCredFrame parses b, which must be exactly one frame: right magic and version, a total length that is b's
// length, entries that fill it exactly, known kinds in ascending order (so each at most once), no empty value, nothing after.
func DecodeCredFrame(b []byte) (CredFrame, error) {
	var f CredFrame
	if len(b) < credHeader {
		return f, fmt.Errorf("%w: short", ErrCredFrame)
	}
	if string(b[:4]) != credMagic {
		return f, fmt.Errorf("%w: wrong magic", ErrCredFrame)
	}
	if b[4] != credVersion {
		return f, fmt.Errorf("%w: unknown version", ErrCredFrame)
	}
	total := int(binary.BigEndian.Uint16(b[5:7]))
	if total < credHeader || total > MaxFrame {
		return f, fmt.Errorf("%w: bad length", ErrCredFrame)
	}
	if total != len(b) {
		return f, fmt.Errorf("%w: length does not match", ErrCredFrame)
	}
	var last CredKind
	rest := b[credHeader:]
	if len(rest) == 0 {
		return f, fmt.Errorf("%w: no entries", ErrCredFrame)
	}
	for len(rest) > 0 {
		if len(rest) < 3 {
			return f, fmt.Errorf("%w: truncated entry", ErrCredFrame)
		}
		kind, n := CredKind(rest[0]), int(binary.BigEndian.Uint16(rest[1:3]))
		rest = rest[3:]
		if n == 0 || n > len(rest) {
			return f, fmt.Errorf("%w: bad entry length", ErrCredFrame)
		}
		// kinds come in ascending order, so a kind is neither repeated nor out of place and a frame has one encoding
		if kind <= last {
			return f, fmt.Errorf("%w: repeated or out of order kind", ErrCredFrame)
		}
		last = kind
		v := Secret(append([]byte(nil), rest[:n]...))
		switch kind {
		case CredSessionToken:
			f.SessionToken = v
		case CredTaskToken:
			f.TaskToken = v
		case CredMissionConfirmation:
			f.MissionConfirmation = v
		default:
			return f, fmt.Errorf("%w: unknown kind", ErrCredFrame)
		}
		rest = rest[n:]
	}
	return f, nil
}

// readClass says what kind of read failure err is, without carrying the reader's own text (which could be anything):
// a deadline, an end of stream or a truncation stay detectable with errors.Is, everything else is an opaque failure.
func readClass(err error) error {
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return os.ErrDeadlineExceeded
	case errors.Is(err, io.ErrUnexpectedEOF):
		return io.ErrUnexpectedEOF
	case errors.Is(err, io.EOF):
		return io.EOF
	case errors.Is(err, io.ErrNoProgress):
		return io.ErrNoProgress
	}
	return errCredRead
}

var errCredRead = errors.New("the read failed")

// maxEmptyReads is how many reads that return neither data nor an error are tolerated before the stream counts as
// stuck (io.Reader allows them, and discourages them).
const maxEmptyReads = 16

// readFull is io.ReadFull that gives up on a reader that keeps returning (0, nil).
func readFull(r io.Reader, b []byte) error {
	n, empty := 0, 0
	for n < len(b) {
		m, err := r.Read(b[n:])
		n += m
		if err != nil {
			if n >= len(b) {
				return nil
			}
			if err == io.EOF && n > 0 {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		if m == 0 {
			if empty++; empty >= maxEmptyReads {
				return io.ErrNoProgress
			}
		} else {
			empty = 0
		}
	}
	return nil
}

// ReadCredFrame reads exactly one frame from r and requires the end of the stream after it. It reads the header first
// and refuses a declared length that is not plausible before it reads (or allocates) anything more, so a hostile or
// broken writer cannot make the reader wait for or hold more than MaxFrame bytes. The caller sets the deadline on r (an
// *os.File pipe takes SetReadDeadline). A failing read is returned wrapped in ErrCredFrame together with its class
// (os.ErrDeadlineExceeded, io.EOF, io.ErrUnexpectedEOF, io.ErrNoProgress, or an opaque failure), never with the
// reader's own text. The reader's buffer is zeroed before it returns; the frame returned is a copy the caller owns
// and zeroes (CredFrame.Zero).
func ReadCredFrame(r io.Reader) (CredFrame, error) {
	var hdr [credHeader]byte
	if err := readFull(r, hdr[:]); err != nil {
		return CredFrame{}, fmt.Errorf("%w: header: %w", ErrCredFrame, readClass(err))
	}
	if string(hdr[:4]) != credMagic || hdr[4] != credVersion {
		return CredFrame{}, fmt.Errorf("%w: wrong magic or version", ErrCredFrame)
	}
	total := int(binary.BigEndian.Uint16(hdr[5:7]))
	if total <= credHeader || total > MaxFrame {
		return CredFrame{}, fmt.Errorf("%w: bad length", ErrCredFrame)
	}
	buf := make([]byte, total)
	defer Secret(buf).Zero()
	copy(buf, hdr[:])
	if err := readFull(r, buf[credHeader:]); err != nil {
		return CredFrame{}, fmt.Errorf("%w: body: %w", ErrCredFrame, readClass(err))
	}
	// the end of the stream: a read that returns nothing yet is tried again a bounded number of times
	var one [1]byte
	for empty := 0; ; empty++ {
		n, err := r.Read(one[:])
		if n != 0 {
			return CredFrame{}, fmt.Errorf("%w: trailing data", ErrCredFrame)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return CredFrame{}, fmt.Errorf("%w: the end of the stream: %w", ErrCredFrame, readClass(err))
		}
		if empty+1 >= maxEmptyReads {
			return CredFrame{}, fmt.Errorf("%w: the end of the stream: %w", ErrCredFrame, io.ErrNoProgress)
		}
	}
	return DecodeCredFrame(buf)
}
