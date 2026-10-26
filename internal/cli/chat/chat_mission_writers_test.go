package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func loadSettings(t *testing.T, raw string) coderSettings {
	t.Helper()
	var s coderSettings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	return s.withDefaults()
}

// A confirmed mission whose operator never set --org-writers runs isolated writers in a git
// work tree; an explicit shared or isolated stays as set, and without a repository it is shared.
func TestMissionWritersDefaultsToIsolatedOnlyInAGitRepoAndOnlyWhenUnset(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo, plain := t.TempDir(), t.TempDir()
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	unborn := t.TempDir()
	gitIn(t, unborn, "init", "-q")
	m := missionStoreForTest(t)
	confirmed := &dynorg.MissionConfig{Store: m, Access: "coding", Confirmed: true}
	unconfirmed := &dynorg.MissionConfig{Store: m, Access: "coding"}
	for _, tc := range []struct {
		name string
		raw  string
		m    *dynorg.MissionConfig
		cwd  string
		want string
	}{
		{"unset, git repo", `{}`, confirmed, repo, dynorg.WritersIsolated},
		{"unset, no repo", `{}`, confirmed, plain, dynorg.WritersShared},
		{"unset, repo without a commit", `{}`, confirmed, unborn, dynorg.WritersShared},
		{"unset, empty cwd", `{}`, confirmed, "", dynorg.WritersShared},
		{"explicit shared, git repo", `{"orgWriters":"shared"}`, confirmed, repo, dynorg.WritersShared},
		{"explicit isolated, no repo", `{"orgWriters":"isolated"}`, confirmed, plain, dynorg.WritersIsolated},
		{"explicit isolated, git repo", `{"orgWriters":"isolated"}`, confirmed, repo, dynorg.WritersIsolated},
		{"unset, not a mission", `{}`, nil, repo, dynorg.WritersShared},
		{"unset, unconfirmed", `{}`, unconfirmed, repo, dynorg.WritersShared},
	} {
		if got := missionWriters(context.Background(), loadSettings(t, tc.raw), tc.m, tc.cwd); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Saving the settings after an unrelated `coder set` must not turn the unset default into
// the operator's explicit shared: that would switch the mission default off silently.
func TestCoderSetOfAnotherFlagKeepsOrgWritersUnset(t *testing.T) {
	withCoderCaps(t)
	dbPath := newChatCLITestDB(t)
	if _, code := runCoderCLI(t, dbPath, "set", "--timeout", "30m"); code != 0 {
		t.Fatalf("set timeout: exit %d", code)
	}
	if s := storedCoderSettings(t, dbPath); !s.orgWritersDefaulted {
		t.Fatalf("an unrelated set made org-writers explicit: %+v", s)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--org-writers", "shared"); code != 0 {
		t.Fatalf("set org-writers: exit %d", code)
	}
	if s := storedCoderSettings(t, dbPath); s.orgWritersDefaulted || s.OrgWriters != dynorg.WritersShared {
		t.Fatalf("an explicit shared did not stick: %+v", s)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--timeout", "40m"); code != 0 {
		t.Fatal("set timeout again")
	}
	if s := storedCoderSettings(t, dbPath); s.orgWritersDefaulted {
		t.Fatalf("an explicit shared was lost by another set: %+v", s)
	}
}

func storedCoderSettings(t *testing.T, dbPath string) coderSettings {
	t.Helper()
	db, err := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}).OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := loadCoderSettings(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The flag stays the operator's: an agent (or anything that is not the user) is refused,
// and the stored setting does not change.
func TestOrgWritersStaysOperatorOnly(t *testing.T) {
	withCoderCaps(t)
	listed := false
	for _, o := range operatorOrgFlags {
		listed = listed || o.name == "org-writers"
	}
	if !listed {
		t.Fatal("org-writers is no longer an operator-only coder flag")
	}
	dbPath := newChatCLITestDB(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	cfg.RequireOperator = func(what string) error { return clicore.OperatorOnly("only the user can %s", what) }
	cmd := newCoderCmd(cfg)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"set", "--org-writers", "isolated"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); exitCodeFor(err) != 3 {
		t.Fatalf("an agent set org-writers: %v", err)
	}
	if s := storedCoderSettings(t, dbPath); s.OrgWriters != dynorg.WritersShared || !s.orgWritersDefaulted {
		t.Fatalf("a refused set changed the setting: %+v", s)
	}
}
