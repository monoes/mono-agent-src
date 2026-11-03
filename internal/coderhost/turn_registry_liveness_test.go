package coderhost

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// setLiveness replaces the two probes hostIsGone makes of a pid for the rest of the test. It reads and writes the
// variables on the test goroutine only, before any gate call starts (a goroutine that outlives the test must not
// race the restore).
func setLiveness(t *testing.T, stamp func(int) string, signal func(int) error) {
	t.Helper()
	prevStamp, prevSignal := procStamp, signalPID
	procStamp, signalPID = stamp, signal
	t.Cleanup(func() { procStamp, signalPID = prevStamp, prevSignal })
}

func noStamp(int) string { return "" }

func unreadableEntry(t *testing.T, pid int, stamp string) string {
	t.Helper()
	dir := registryDirOf(t)
	junk := filepath.Join(t.TempDir(), "junk.db")
	writeEntry(t, junk, strings.Repeat("not a database ", 500), 0o600)
	return registerRaw(t, dir, canonicalOrSelf(t, junk), pid, stamp)
}

func TestHostIsGoneJudgesOnlyWhatItCanShow(t *testing.T) {
	e := registryEntry{PID: 4242, Stamp: "s1"}
	cases := []struct {
		name   string
		stamp  string
		signal error
		gone   bool
	}{
		{"same start stamp: alive", "s1", os.ErrProcessDone, false},
		{"different start stamp: reused pid", "s2", nil, true},
		{"no stamp, signal says done: gone", "", os.ErrProcessDone, true},
		{"no stamp, a joined done error: gone", "", errors.Join(errors.New("x"), os.ErrProcessDone), true},
		{"no stamp, signal ok: alive", "", nil, false},
		{"no stamp, EPERM: unjudgeable, not gone", "", syscall.EPERM, false},
		{"no stamp, ESRCH raw errno: not os.ErrProcessDone, not gone", "", syscall.ESRCH, false},
		{"no stamp, lookup failure: not gone", "", errors.New("find process 4242: boom"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setLiveness(t, func(int) string { return c.stamp }, func(int) error { return c.signal })
			if got := hostIsGone(e); got != c.gone {
				t.Fatalf("hostIsGone = %v, want %v", got, c.gone)
			}
		})
	}
	t.Run("entry without recorded stamp or pid is never gone", func(t *testing.T) {
		setLiveness(t, noStamp, func(int) error { return os.ErrProcessDone })
		if hostIsGone(registryEntry{PID: 4242}) || hostIsGone(registryEntry{PID: 0, Stamp: "s1"}) {
			t.Fatal("judged gone without evidence")
		}
	})
}

// (1) a signal error that is not "process done" (EPERM) must keep an unreadable registered database refusing.
func TestAnUnjudgeablePidKeepsAnUnreadableDatabaseRefusing(t *testing.T) {
	needAncestry(t)
	file := unreadableEntry(t, 4242, "s1")
	setLiveness(t, noStamp, func(int) error { return syscall.EPERM })
	in, err := gateWithin(t, 6*time.Second)
	if in || err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("an unjudgeable host must refuse: %v %v", in, err)
	}
}

// (2) control: a pid the system reports as done is gone, so its unreadable database is skipped.
func TestADonePidSkipsItsUnreadableDatabase(t *testing.T) {
	needAncestry(t)
	unreadableEntry(t, 4242, "s1")
	setLiveness(t, noStamp, func(int) error { return os.ErrProcessDone })
	if in, err := gateWithin(t, 6*time.Second); in || err != nil {
		t.Fatalf("a gone host's unreadable database must be skipped: %v %v", in, err)
	}
}

// (3) a live pid whose start stamp cannot be read is not judged dead.
func TestALivePidWithAnUnreadableStampKeepsItsUnreadableDatabaseRefusing(t *testing.T) {
	needAncestry(t)
	file := unreadableEntry(t, 4242, "s1")
	setLiveness(t, noStamp, func(int) error { return nil })
	in, err := gateWithin(t, 6*time.Second)
	if in || err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("a live host must refuse: %v %v", in, err)
	}
}
