package procbind

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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

// CredFrame is what a turn is handed. A nil value is an entry that is not present; a present entry is never empty.
type CredFrame struct {
	SessionToken        []byte
	TaskToken           []byte
	MissionConfirmation []byte
}

func (f CredFrame) entries() [][2]any {
	return [][2]any{{CredSessionToken, f.SessionToken}, {CredTaskToken, f.TaskToken}, {CredMissionConfirmation, f.MissionConfirmation}}
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
		v := append([]byte(nil), rest[:n]...)
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

// ReadCredFrame reads exactly one frame from r and requires EOF after it. It reads the header first and refuses a declared
// length that is not plausible before it reads (or allocates) anything more, so a hostile or broken writer cannot make
// the reader wait for or hold more than MaxFrame bytes. The caller sets the deadline on r (an *os.File pipe takes
// SetReadDeadline); a read that times out is returned as it is.
func ReadCredFrame(r io.Reader) (CredFrame, error) {
	var hdr [credHeader]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return CredFrame{}, fmt.Errorf("%w: header: %v", ErrCredFrame, err)
	}
	if string(hdr[:4]) != credMagic || hdr[4] != credVersion {
		return CredFrame{}, fmt.Errorf("%w: wrong magic or version", ErrCredFrame)
	}
	total := int(binary.BigEndian.Uint16(hdr[5:7]))
	if total <= credHeader || total > MaxFrame {
		return CredFrame{}, fmt.Errorf("%w: bad length", ErrCredFrame)
	}
	buf := make([]byte, total)
	copy(buf, hdr[:])
	if _, err := io.ReadFull(r, buf[credHeader:]); err != nil {
		return CredFrame{}, fmt.Errorf("%w: body: %v", ErrCredFrame, err)
	}
	var one [1]byte
	if n, err := r.Read(one[:]); n != 0 || err != io.EOF {
		return CredFrame{}, fmt.Errorf("%w: trailing data or no end of stream", ErrCredFrame)
	}
	return DecodeCredFrame(buf)
}
