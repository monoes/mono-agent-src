package testhome

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoveHomeRetriesUntilItSucceeds(t *testing.T) {
	calls, sleeps := 0, 0
	var out bytes.Buffer
	removeHome("h", func(string) error {
		calls++
		if calls < 3 {
			return errors.New("busy")
		}
		return nil
	}, func(time.Duration) { sleeps++ }, &out)
	if calls != 3 || sleeps != 2 || out.Len() != 0 {
		t.Fatalf("calls=%d sleeps=%d note=%q, want 3, 2 and no note", calls, sleeps, out.String())
	}
}

func TestRemoveHomeGivesUpWithANote(t *testing.T) {
	calls := 0
	var slept time.Duration
	var out bytes.Buffer
	removeHome("the-home", func(string) error { calls++; return errors.New("in use") },
		func(d time.Duration) { slept += d }, &out)
	if calls != removeAttempts || slept > 5*time.Second {
		t.Fatalf("calls=%d slept=%v, want %d attempts within a bounded wait", calls, slept, removeAttempts)
	}
	want := "testhome: could not remove the-home: in use\n"
	if out.String() != want {
		t.Fatalf("note = %q, want %q", out.String(), want)
	}
}

// The real removal does not follow a link out of the home.
func TestRemoveHomeDoesNotFollowLinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	home := filepath.Join(root, "home")
	for _, d := range []string{outside, home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "link")); err != nil {
		t.Skipf("no symlinks: %v", err)
	}
	var out bytes.Buffer
	removeHome(home, os.RemoveAll, time.Sleep, &out)
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("home still there: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("the link was followed: %v", err)
	}
}

func TestLeaveHomeMovesTheWorkingDirOut(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	sub := filepath.Join(home, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	leaveHome(home)
	cwd, _ := os.Getwd()
	if inside(cwd, home) {
		t.Fatalf("cwd %s is still inside %s", cwd, home)
	}
	// a working directory elsewhere is left alone
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(other)
	leaveHome(home)
	if cwd, _ := os.Getwd(); !strings.HasSuffix(cwd, "other") {
		t.Fatalf("cwd moved to %s", cwd)
	}
}
