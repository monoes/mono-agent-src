package chat

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/storage"
)

func addTestProfile(t *testing.T, dbPath, id, name, rootDir string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, root_dir) VALUES (?, ?, ?)`, id, name, rootDir); err != nil {
		t.Fatal(err)
	}
}

type defaultWorkspaceOut struct {
	Path    string `json:"path"`
	Source  string `json:"source"`
	Created bool   `json:"created"`
	Profile struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"profile"`
}

func TestCoderWorkspaceDefaultIsTheProfileFolder(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	_, argsLog := writeCoderMonomind(t, "")
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, WorkspaceRoot: filepath.Join(t.TempDir(), "never-used")})

	home, _ := os.UserHomeDir()
	out, code := runCoderCLI(t, dbPath, "workspace", "default")
	var def defaultWorkspaceOut
	decodeChatJSON(t, out, &def)
	wantDefault := filepath.Join(home, ".monoagent", "profiles", "default")
	if resolved, err := filepath.EvalSymlinks(wantDefault); err == nil {
		wantDefault = resolved
	}
	if code != 0 || def.Path != wantDefault || def.Profile.ID != "default" || def.Source != "profile" {
		t.Fatalf("default profile: exit %d %s", code, out)
	}
	if logged, _ := os.ReadFile(argsLog); !strings.Contains(string(logged), "init --project "+def.Path+" --if-missing") {
		t.Errorf("add-only init not run in the profile folder:\n%s", logged)
	}
	if _, err := os.Stat(filepath.Join(def.Path, ".git")); err == nil {
		t.Error("the profile folder must not be git-initialized")
	}

	custom, _ := filepath.EvalSymlinks(t.TempDir())
	addTestProfile(t, dbPath, "work", "Work Stuff", custom)
	os.WriteFile(filepath.Join(custom, "keep.txt"), []byte("mine"), 0o644)
	cmd := newCoderCmd(testDeps(&testConfig{DBPath: dbPath, ProfileID: "work", JSONOutput: true}))
	var buf strings.Builder
	cmd.SetOut(&buf)
	// the user's own folder: the setup's files are named and kept out of git (#1755)
	var notice strings.Builder
	cmd.SetErr(&notice)
	if _, err := exec.LookPath("git"); err == nil {
		ownRun(t, custom, "init", "-q")
	}
	prevInit := initWorkspace
	t.Cleanup(func() { initWorkspace = prevInit })
	initWorkspace = func(_ context.Context, _, d, _ string) (*monomind.WorkspaceInit, error) {
		os.WriteFile(filepath.Join(d, "CLAUDE.md"), []byte("#"), 0o644)
		return &monomind.WorkspaceInit{Root: d}, nil
	}
	cmd.SetArgs([]string{"workspace", "default"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var w defaultWorkspaceOut
	decodeChatJSON(t, buf.String(), &w)
	if w.Path != custom || w.Profile.Name != "Work Stuff" || w.Source != "profile_custom_folder" {
		t.Fatalf("custom-folder profile: %s", buf.String())
	}
	if b, _ := os.ReadFile(filepath.Join(custom, "keep.txt")); string(b) != "mine" {
		t.Error("existing file changed")
	}
	if !strings.Contains(notice.String(), "monoagent added CLAUDE.md") {
		t.Errorf("no notice for a custom profile folder: %q", notice.String())
	}
	if _, err := exec.LookPath("git"); err == nil {
		if st := ownRun(t, custom, "status", "--porcelain"); strings.Contains(st, "CLAUDE.md") || !strings.Contains(st, "keep.txt") {
			t.Errorf("git status in the custom folder:\n%s", st)
		}
	}
}

func TestChatHistoryCreateProfileFolder(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	writeCoderMonomind(t, "")
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	custom, _ := filepath.EvalSymlinks(t.TempDir())
	addTestProfile(t, dbPath, "work", "Work Stuff", custom)

	out, code := runChatHistory(t, dbPath, "work", "create", "--runtime", "claude", "--mode", "coder", "--profile-folder")
	var rec ai.ConversationRecord
	decodeChatJSON(t, out, &rec)
	if code != 0 || rec.Cwd != custom {
		t.Fatalf("create --profile-folder: exit %d %s", code, out)
	}
	if out, code := runChatHistory(t, dbPath, "work", "create", "--runtime", "claude", "--mode", "coder", "--profile-folder", "--cwd", custom); code != 3 {
		t.Errorf("--profile-folder with --cwd: exit %d %s", code, out)
	}
}
