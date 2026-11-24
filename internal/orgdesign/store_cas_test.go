package orgdesign

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A Doc saves only over the bytes it was loaded from: a second writer that
// loaded the same file does not silently undo the first one's edit.
func TestSaveRefusesAFileChangedSinceLoad(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, NewOrg("cas-org", "goal", NewOrgOptions{})); err != nil {
		t.Fatal(err)
	}
	a, err := Load(dir, "cas-org")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(dir, "cas-org")
	if err != nil {
		t.Fatal(err)
	}
	a.Goal = "first writer"
	if _, err := Save(dir, a); err != nil {
		t.Fatal(err)
	}
	b.Goal = "second writer"
	if _, err := Save(dir, b); !errors.Is(err, ErrConcurrentEdit) {
		t.Fatalf("stale save: %v", err)
	}
	got, _ := Load(dir, "cas-org")
	if got.Goal != "first writer" {
		t.Fatalf("goal = %q", got.Goal)
	}
	// Saving again from the Doc Save just updated is fine.
	a.Goal = "first writer again"
	if _, err := Save(dir, a); err != nil {
		t.Fatalf("save of a current doc: %v", err)
	}
}

// Concurrent writers never share a temp file: every Save either lands or is
// refused whole, the file stays one valid document and no temp file is left.
func TestSaveConcurrentWritersLeaveNoTornFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, NewOrg("race-org", "goal", NewOrgOptions{})); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		d, err := Load(dir, "race-org")
		if err != nil {
			t.Fatal(err)
		}
		d.Goal = "writer " + strings.Repeat("x", 4000+i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Save(dir, d); err != nil && !errors.Is(err, ErrConcurrentEdit) {
				t.Errorf("save: %v", err)
			}
		}()
	}
	wg.Wait()
	if _, err := Load(dir, "race-org"); err != nil {
		t.Fatalf("torn file: %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(OrgsDir(dir), "*.tmp"))
	if len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	if _, err := os.Stat(filepath.Join(OrgsDir(dir), "race-org.json")); err != nil {
		t.Fatal(err)
	}
}
