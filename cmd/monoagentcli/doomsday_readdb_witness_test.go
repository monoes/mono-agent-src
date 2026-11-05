//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const committedRecord = `{"t":"committed","gen":1,"to":3}`

func witnessCfg(t *testing.T, profile string) *globalConfig {
	cfg := sendEnv(t)
	cfg.DBPath = filepath.Join(t.TempDir(), "monoagent.db")
	cfg.ProfileID = profile
	return cfg
}

// within runs f and fails the test, instead of hanging, when it does not return in time.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s", what, d)
	}
}

// A FIFO (or a link) swapped in after the path check can neither stall the open nor be followed.
func TestWitnessOpenDoesNotBlockOnAFIFOOrFollowALink(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no fifo here")
	}
	within(t, 3*time.Second, "opening a FIFO", func() {
		if f, err := openWitnessNoFollow(fifo); err == nil {
			st, serr := f.Stat()
			f.Close()
			if serr == nil && st.Mode().IsRegular() {
				t.Error("a FIFO looked like a regular file")
			}
		}
	})
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte(committedRecord), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here")
	}
	if f, err := openWitnessNoFollow(link); err == nil {
		f.Close()
		t.Fatal("a symlink was followed")
	}
}

// A FIFO standing where the witness is never blocks the look and never counts as a record.
func TestWitnessHoldsRunIgnoresAFIFO(t *testing.T) {
	cfg := witnessCfg(t, "p1")
	if err := os.MkdirAll(doomsdayRunDirPath(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(doomsdayRunDirPath(cfg), "witness"), 0o600); err != nil {
		t.Skip("no fifo here")
	}
	within(t, 3*time.Second, "witnessHoldsRun on a FIFO", func() {
		held, err := witnessHoldsRun(cfg)
		if held || !errors.Is(err, errWitnessUnreadable) {
			t.Errorf("a FIFO where the witness is: held %v, err %v, want an unreadable error (never off)", held, err)
		}
	})
}

// A linked run directory (or a linked parent) is not followed to another profile's witness.
func TestWitnessHoldsRunDoesNotFollowALinkedDirectory(t *testing.T) {
	cfg := witnessCfg(t, "p1")
	writeWitness(t, cfg, committedRecord)
	if held, err := witnessHoldsRun(cfg); !held || err != nil {
		t.Fatalf("control: the real run directory must count: %v %v", held, err)
	}
	linked := *cfg
	linked.ProfileID = "p3"
	if err := os.Symlink(doomsdayRunDirPath(cfg), doomsdayRunDirPath(&linked)); err != nil {
		t.Skip("no symlinks here")
	}
	if held, err := witnessHoldsRun(&linked); held || !errors.Is(err, errWitnessUnreadable) {
		t.Fatalf("a symlinked run directory: held %v, err %v, want an unreadable error (not followed, and never off)", held, err)
	}
	// The parent ("doomsday") as a link to a real one.
	viaLink := witnessCfg(t, "p1")
	if err := os.Symlink(filepath.Dir(doomsdayRunDirPath(cfg)), filepath.Join(filepath.Dir(viaLink.DBPath), "doomsday")); err != nil {
		t.Fatal(err)
	}
	if held, err := witnessHoldsRun(viaLink); held || !errors.Is(err, errWitnessUnreadable) {
		t.Fatalf("a symlinked doomsday directory: held %v, err %v, want an unreadable error (not followed, and never off)", held, err)
	}
}

// The file is read up to a cap: a record past the cap is not seen, one within it is.
func TestWitnessReadIsCapped(t *testing.T) {
	cfg := witnessCfg(t, "p1")
	p := writeWitness(t, cfg, "")
	pad := strings.Repeat("x", witnessReadCap)
	if err := os.WriteFile(p, []byte(pad+committedRecord), 0o600); err != nil {
		t.Fatal(err)
	}
	if held, err := witnessHoldsRun(cfg); held || !errors.Is(err, errWitnessUnreadable) {
		t.Fatalf("a record beyond the 1 MiB cap: held %v, err %v, want an unreadable error (cannot tell, never off)", held, err)
	}
	if err := os.WriteFile(p, []byte(committedRecord+pad), 0o600); err != nil {
		t.Fatal(err)
	}
	if held, err := witnessHoldsRun(cfg); !held || err != nil {
		t.Fatalf("a record within the cap was missed: %v %v", held, err)
	}
}

// A profile id that is not one plain name never looks at a witness, even with files planted at every traversal target.
func TestWitnessHoldsRunIdGuard(t *testing.T) {
	cfg := witnessCfg(t, "p1")
	base := filepath.Dir(cfg.DBPath)
	rec := []byte(committedRecord)
	for _, f := range []string{
		filepath.Join(base, "witness"),                       // ".."
		filepath.Join(base, "doomsday", "witness"),           // "."
		filepath.Join(base, "doomsday", "a", "b", "witness"), // "a/b"
		filepath.Join(base, "p1", "witness"),                 // "../p1"
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, rec, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeWitness(t, cfg, committedRecord) // doomsday/p1/witness: the only legitimate one
	if held, err := witnessHoldsRun(cfg); !held || err != nil {
		t.Fatalf("control: p1 must count: %v %v", held, err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "../p1", "p1/", "\u2025/p1", "p\uff11"} {
		c := *cfg
		c.ProfileID = bad
		if held, err := witnessHoldsRun(&c); held || err != nil {
			t.Errorf("profile id %q looked into a witness: %v %v", bad, held, err)
		}
	}
}

// Everything beyond "plainly nothing there" is an error, never off: a witness that is a link, a directory, unreadable,
// or a missing run directory is plainly nothing only when it does not exist.
func TestWitnessHoldsRunOnlyAMissingPlaceIsOff(t *testing.T) {
	cfg := witnessCfg(t, "p1")
	if held, err := witnessHoldsRun(cfg); held || err != nil {
		t.Fatalf("nothing at all: %v %v, want off", held, err)
	}
	if err := os.MkdirAll(doomsdayRunDirPath(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if held, err := witnessHoldsRun(cfg); held || err != nil {
		t.Fatalf("no witness file: %v %v, want off", held, err)
	}
	if err := os.Mkdir(filepath.Join(doomsdayRunDirPath(cfg), "witness"), 0o700); err != nil {
		t.Fatal(err)
	}
	if held, err := witnessHoldsRun(cfg); held || !errors.Is(err, errWitnessUnreadable) {
		t.Fatalf("a directory where the witness is: %v %v, want an unreadable error", held, err)
	}
}
