//go:build !windows

package coderhost

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/monoes/mono-agent/internal/procbind"
)

// credPipe makes a pipe holding b, with the read end on a descriptor the test announces in CredFDEnv. The write end
// is closed after the write unless keepOpen (a writer that never finishes).
func credPipe(t *testing.T, b []byte, keepOpen bool) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if len(b) > 0 {
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if keepOpen {
		t.Cleanup(func() { w.Close() })
	} else {
		w.Close()
	}
	t.Setenv(CredFDEnv, strconv.Itoa(fd))
}

func resetStash(t *testing.T) {
	t.Helper()
	stashSet("")
	t.Setenv(SessionTokenEnv, "")
	os.Unsetenv(SessionTokenEnv)
	t.Cleanup(func() { stashSet(""); os.Unsetenv(SessionTokenEnv) })
}

var (
	goodSession = []byte("mst-" + strings.Repeat("a", 43))
	goodTask    = []byte("mat-" + strings.Repeat("b", 40))
	goodConfirm = []byte("12." + strings.Repeat("c", 22))
)

func frameOf(t *testing.T, f procbind.CredFrame) []byte {
	t.Helper()
	b, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAGoodFrameIsAppliedAndTheMarkerIsGone(t *testing.T) {
	for name, c := range map[string]struct {
		f       procbind.CredFrame
		session string
		taken   string
	}{
		"session only":        {procbind.CredFrame{SessionToken: goodSession}, string(goodSession), ""},
		"session and task":    {procbind.CredFrame{SessionToken: goodSession, TaskToken: goodTask}, string(goodSession), string(goodTask)},
		"session and mission": {procbind.CredFrame{SessionToken: goodSession, MissionConfirmation: goodConfirm}, string(goodSession), string(goodConfirm)},
		"mission only":        {procbind.CredFrame{MissionConfirmation: goodConfirm}, "", string(goodConfirm)},
	} {
		resetStash(t)
		credPipe(t, frameOf(t, c.f), false)
		if err := StashCredFrame(); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if os.Getenv(CredFDEnv) != "" {
			t.Errorf("%s: the marker stayed in the environment", name)
		}
		if got := os.Getenv(SessionTokenEnv); got != c.session {
			t.Errorf("%s: session token env %q, want %q", name, got, c.session)
		}
		if got := TakeMissionToken(); got != c.taken {
			t.Errorf("%s: TakeMissionToken %q, want %q", name, got, c.taken)
		}
	}
}

// Without the marker nothing happens: the environment and the stash are untouched (the setting-off path).
func TestWithoutTheMarkerNothingHappens(t *testing.T) {
	resetStash(t)
	t.Setenv(SessionTokenEnv, "mst-already")
	if err := StashCredFrame(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv(SessionTokenEnv) != "mst-already" || TakeMissionToken() != "" {
		t.Error("a process without the marker was changed")
	}
}

// Every way the launch can be wrong is fatal, leaves no credential behind and still removes the marker.
func TestABadFrameIsFatalAndLeavesNothing(t *testing.T) {
	good := frameOf(t, procbind.CredFrame{SessionToken: goodSession, TaskToken: goodTask})
	both := frameOf(t, procbind.CredFrame{TaskToken: goodTask, MissionConfirmation: goodConfirm})
	badSession := frameOf(t, procbind.CredFrame{SessionToken: []byte("not-a-session-token")})
	badTask := frameOf(t, procbind.CredFrame{TaskToken: []byte("tok with a space")})
	badConfirm := frameOf(t, procbind.CredFrame{MissionConfirmation: []byte("no-dot-confirmation")})
	for name, b := range map[string][]byte{
		"nothing":                 nil,
		"truncated":               good[:len(good)-3],
		"trailing byte":           append(append([]byte(nil), good...), 'x'),
		"garbage":                 []byte(strings.Repeat("x", 80)),
		"a legacy token line":     []byte("12." + strings.Repeat("c", 22) + "\n"),
		"task and confirmation":   both,
		"a session token of junk": badSession,
		"a task token with space": badTask,
		"a confirmation not one":  badConfirm,
	} {
		resetStash(t)
		credPipe(t, b, false)
		err := StashCredFrame()
		if err == nil || !strings.Contains(err.Error(), ErrCredFrameFatal.Error()) {
			t.Errorf("%s: not fatal: %v", name, err)
		}
		if os.Getenv(CredFDEnv) != "" {
			t.Errorf("%s: the marker stayed", name)
		}
		if os.Getenv(SessionTokenEnv) != "" || TakeMissionToken() != "" {
			t.Errorf("%s: a credential was kept", name)
		}
		for _, secret := range []string{string(goodSession), string(goodTask), string(goodConfirm)} {
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error names a credential", name)
			}
		}
	}
}

// A writer that never finishes: the turn gives up at the deadline and does not hang.
func TestAFrameThatNeverArrivesIsFatalAtTheDeadline(t *testing.T) {
	resetStash(t)
	old := credFrameTimeout
	credFrameTimeout = 150 * time.Millisecond
	t.Cleanup(func() { credFrameTimeout = old })
	credPipe(t, nil, true)
	start := time.Now()
	err := StashCredFrame()
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
	// the frame arrives but the stream is not closed: the end is required too
	resetStash(t)
	credPipe(t, frameOf(t, procbind.CredFrame{SessionToken: goodSession}), true)
	if err := StashCredFrame(); err == nil {
		t.Error("a frame without the end of the stream was accepted")
	}
}

// Both pipes at once is a wrong launch: fatal, and the legacy one is read and closed too.
func TestBothMarkersAreFatal(t *testing.T) {
	resetStash(t)
	credPipe(t, frameOf(t, procbind.CredFrame{SessionToken: goodSession}), false)
	r, err := newTokenPipe(string(goodConfirm))
	if err != nil {
		t.Fatal(err)
	}
	fd, _ := unix.Dup(int(r.Fd()))
	r.Close()
	t.Setenv(MissionFDEnv, strconv.Itoa(fd))
	if err := StashCredFrame(); err == nil {
		t.Fatal("two pipes were accepted")
	}
	if os.Getenv(MissionFDEnv) != "" || os.Getenv(SessionTokenEnv) != "" || TakeMissionToken() != "" {
		t.Error("something of either pipe was kept")
	}
}

func TestAMarkerThatIsNotAPipeIsFatal(t *testing.T) {
	resetStash(t)
	// a regular file that holds a perfectly good frame: only the pipe-type guard refuses it
	f, err := os.CreateTemp(t.TempDir(), "notapipe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(frameOf(t, procbind.CredFrame{SessionToken: goodSession})); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	fd, _ := unix.Dup(int(f.Fd()))
	f.Close()
	t.Setenv(CredFDEnv, strconv.Itoa(fd))
	if err := StashCredFrame(); err == nil {
		t.Error("a regular file holding a valid frame was accepted as the frame pipe")
	}
	if os.Getenv(SessionTokenEnv) != "" || TakeMissionToken() != "" {
		t.Error("a credential was taken from a file")
	}
	for _, v := range []string{"x", "1", "-4", "99999999999"} {
		t.Setenv(CredFDEnv, v)
		if err := StashCredFrame(); err == nil {
			t.Errorf("marker %q was accepted", v)
		}
	}
}

// A marker that is present but empty is a launch that promised a frame and did not deliver one: fatal, the marker is
// removed, and a legacy marker next to it (even an empty one) is the both-pipes error, with the legacy pipe consumed.
func TestAnEmptyMarkerIsFatalNotUnmarked(t *testing.T) {
	resetStash(t)
	t.Setenv(CredFDEnv, "")
	if err := StashCredFrame(); err == nil {
		t.Fatal("an empty marker was treated as unmarked")
	}
	if _, still := os.LookupEnv(CredFDEnv); still {
		t.Error("the empty marker stayed in the environment")
	}
	// empty cred marker + a legacy pipe that holds a token: both-markers error, the legacy token is consumed
	resetStash(t)
	t.Setenv(CredFDEnv, "")
	r, err := newTokenPipe(string(goodConfirm))
	if err != nil {
		t.Fatal(err)
	}
	fd, _ := unix.Dup(int(r.Fd()))
	r.Close()
	t.Setenv(MissionFDEnv, strconv.Itoa(fd))
	if err := StashCredFrame(); err == nil {
		t.Fatal("an empty frame marker next to a legacy pipe was accepted")
	}
	if _, still := os.LookupEnv(MissionFDEnv); still || TakeMissionToken() != "" {
		t.Error("the legacy pipe was left in place")
	}
	// a valid frame marker next to an EMPTY legacy marker is the both-markers error too
	resetStash(t)
	credPipe(t, frameOf(t, procbind.CredFrame{SessionToken: goodSession}), false)
	t.Setenv(MissionFDEnv, "")
	if err := StashCredFrame(); err == nil {
		t.Fatal("a frame marker next to an empty legacy marker was accepted")
	}
	if os.Getenv(SessionTokenEnv) != "" {
		t.Error("a credential was applied")
	}
}
