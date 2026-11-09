package chat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `coder host run` locks the host's own access list (Windows) before it creates its directory, its lock file or anything else,
// and a host that cannot lock itself does not start.
func TestHostRunLocksItsProcessBeforeAnythingElse(t *testing.T) {
	db := filepath.Join(t.TempDir(), "d", "mono.db")
	cfg := testDeps(&testConfig{DBPath: db})
	cfg.RequireOperator = func(string) error { return nil }
	old := lockHostProcessAtStart
	t.Cleanup(func() { lockHostProcessAtStart = old })
	calls := 0
	lockHostProcessAtStart = func() error {
		calls++
		if _, err := os.Stat(hostDir(cfg)); err == nil {
			t.Error("the host directory existed before the host locked itself")
		}
		return errors.New("no access list")
	}
	cmd := newCoderHostRunCmd(cfg)
	cmd.SetArgs(nil)
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "could not lock its own process") {
		t.Fatalf("a host that cannot lock itself: %v", err)
	}
	if calls != 1 {
		t.Fatalf("lock calls %d", calls)
	}
	if _, err := os.Stat(hostDir(cfg)); err == nil {
		t.Fatal("a host that could not lock itself created its directory")
	}
}
