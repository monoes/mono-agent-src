package monomind

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// recordingMonomind is a fake monomind that records the paths of the files Exec
// hands it (--prompt-file, --system-file, --tools-file) and answers a turn.
func recordingMonomind(t *testing.T) (bin, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "monomind")
	record = filepath.Join(dir, "files.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ] && [ \"$2\" = \"--json\" ]; then echo '{\"v\":1,\"version\":\"2.10.0\",\"min_caller\":\"1.0.0\",\"capabilities\":[\"agent-exec\",\"agent-scan\",\"org-json-v1\"]}'; exit 0; fi\n" +
		"if [ \"$1\" = \"agent\" ] && [ \"$2\" = \"exec\" ]; then\n" +
		"  prev=\"\"\n" +
		"  for a in \"$@\"; do\n" +
		"    case \"$prev\" in --prompt-file|--system-file|--tools-file) echo \"$a\" >> \"" + record + "\";; esac\n" +
		"    prev=\"$a\"\n" +
		"  done\n" +
		"  echo '{\"v\":1,\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"stop_reason\":\"end_turn\",\"text\":\"ok\"}'\n" +
		"  echo '{\"v\":1,\"type\":\"done\",\"exit_code\":0}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'unsupported' >&2; exit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

// recordedFiles is what the fake monomind was handed.
func recordedFiles(t *testing.T, record string) []string {
	t.Helper()
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(raw))
}

func execWithAllFiles(t *testing.T, bin string) {
	t.Helper()
	opts := ExecOptions{Bin: bin, Runtime: "claude", Prompt: "p", SystemPrompt: "s", Tools: []ToolSpec{{Name: "t", Description: "d"}},
		OnToolCall: func(context.Context, string, json.RawMessage) (string, error) { return "", nil }}
	if _, err := Exec(context.Background(), opts, nil); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

// A turn that runs in a workspace-write sandbox may write the system temp
// directory, and it can then rewrite another turn's prompt file between Exec
// creating it and monomind reading it. So without a TempDir of its own, Exec
// does not use the system temp directory: it uses a private folder of the
// monoagent home, which no sandboxed turn can write.
func TestExecWritesItsFilesInAPrivateFolderByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	bin, record := recordingMonomind(t)

	execWithAllFiles(t, bin)

	private := filepath.Join(home, ".monoagent", "tmp")
	files := recordedFiles(t, record)
	if len(files) != 3 {
		t.Fatalf("want the prompt, system and tools files, got %q", files)
	}
	for _, f := range files {
		if filepath.Dir(f) != private {
			t.Errorf("%s is not in the private folder %s", f, private)
		}
	}
	if fi, err := os.Stat(private); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700) { // Windows has no Unix modes
		t.Errorf("the private folder must be 0700: %v %v", fi, err)
	}
	if left, _ := os.ReadDir(private); len(left) != 0 {
		t.Errorf("Exec must remove its files when the turn ends: %d left", len(left))
	}
}

// When the home cannot hold the private folder the turn still runs, as before,
// with the system temp directory.
func TestExecFallsBackToTheSystemTempDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.WriteFile(filepath.Join(home, ".monoagent"), []byte("a file, not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin, record := recordingMonomind(t)

	execWithAllFiles(t, bin)

	for _, f := range recordedFiles(t, record) {
		if filepath.Dir(f) != filepath.Clean(os.TempDir()) {
			t.Errorf("%s is not in the system temp directory %s", f, os.TempDir())
		}
	}
}

// Files left by a crash would stay for good in a folder the system does not
// clean, so files of Exec's own more than a day old are removed, and nothing
// else is.
func TestPrivateFolderSweepsStaleFilesOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	private := filepath.Join(home, ".monoagent", "tmp")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	old, fresh, other := filepath.Join(private, "monoagent-prompt-old.md"), filepath.Join(private, "monoagent-prompt-fresh.md"), filepath.Join(private, "notes.txt")
	for _, f := range []string{old, fresh, other} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	twoDaysAgo := time.Now().Add(-48 * time.Hour)
	for _, f := range []string{old, other} {
		if err := os.Chtimes(f, twoDaysAgo, twoDaysAgo); err != nil {
			t.Fatal(err)
		}
	}
	bin, _ := recordingMonomind(t)

	execWithAllFiles(t, bin)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("a prompt file more than a day old must be removed: %v", err)
	}
	for _, f := range []string{fresh, other} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s must be left alone: %v", f, err)
		}
	}
}

// MkdirAll keeps the mode of a folder that already exists; one a user made
// looser is tightened, because it holds prompts.
func TestPrivateFolderIsTightenedTo0700(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	private := filepath.Join(home, ".monoagent", "tmp")
	if err := os.MkdirAll(private, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(private, 0o755); err != nil {
		t.Fatal(err)
	}
	bin, _ := recordingMonomind(t)

	execWithAllFiles(t, bin)

	if fi, err := os.Stat(private); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700) {
		t.Errorf("the folder holding prompts must be 0700: %v %v", fi, err)
	}
}

// monomind keeps copies of its own (a prompt file, an agent file) under the temp
// directory it runs with. A caller that runs turns for others, the
// OpenAI-compatible API, gives every turn a temp folder of its own through
// ExecOptions.Env, and that value must win over the one the parent process has.
func TestExecEnvOverridesTheInheritedTempDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	dir, own, files := t.TempDir(), t.TempDir(), t.TempDir() // before TMPDIR changes below
	bin, seen := filepath.Join(dir, "monomind"), filepath.Join(dir, "seen.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ] && [ \"$2\" = \"--json\" ]; then echo '{\"v\":1,\"version\":\"2.10.0\",\"min_caller\":\"1.0.0\",\"capabilities\":[\"agent-exec\",\"agent-scan\",\"org-json-v1\"]}'; exit 0; fi\n" +
		"if [ \"$1\" = \"agent\" ] && [ \"$2\" = \"exec\" ]; then\n" +
		"  echo \"$TMPDIR $TMP $TEMP\" > \"" + seen + "\"\n" +
		"  echo '{\"v\":1,\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"stop_reason\":\"end_turn\",\"text\":\"ok\"}'\n" +
		"  echo '{\"v\":1,\"type\":\"done\",\"exit_code\":0}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'unsupported' >&2; exit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(dir, "ambient"))

	opts := ExecOptions{Bin: bin, Runtime: "claude", Prompt: "p", TempDir: files,
		Env: map[string]string{"TMPDIR": own, "TMP": own, "TEMP": own}}
	if _, err := Exec(context.Background(), opts, nil); err != nil {
		t.Fatalf("exec: %v", err)
	}
	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(raw)), own+" "+own+" "+own; got != want {
		t.Errorf("the monomind process ran with TMPDIR, TMP and TEMP %q, want %q", got, want)
	}
}
