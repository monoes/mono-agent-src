package profile

import (
	"os"
	"path/filepath"
	"testing"
)

// makeMonomindProject creates dir/.monomind/config.yaml so
// isMonomindProject(dir) reports true — the same on-disk marker
// monomind's own CLI uses.
func makeMonomindProject(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".monomind"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".monomind", "config.yaml"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestFilterMonomindProjects covers the suggestion-list filtering rules:
// the real ~/.monomind-projects.json on a real machine is very noisy (temp
// test dirs from monomind's own test suite, one-off /tmp paths, monoagent's
// own profile folders) — filterMonomindProjects is what turns that into a
// small, meaningful suggestion list.
func TestFilterMonomindProjects(t *testing.T) {
	t.Parallel()
	base := t.TempDir()

	realProject := filepath.Join(base, "real-project")
	makeMonomindProject(t, realProject)

	alreadyAProfile := filepath.Join(base, "already-a-profile")
	makeMonomindProject(t, alreadyAProfile)

	neverInitialized := filepath.Join(base, "never-initialized")
	if err := os.MkdirAll(neverInitialized, 0755); err != nil {
		t.Fatal(err)
	}

	deletedProject := filepath.Join(base, "deleted-project") // never created on disk

	t.Run("keeps only paths that exist and are currently monomind-initialized", func(t *testing.T) {
		got := filterMonomindProjects(
			[]string{realProject, neverInitialized, deletedProject},
			map[string]bool{},
		)
		if len(got) != 1 {
			t.Fatalf("got %d projects, want 1: %+v", len(got), got)
		}
		if got[0].Path != realProject || got[0].Name != "real-project" {
			t.Fatalf("got %+v, want Path=%q Name=%q", got[0], realProject, "real-project")
		}
	})

	t.Run("excludes a path already used as a profile's root_dir", func(t *testing.T) {
		got := filterMonomindProjects(
			[]string{realProject, alreadyAProfile},
			map[string]bool{alreadyAProfile: true},
		)
		if len(got) != 1 || got[0].Path != realProject {
			t.Fatalf("got %+v, want only %q", got, realProject)
		}
	})

	t.Run("dedupes exact-duplicate paths, keeping the first occurrence's order", func(t *testing.T) {
		second := filepath.Join(base, "second-project")
		makeMonomindProject(t, second)

		got := filterMonomindProjects(
			[]string{realProject, second, realProject},
			map[string]bool{},
		)
		if len(got) != 2 {
			t.Fatalf("got %d projects, want 2 (deduped): %+v", len(got), got)
		}
		if got[0].Path != realProject || got[1].Path != second {
			t.Fatalf("got order %+v, want [%q, %q]", got, realProject, second)
		}
	})

	t.Run("empty input yields an empty (non-nil-relevant) slice", func(t *testing.T) {
		got := filterMonomindProjects(nil, map[string]bool{})
		if len(got) != 0 {
			t.Fatalf("got %d projects, want 0", len(got))
		}
	})
}
