package attachments

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// R11-09: per-profile folders, delete with the message, retention.
func TestSaveForProfileSeparatesProfiles(t *testing.T) {
	a, err := SaveForProfile("work", "m1", "a.pdf", []byte("x"), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SaveForProfile("home", "m1", "a.pdf", []byte("y"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !strings.HasPrefix(a, ProfileDir("work")) || !strings.HasPrefix(b, ProfileDir("home")) {
		t.Fatalf("paths %s %s", a, b)
	}
}

func TestRemoveMessageDeletesOnlyThatMessage(t *testing.T) {
	keep, _ := SaveForProfile("work", "m2", "k.txt", []byte("k"), nil)
	gone, _ := SaveForProfile("work", "m1", "g.txt", []byte("g"), nil)
	if err := RemoveMessage("work", "m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("m1 file still there: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("m2 file lost: %v", err)
	}
	if err := RemoveMessage("work", "never-saved"); err != nil {
		t.Errorf("removing nothing: %v", err)
	}
}

func TestPruneRemovesOldFoldersInBothLayouts(t *testing.T) {
	oldNew, _ := SaveForProfile("work", "fresh", "f.txt", []byte("f"), nil)
	oldP, _ := SaveForProfile("work", "stale", "s.txt", []byte("s"), nil)
	oldFlat, _ := Save("flat-stale", "o.txt", []byte("o"))
	past := time.Now().Add(-400 * 24 * time.Hour)
	for _, p := range []string{oldP, oldFlat} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	n, err := Prune(DefaultRetention, time.Now())
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if _, err := os.Stat(oldNew); err != nil {
		t.Errorf("fresh file removed: %v", err)
	}
	for _, p := range []string{oldP, oldFlat} {
		if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
			t.Errorf("%s still there", p)
		}
	}
	if n, _ := Prune(0, time.Now()); n != 0 {
		t.Errorf("zero retention pruned %d", n)
	}
}
