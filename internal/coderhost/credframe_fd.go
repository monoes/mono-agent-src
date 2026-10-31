package coderhost

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

// A turn launched with peer-credential binding on receives its credentials as one typed frame (procbind.CredFrame)
// on an inherited pipe, announced by CredFDEnv: a marker distinct from MissionFDEnv, set only by that launch. The
// turn picks its reader by the marker, never by looking at the bytes, so a legacy mission token can never be parsed
// as a frame or the reverse. With the setting off the marker is absent and nothing here runs.
//
// The turn reads the frame before anything else (StashCredFrame is the first thing main's run does, before flag
// parsing, config, the account check or any database open) and treats a missing, short, late or malformed frame as
// fatal: the process must not go on without the credentials its launch promised.

// CredFDEnv names the descriptor the frame pipe is on. It holds a number, never a credential.
const CredFDEnv = "MONOAGENT_CRED_FD"

// credFrameTimeout is how long the turn waits for the whole frame and its end; the host writes it right after it
// has committed, so only a wedged host makes it matter.
var credFrameTimeout = 10 * time.Second

// ErrCredFrameFatal is every reason the turn cannot go on; the text names the cause and never the bytes.
var ErrCredFrameFatal = errors.New("the credential frame from the coder host is missing or malformed")

// StashCredFrame reads the frame named by CredFDEnv, if the marker is set. It returns nil when there is no marker
// (a legacy or unlaunched process), and an error, after closing the descriptor, for anything else that is not a good
// frame. A good frame is applied: the session token goes into the environment (the turn's own shells and tools
// present it), and the task token or the mission confirmation is kept for TakeMissionToken. A frame that carries
// both a task token and a mission confirmation is refused: a turn never has both.
func StashCredFrame() error {
	v, marked := os.LookupEnv(CredFDEnv)
	if !marked {
		return nil
	}
	// A marker that is present at all means the launch promised a frame, even if its value is empty or junk: that is
	// fatal, never "unmarked".
	_ = os.Unsetenv(CredFDEnv)
	n, perr := strconv.ParseUint(v, 10, 32)
	var f *os.File
	if perr == nil && n >= 3 {
		var oerr error
		if f, oerr = openInherited(uintptr(n)); oerr != nil {
			f = nil
		}
	}
	if _, legacy := os.LookupEnv(MissionFDEnv); legacy { // two pipes: the launch is wrong, and the legacy one must be closed too
		StashMissionToken()
		TakeMissionToken()
		if f != nil {
			f.Close()
		}
		return fmt.Errorf("%w: both pipes were given", ErrCredFrameFatal)
	}
	if f == nil {
		return fmt.Errorf("%w: the pipe is not usable", ErrCredFrameFatal)
	}
	defer f.Close()
	if !isPipe(f) {
		return fmt.Errorf("%w: the descriptor is not a pipe", ErrCredFrameFatal)
	}
	type res struct {
		f   procbind.CredFrame
		err error
	}
	ch := make(chan res, 1)
	go func() {
		fr, err := procbind.ReadCredFrame(f)
		ch <- res{fr, err}
	}()
	var fr procbind.CredFrame
	select {
	case x := <-ch:
		if x.err != nil {
			return fmt.Errorf("%w: it did not parse", ErrCredFrameFatal)
		}
		fr = x.f
	case <-time.After(credFrameTimeout):
		return fmt.Errorf("%w: it did not arrive in time", ErrCredFrameFatal)
	}
	if fr.TaskToken != nil && fr.MissionConfirmation != nil {
		return fmt.Errorf("%w: a task token and a mission confirmation together", ErrCredFrameFatal)
	}
	if fr.SessionToken != nil && !credText(fr.SessionToken, procbind.SessionPrefixV2, 128) {
		return fmt.Errorf("%w: the session token is not one", ErrCredFrameFatal)
	}
	if fr.TaskToken != nil && !credText(fr.TaskToken, procbind.TaskPrefixV2, missionTokenMax-1) {
		return fmt.Errorf("%w: the task token is not one", ErrCredFrameFatal)
	}
	if fr.MissionConfirmation != nil {
		tok := string(fr.MissionConfirmation)
		if _, _, ok := parseToken(tok); !ok || !credText(fr.MissionConfirmation, "", missionTokenMax-1) {
			return fmt.Errorf("%w: the mission confirmation is not one", ErrCredFrameFatal)
		}
	}
	defer fr.Zero() // the frame's own copies are cleared once applied (the environment and the stash hold the strings)
	if fr.SessionToken != nil {
		if err := os.Setenv(SessionTokenEnv, string(fr.SessionToken)); err != nil {
			return fmt.Errorf("%w: the session token could not be kept", ErrCredFrameFatal)
		}
	}
	switch {
	case fr.TaskToken != nil:
		stashSet(string(fr.TaskToken))
	case fr.MissionConfirmation != nil:
		stashSet(string(fr.MissionConfirmation))
	}
	return nil
}

// credText says whether b is printable ASCII without spaces, at most max bytes, and starts with prefix.
func credText(b []byte, prefix string, max int) bool {
	if len(b) == 0 || len(b) > max || !strings.HasPrefix(string(b), prefix) {
		return false
	}
	for _, c := range b {
		if c <= ' ' || c >= 0x7f {
			return false
		}
	}
	return true
}

func stashSet(tok string) {
	stashMu.Lock()
	stashedToken = tok
	stashMu.Unlock()
}
