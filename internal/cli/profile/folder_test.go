package profile

import (
	"os"
	"path/filepath"
	"testing"
)

// TestValidateFolderChoiceAllowsNonEmptyFolders is a regression test for
// letting CreateProfile/MoveProfileFolder point a profile at an existing,
// non-empty folder (e.g. a coding project already initialized with
// `monomind init`) instead of requiring an empty one — EnsureLayout only
// ever adds .monoagent/ and .monomind/ alongside whatever is already there.
func TestValidateFolderChoiceAllowsNonEmptyFolders(t *testing.T) {
	t.Parallel()
	t.Run("relative path rejected", func(t *testing.T) {
		if err := validateFolderChoice("relative/path"); err == nil {
			t.Fatal("expected an error for a relative path, got nil")
		}
	})

	t.Run("path that doesn't exist yet is fine", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist-yet")
		if err := validateFolderChoice(dir); err != nil {
			t.Fatalf("expected nil for a not-yet-created folder, got: %v", err)
		}
	})

	t.Run("existing empty folder is fine", func(t *testing.T) {
		dir := t.TempDir()
		if err := validateFolderChoice(dir); err != nil {
			t.Fatalf("expected nil for an empty folder, got: %v", err)
		}
	})

	t.Run("existing folder with unrelated files is now allowed", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "src"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := validateFolderChoice(dir); err != nil {
			t.Fatalf("expected a non-empty folder with unrelated content to be allowed, got: %v", err)
		}
	})

	t.Run("existing folder already initialized with monomind is fine", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".monomind"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".monomind", "config.yaml"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := validateFolderChoice(dir); err != nil {
			t.Fatalf("expected an already-monomind-initialized folder to be allowed, got: %v", err)
		}
	})

	t.Run("path that is a file, not a folder, is rejected", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "notadir")
		if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := validateFolderChoice(file); err == nil {
			t.Fatal("expected an error when the chosen path is a file, got nil")
		}
	})

	t.Run("a plain file named .monoagent is rejected", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".monoagent"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := validateFolderChoice(dir); err == nil {
			t.Fatal("expected an error when \".monoagent\" already exists as a file, got nil")
		}
	})

	t.Run("a plain file named .monomind is rejected", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".monomind"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := validateFolderChoice(dir); err == nil {
			t.Fatal("expected an error when \".monomind\" already exists as a file, got nil")
		}
	})
}
