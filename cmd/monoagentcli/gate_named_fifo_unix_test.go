//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// A FIFO named as the database must not hang the probe: opening one blocks without bound. The gate answers
// within a few seconds; the non-recovery flavour refuses ("cannot tell"), the recovery flavour does not.
func TestAFifoNamedAsTheDatabaseDoesNotHangTheGate(t *testing.T) {
	coderhost.SetRegistryDirForTest(t, "")
	cfg := opEnv(t)
	cfg.DBPath = filepath.Join(t.TempDir(), "fifo.db")
	if err := syscall.Mkfifo(cfg.DBPath, 0o600); err != nil {
		t.Skip("cannot make a FIFO here: ", err)
	}
	// frees a probe that is still blocked in open(2) so the test binary does not keep a stuck goroutine
	t.Cleanup(func() {
		if f, err := os.OpenFile(cfg.DBPath, os.O_RDWR, 0); err == nil {
			f.Close()
		}
	})
	type answer struct{ normal, recovery string }
	ch := make(chan answer, 1)
	go func() { ch <- answer{opgate.Refusal(), opgate.RecoveryRefusal()} }()
	select {
	case a := <-ch:
		if !strings.Contains(a.normal, "cannot tell") {
			t.Errorf("a FIFO as the database did not refuse the normal gate: %q", a.normal)
		}
		if a.recovery != "" {
			t.Errorf("a FIFO as the database locked out a recovery verb: %q", a.recovery)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the gate hung on a FIFO named as the database")
	}
}
