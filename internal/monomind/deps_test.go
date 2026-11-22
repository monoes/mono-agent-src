package monomind

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeSDKInstalledFollowsMonomindHome(t *testing.T) {
	home := t.TempDir()
	mh := t.TempDir()
	t.Setenv("MONOMIND_HOME", "")
	if ClaudeSDKInstalled(home) {
		t.Fatal("nothing installed anywhere, got installed")
	}
	if err := os.MkdirAll(filepath.Join(mh, "deps", claudeSDKDir+"0.3.319"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ClaudeSDKInstalled(home) {
		t.Fatal("MONOMIND_HOME unset: its deps folder must not count")
	}
	t.Setenv("MONOMIND_HOME", mh)
	if !ClaudeSDKInstalled(home) {
		t.Fatal("SDK under $MONOMIND_HOME/deps was not found")
	}
}

func TestClaudeSDKInstalledFindsHomeFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MONOMIND_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, ".monomind", "deps", claudeSDKDir+"0.3.319"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !ClaudeSDKInstalled(home) {
		t.Fatal("SDK under $HOME/.monomind/deps was not found")
	}
}
