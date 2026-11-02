package chat

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// A running host registers its database in the turn registry (so the operator gates check it whatever
// database a command names) and removes the entry when it stops. The seam keeps the test off the real registry.
func TestTheHostRegistersItsDatabaseWhileItRunsAndRemovesItAtExit(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	regDir := filepath.Join(t.TempDir(), "coder-hosts")
	coderhost.SetRegistryDirForTest(t, regDir)
	cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default", JSONOutput: true})
	cmd := newCoderHostCmd(cfg)
	cmd.SetArgs([]string{"run"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetErr(&bytes.Buffer{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	deadline := time.Now().Add(20 * time.Second)
	var dbs []string
	for time.Now().Before(deadline) {
		dbs, _ = coderhost.RegisteredHostDBs()
		if len(dbs) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(dbs) != 1 || !strings.HasSuffix(dbs[0], filepath.Base(cfg.DBPath())) {
		t.Fatalf("the running host registered %v", dbs)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the host did not stop")
	}
	if dbs, _ := coderhost.RegisteredHostDBs(); len(dbs) != 0 {
		t.Fatalf("the stopped host left %v", dbs)
	}
}

// With no registry (no passwd home) the host still starts, says so on its error stream, and status shows the line.
func TestHostStatusSaysWhenTheTurnRegistryIsUnavailable(t *testing.T) {
	coderhost.SetRegistryDirForTest(t, "")
	cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default", JSONOutput: true})
	if st := hostStatus(cfg); st["turnRegistry"] == nil {
		t.Fatalf("status has no registry line: %v", st)
	}
	coderhost.SetRegistryDirForTest(t, filepath.Join(t.TempDir(), "coder-hosts"))
	if st := hostStatus(cfg); st["turnRegistry"] != nil {
		t.Fatalf("status reports a working registry as unavailable: %v", st)
	}
}
