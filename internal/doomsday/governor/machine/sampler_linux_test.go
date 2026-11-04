//go:build linux

package machine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in: reads the real /proc and statfs of the test temp dir, read only.
func TestRealLinuxSampler(t *testing.T) {
	if os.Getenv("MONOAGENT_GOVERNOR_REAL_PROC") != "1" {
		t.Skip("set MONOAGENT_GOVERNOR_REAL_PROC=1 to read the real /proc")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := New(t.TempDir(), "/").Sample(ctx)
	if err != nil || !s.Valid || s.Cores < 1 || s.MemAvailFrac <= 0 || s.MemAvailFrac > 1 || s.DiskFreeFrac < 0 || s.DiskFreeFrac > 1 {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestReadBoundedRefusesOversizedAndMissing(t *testing.T) {
	p := t.TempDir() + "/f"
	if err := os.WriteFile(p, []byte(strings.Repeat("a", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := readBounded(p, 100); err != nil || len(b) != 100 {
		t.Fatal(len(b), err)
	}
	if _, err := readBounded(p, 99); err == nil {
		t.Fatal("oversized accepted")
	}
	if _, err := readBounded(p+"x", 10); err == nil {
		t.Fatal("missing accepted")
	}
}

func TestStatfsOfTempDirIsSane(t *testing.T) {
	free, total, err := statfs(t.TempDir())
	if err != nil || total == 0 || free > total {
		t.Fatal(free, total, err)
	}
	if _, _, err := statfs("/nonexistent-dir-for-governor-test"); err == nil {
		t.Fatal("missing dir accepted")
	}
}
