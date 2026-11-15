package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/daemonhb"
)

// hostWithInfo makes a host look running (its lock held) with the given host info file ("" writes none).
func hostWithInfo(t *testing.T, info string) string {
	t.Helper()
	dbPath := newChatCLITestDB(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	if err := os.MkdirAll(hostDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := daemonhb.LockFile(hostLockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if info != "" {
		if err := os.WriteFile(filepath.Join(hostDir(cfg), hostInfoFile), []byte(info), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dbPath
}

// A running host that does not publish the run-gate marker is one built before the default-on binding: its dispatch gate reads an
// unset setting as OFF and refuses every turn of a run the new start made bound. The start must say so before it makes a run.
func TestAHostWithoutTheRunGateMarkerIsStaleForAnOpenEndedStart(t *testing.T) {
	dbPath := hostWithInfo(t, `{"pid":1,"instance":"host-old","childTokens":true}`)
	why := StaleHostReason(dbPath)
	for _, want := range []string{"older", "coder host stop", "doomsday start"} {
		if !strings.Contains(why, want) {
			t.Fatalf("reason %q lacks %q", why, want)
		}
	}
}

func TestAHostThatPublishesTheRunGateMarkerAndNoHostAreNotStale(t *testing.T) {
	raw, _ := json.Marshal(hostInfo{PID: 1, Instance: "host-new", RunGate: runGateVersion})
	if why := StaleHostReason(hostWithInfo(t, string(raw))); why != "" {
		t.Fatalf("a current host is stale: %q", why)
	}
	cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default", JSONOutput: true})
	if why := StaleHostReason(cfg.DBPath()); why != "" {
		t.Fatalf("no host running is stale: %q", why)
	}
	// a host that holds the lock but has not written its info yet is not called stale (it is starting; the marker is written first)
	if why := StaleHostReason(hostWithInfo(t, "")); why != "" {
		t.Fatalf("a starting host is stale: %q", why)
	}
}
