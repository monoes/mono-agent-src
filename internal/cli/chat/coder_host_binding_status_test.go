package chat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/daemonhb"
)

// runningHostWith makes the host look running (the lock held) with a host info file that carries binding state.
func runningHostWith(t *testing.T, binding []coderhost.BindingStatus) *Deps {
	t.Helper()
	cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default", JSONOutput: true})
	if err := os.MkdirAll(hostDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := daemonhb.LockFile(hostLockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	raw, _ := json.Marshal(hostInfo{PID: os.Getpid(), Instance: "host-test", StartedAt: "2026-10-31T00:00:00Z", Binding: binding})
	if err := os.WriteFile(filepath.Join(hostDir(cfg), hostInfoFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The binding services' counts are the operator's to see: the operator gets them, an agent gets the status without.
func TestHostStatusShowsTheBindingServicesToTheOperatorOnly(t *testing.T) {
	cfg := runningHostWith(t, []coderhost.BindingStatus{{Profile: "default", Running: true, Start: 2, Pending: 1, Active: 3, Revoked: 4}})
	cfg.RequireOperator = func(string) error { return nil }
	st := hostStatus(cfg)
	b, ok := st["binding"].([]coderhost.BindingStatus)
	if !ok || len(b) != 1 || b[0].Active != 3 || b[0].Revoked != 4 || b[0].Start != 2 {
		t.Fatalf("operator status: %v", st)
	}
	raw, _ := json.Marshal(st)
	for _, secret := range []string{"token", "hash", "generation", "key", "mst-", "mat-"} {
		if strings.Contains(strings.ToLower(string(raw)), secret) {
			t.Errorf("the status mentions %q: %s", secret, raw)
		}
	}
	cfg.RequireOperator = func(string) error { return errors.New("operator only") }
	if st := hostStatus(cfg); st["binding"] != nil || st["running"] != true {
		t.Errorf("an agent's status: %v", st)
	}
}

// The host publishes the services' state into its info file when it changes, and not before.
func TestThePublisherRewritesTheInfoFileWhenTheBindingStateChanges(t *testing.T) {
	old := bindingPublishEvery
	bindingPublishEvery = 10 * time.Millisecond
	t.Cleanup(func() { bindingPublishEvery = old })
	dir := t.TempDir()
	infoPath := filepath.Join(dir, hostInfoFile)
	binding := &coderhost.BindingHost{} // no services: the state stays empty
	info := &hostInfo{PID: 1, Instance: "host-test"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); publishBindingStatus(ctx, info, infoPath, binding) }()
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(infoPath); err == nil {
		t.Error("the info file was written although nothing changed")
	}
	cancel()
	<-done
}
