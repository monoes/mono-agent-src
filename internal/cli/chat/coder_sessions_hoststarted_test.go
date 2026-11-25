package chat

import (
	"encoding/json"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/daemonhb"
)

// #1766: `send` answers hostStarted true only when this send started a host,
// not whenever a host was merely wanted.
func TestSessionsSendReportsAHostItActuallyStarted(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	conv, err := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	orig := startHostProcess
	t.Cleanup(func() { startHostProcess = orig })
	startHostProcess = func(c *Deps) error { // stands in for the detached host: takes the lock
		release, err := daemonhb.LockFile(hostLockPath(c))
		if err == nil {
			t.Cleanup(release)
		}
		return err
	}
	send := func(args ...string) bool {
		t.Helper()
		out, code := runSessionsCLI(t, cfg, append([]string{"send", conv.ID, "hi"}, args...)...)
		var got struct {
			HostStarted *bool `json:"hostStarted"`
		}
		if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil || got.HostStarted == nil {
			t.Fatalf("send %v: exit %d %s (%v)", args, code, out, err)
		}
		return *got.HostStarted
	}
	if send("--no-host") {
		t.Error("--no-host started a host")
	}
	if !send() {
		t.Error("the send that started the host says it did not")
	}
	if send() {
		t.Error("a send to a running host says it started one")
	}
}
