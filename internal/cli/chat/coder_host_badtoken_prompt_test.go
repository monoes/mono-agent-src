package chat

import (
	"os"
	"path/filepath"
	"testing"
)

// R09-08: a refused mission token leaves no prompt file (the turn message) on disk.
func TestExecLauncherBadMissionTokenLeavesNoPromptFile(t *testing.T) {
	dir := t.TempDir()
	l := &execLauncher{bin: "/nonexistent", dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns")}
	if _, err := l.Start("p", "c1", "t3", "secret turn message", nil, "--mission"); err == nil {
		t.Fatal("a token that reads as a flag was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "turns", "t3.prompt")); !os.IsNotExist(err) {
		t.Fatalf("the prompt file stayed on disk after the refusal: %v", err)
	}
}
