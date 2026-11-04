//go:build doomsday

package main

import (
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
		if witnessHoldsRun(cfg) {
			t.Error("a FIFO counted as a witness record")
		}
	})
}

// A linked run directory (or a linked parent) is not followed to another profile's witness.
func TestWitnessHoldsRunDoesNotFollowALinkedDirectory(t *testing.T) {
	cfg := witnessCfg(t, "p1")
	writeWitness(t, cfg, committedRecord)
	if !witnessHoldsRun(cfg) {
		t.Fatal("control: the real run directory must count")
	}
	linked := *cfg
	linked.ProfileID = "p3"
	if err := os.Symlink(doomsdayRunDirPath(cfg), doomsdayRunDirPath(&linked)); err != nil {
		t.Skip("no symlinks here")
	}
	if witnessHoldsRun(&linked) {
		t.Fatal("a symlinked run directory was followed")
	}
	// The parent ("doomsday") as a link to a real one.
	viaLink := witnessCfg(t, "p1")
	if err := os.Symlink(filepath.Dir(doomsdayRunDirPath(cfg)), filepath.Join(filepath.Dir(viaLink.DBPath), "doomsday")); err != nil {
		t.Fatal(err)
	}
	if witnessHoldsRun(viaLink) {
		t.Fatal("a symlinked doomsday directory was followed")
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
	if witnessHoldsRun(cfg) {
		t.Fatal("a record beyond the 1 MiB cap was read")
	}
	if err := os.WriteFile(p, []byte(committedRecord+pad), 0o600); err != nil {
		t.Fatal(err)
	}
	if !witnessHoldsRun(cfg) {
		t.Fatal("a record within the cap was missed")
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
	if !witnessHoldsRun(cfg) {
		t.Fatal("control: p1 must count")
	}
	for _, bad := range []string{"", ".", "..", "a/b", "../p1", "p1/", "\u2025/p1", "p\uff11"} {
		c := *cfg
		c.ProfileID = bad
		if witnessHoldsRun(&c) {
			t.Errorf("profile id %q looked into a witness", bad)
		}
	}
}
