package coderhost

import (
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// The host hands a chat child its mission-confirmation token over an inherited
// pipe, not through the environment or the arguments. A value placed in a
// process environment can be read by other processes of the same user until the
// process exits; a pipe read once and closed leaves nothing behind. The only
// thing in the environment is the number of the descriptor (a handle on Windows),
// which is not secret (MissionFDEnv).
//
// The child reads the pipe once, first thing, closes it and forgets the marker,
// so nothing it later starts inherits either.

// MissionFDEnv names the descriptor (Unix) or handle (Windows) the token pipe is
// on. It holds a number, never the token.
const MissionFDEnv = "MONOAGENT_MISSION_FD"

const missionTokenMax = 256 // most bytes read, newline included

var missionTokenTimeout = 2 * time.Second

var (
	stashMu      sync.Mutex
	stashedToken string
)

// StashMissionToken reads the token from the pipe named by MissionFDEnv, if there
// is one, and keeps it in memory for TakeMissionToken. The binary calls it before
// anything else can start a subprocess. It fails closed: a missing marker, a
// descriptor that is not a pipe, a read that does not finish in time, more than
// missionTokenMax bytes or text that is not a token all leave no token. The
// descriptor named by the marker is closed whatever happens.
func StashMissionToken() {
	v := os.Getenv(MissionFDEnv)
	if v == "" {
		return
	}
	_ = os.Unsetenv(MissionFDEnv)
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil || n < 3 {
		return
	}
	f, err := openInherited(uintptr(n))
	if err != nil {
		return
	}
	defer f.Close()
	if !isPipe(f) {
		return
	}
	tok, err := readMissionToken(f, missionTokenTimeout)
	if err != nil {
		return
	}
	stashMu.Lock()
	stashedToken = tok
	stashMu.Unlock()
}

// readMissionToken reads one newline-terminated token of at most missionTokenMax
// bytes from r, giving up after timeout.
func readMissionToken(r io.Reader, timeout time.Duration) (string, error) {
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		b, err := io.ReadAll(io.LimitReader(r, missionTokenMax+1))
		ch <- res{b, err}
	}()
	select {
	case x := <-ch:
		if x.err != nil {
			return "", x.err
		}
		b := x.b
		if len(b) > missionTokenMax || len(b) < 2 || b[len(b)-1] != '\n' {
			return "", errors.New("malformed mission token")
		}
		tok := string(b[:len(b)-1])
		if _, _, ok := parseToken(tok); !ok {
			return "", errors.New("malformed mission token")
		}
		for _, c := range []byte(tok) {
			if c <= ' ' || c >= 0x7f {
				return "", errors.New("malformed mission token")
			}
		}
		return tok, nil
	case <-time.After(timeout):
		return "", errors.New("mission token read timed out")
	}
}

// TakeMissionToken returns the token (reading it first if the pipe is still
// unread) and forgets it, so a token is handed out once.
func TakeMissionToken() string {
	StashMissionToken()
	stashMu.Lock()
	defer stashMu.Unlock()
	t := stashedToken
	stashedToken = ""
	return t
}

// newTokenPipe returns a pipe holding token and a newline with the write end
// already closed: the token is small and fits any pipe buffer, so the write never
// blocks.
func newTokenPipe(token string) (*os.File, error) {
	if len(token)+1 > missionTokenMax {
		return nil, errors.New("mission token too long")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	_, werr := w.WriteString(token + "\n")
	if cerr := w.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		r.Close()
		return nil, werr
	}
	return r, nil
}
