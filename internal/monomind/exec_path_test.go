package monomind

import (
	"bytes"
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSelf makes the running executable look like a monoagentcli in its own
// folder, next to a node that must not reach PATH, under a scratch home.
func fakeSelf(t *testing.T, name string) (exe, home string) {
	t.Helper()
	home = t.TempDir()
	testhome.SetHome(t, home)
	dir := t.TempDir()
	exe = filepath.Join(dir, name)
	for _, f := range []string{exe, filepath.Join(dir, "node")} {
		if err := os.WriteFile(f, []byte("#!/bin/sh\necho self\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := selfExecutable
	selfExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { selfExecutable = old })
	return exe, home
}

func TestSelfFirstOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need developer mode")
	}
	exe, home := fakeSelf(t, "monoagentcli")
	env := SelfFirstOnPath([]string{"A=1", "PATH=/usr/bin:/bin"})
	path := pathOf(env)
	if len(path) != 3 || path[1] != "/usr/bin" || path[2] != "/bin" || env[0] != "A=1" {
		t.Fatalf("PATH = %v, env = %v", path, env)
	}
	if !strings.HasPrefix(path[0], filepath.Join(home, ".monoagent", "self-bin")+string(filepath.Separator)) {
		t.Fatalf("first entry %q is not the self-bin folder", path[0])
	}
	entries, err := os.ReadDir(path[0])
	if err != nil || len(entries) != 1 || entries[0].Name() != "monoagentcli" {
		t.Fatalf("the folder must hold only the monoagentcli link: %v %v", entries, err)
	}
	got, err1 := os.Stat(filepath.Join(path[0], "monoagentcli"))
	want, err2 := os.Stat(exe)
	if err1 != nil || err2 != nil || !os.SameFile(got, want) {
		t.Fatalf("the link is not the running executable: %v %v", err1, err2)
	}

	// Again: same folder, no duplicate entry; a stale link is replaced.
	if again := pathOf(SelfFirstOnPath(env)); len(again) != 3 || again[0] != path[0] {
		t.Errorf("second call: %v", again)
	}
	link := filepath.Join(path[0], "monoagentcli")
	other := filepath.Join(t.TempDir(), "monoagentcli")
	if err := os.WriteFile(other, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	SelfFirstOnPath(nil)
	if got, err := os.Stat(link); err != nil || !os.SameFile(got, want) {
		t.Errorf("a stale link must point at the running executable again: %v", err)
	}
	if p := pathOf(SelfFirstOnPath(nil)); len(p) != 1 || p[0] != path[0] {
		t.Errorf("no PATH at all: %v", p)
	}
}

func TestSelfFirstOnPathLeavesOtherBinariesAlone(t *testing.T) {
	fakeSelf(t, "monomind.test")
	env := []string{"PATH=/usr/bin"}
	if got := SelfFirstOnPath(env); len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Errorf("a process that isn't monoagentcli adds nothing: %v", got)
	}
}

// An agent's shell finds this build's monoagentcli before any other on PATH.
func TestExecPutsThisMonoagentcliFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	exe, _ := fakeSelf(t, "monoagentcli")
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "monoagentcli"), []byte("#!/bin/sh\necho other\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", elsewhere+string(os.PathListSeparator)+os.Getenv("PATH"))
	bin := filepath.Join(t.TempDir(), "fake-path.sh")
	script := "#!/bin/sh\necho \"cli=$(command -v monoagentcli) out=$(monoagentcli)\" >&2\nexit 3\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, _ = Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "claude", Prompt: "hi", Stderr: &buf}, nil)
	got := strings.TrimSpace(buf.String())
	cli, _, _ := strings.Cut(strings.TrimPrefix(got, "cli="), " ")
	a, err1 := os.Stat(cli)
	b, err2 := os.Stat(exe)
	if err1 != nil || err2 != nil || !os.SameFile(a, b) || !strings.HasSuffix(got, "out=self") {
		t.Errorf("monoagentcli in the agent's env = %q, want the running executable %s", got, exe)
	}
}
