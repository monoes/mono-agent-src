package profiledir

import (
	"database/sql"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestValidProfileID(t *testing.T) {
	valid := []string{"default", "work", "personal-2", "org_1", "a.b"}
	for _, id := range valid {
		if !ValidProfileID(id) {
			t.Errorf("ValidProfileID(%q) = false, want true", id)
		}
	}
	invalid := []string{"", "..", "a/b", `a\b`, "../evil", "evil/..", "a..b", "..a", MachineProfileID}
	for _, id := range invalid {
		if ValidProfileID(id) {
			t.Errorf("ValidProfileID(%q) = true, want false", id)
		}
	}
}

func TestRoot_DefaultJoin(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)

	got := Root(nil, "work")
	want := filepath.Join(home, ".monoagent", "profiles", "work")
	if got != want {
		t.Errorf("Root(nil, \"work\") = %q, want %q", got, want)
	}
	if VaultDir(nil, "work") != filepath.Join(want, ".monoagent", "vault") {
		t.Errorf("VaultDir mismatch: %q", VaultDir(nil, "work"))
	}
	if MonomindDir(nil, "work") != filepath.Join(want, ".monomind") {
		t.Errorf("MonomindDir mismatch: %q", MonomindDir(nil, "work"))
	}
}

// TestRoot_RejectsTraversal is the F1-7 guard: a profileID containing path
// separators or ".." must never produce a path outside the profiles root —
// Root returns the dead path "" for such IDs.
func TestRoot_RejectsTraversal(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)

	for _, id := range []string{"", "..", "../evil", "evil/..", `..\evil`, "a..b"} {
		if got := Root(nil, id); got != "" {
			t.Errorf("Root(nil, %q) = %q, want \"\" (rejected)", id, got)
		}
		// Derived dirs must not escape the home dir either.
		for name, dir := range map[string]string{
			"VaultDir":    VaultDir(nil, id),
			"MonomindDir": MonomindDir(nil, id),
		} {
			if strings.HasPrefix(dir, "..") {
				t.Errorf("%s(nil, %q) = %q escapes via relative ..", name, id, dir)
			}
		}
	}
}

func TestEnsureLayout(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)

	if err := EnsureLayout(nil, "../evil"); err == nil {
		t.Error("EnsureLayout with traversal ID should fail, got nil")
	}
	if err := EnsureLayout(nil, ""); err == nil {
		t.Error("EnsureLayout with empty ID should fail, got nil")
	}

	if err := EnsureLayout(nil, "work"); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	if !Exists(nil, "work") {
		t.Error("Exists should report true after EnsureLayout")
	}
	if Exists(nil, "../evil") {
		t.Error("Exists should report false for a rejected ID")
	}

	// Idempotent.
	if err := EnsureLayout(nil, "work"); err != nil {
		t.Fatalf("EnsureLayout (repeat): %v", err)
	}
}

// TestEnsureLayout_WritesMonoagentGitignore covers the actual reason this
// data lives under .monoagent/ at all (per the user's own request): so it
// can be gitignored as a single unit when root_dir points at a real,
// git-tracked coding project. Without a .gitignore inside .monoagent/
// itself, every profile pointed at a real repo would show it as untracked
// noise in `git status` forever.
func TestEnsureLayout_WritesMonoagentGitignore(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)

	if err := EnsureLayout(nil, "work"); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	gitignorePath := filepath.Join(Root(nil, "work"), ".monoagent", ".gitignore")
	got, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("reading .monoagent/.gitignore: %v", err)
	}
	if string(got) != "*\n" {
		t.Errorf(".monoagent/.gitignore content = %q, want %q", got, "*\n")
	}
}

// TestEnsureLayout_NeverOverwritesExistingMonoagentGitignore: a user is free
// to hand-edit or replace .monoagent/.gitignore (e.g. to un-ignore a
// specific file), and EnsureLayout runs on every startup for every profile
// — it must never clobber that customization back to the default content.
func TestEnsureLayout_NeverOverwritesExistingMonoagentGitignore(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)

	if err := EnsureLayout(nil, "work"); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	gitignorePath := filepath.Join(Root(nil, "work"), ".monoagent", ".gitignore")
	custom := "*\n!keep-this.txt\n"
	if err := os.WriteFile(gitignorePath, []byte(custom), 0600); err != nil {
		t.Fatalf("seed custom .gitignore: %v", err)
	}

	if err := EnsureLayout(nil, "work"); err != nil {
		t.Fatalf("EnsureLayout (repeat): %v", err)
	}

	got, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("reading .monoagent/.gitignore: %v", err)
	}
	if string(got) != custom {
		t.Errorf("EnsureLayout overwrote a customized .gitignore: got %q, want %q", got, custom)
	}
}

func TestIsDefaultManaged(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)

	if !IsDefaultManaged(nil, "work") {
		t.Error("nil db (no override possible) should report default-managed")
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE profiles (id TEXT PRIMARY KEY, root_dir TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO profiles (id, root_dir) VALUES ('default-profile', '')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO profiles (id, root_dir) VALUES ('custom-profile', ?)`, filepath.Join(home, "my-project")); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if !IsDefaultManaged(db, "default-profile") {
		t.Error("a profile with no root_dir override should report default-managed")
	}
	if IsDefaultManaged(db, "custom-profile") {
		t.Error("a profile with a root_dir override should NOT report default-managed")
	}
}

func TestTaxonomyFoldersHaveNonEmptyUniqueNames(t *testing.T) {
	if len(TaxonomyFolders) == 0 {
		t.Fatal("TaxonomyFolders is empty")
	}
	seen := map[string]bool{}
	for _, f := range TaxonomyFolders {
		if f.Name == "" {
			t.Errorf("TaxonomyFolder has empty Name (Purpose: %q)", f.Purpose)
		}
		if f.Purpose == "" {
			t.Errorf("TaxonomyFolder %q has empty Purpose", f.Name)
		}
		if seen[f.Name] {
			t.Errorf("duplicate TaxonomyFolder name %q", f.Name)
		}
		seen[f.Name] = true
	}
}
