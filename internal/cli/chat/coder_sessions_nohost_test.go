package chat

import (
	"bytes"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
)

// stop, pause and resume only reach a host that is running: they never start one (#1763).
func TestSessionsControlVerbsDoNotStartAHost(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	orig := startHostProcess
	t.Cleanup(func() { startHostProcess = orig })
	startHostProcess = func(*Deps) error { t.Error("a control verb started the host"); return nil }

	for _, verb := range []string{"stop", "pause", "resume"} {
		if _, code := runSessionsCLI(t, cfg, verb, conv.ID); code != 0 {
			t.Errorf("%s with no host: exit %d", verb, code)
		}
	}
	if hostRunning(cfg) {
		t.Fatal("a host is running")
	}
	if n := len(pendingCommands(t, dbPath)); n != 3 {
		t.Errorf("queued %d commands, want 3 (they wait for a host)", n)
	}
}

// ensureHost does not start a host while coder mode is disabled.
func TestEnsureHostRefusesWhileCoderIsDisabled(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	db, err := cfg.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	s, err := loadCoderSettings(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	s.Enabled = false
	if err := saveCoderSettings(db.DB, s); err != nil {
		t.Fatal(err)
	}
	db.Close()
	orig := startHostProcess
	t.Cleanup(func() { startHostProcess = orig })
	startHostProcess = func(*Deps) error { t.Error("a host was started with coder mode disabled"); return nil }
	if err := ensureHost(cfg); err == nil {
		t.Fatal("ensureHost with coder mode disabled: want an error")
	}
}

// A stop that no host will apply says so instead of reporting plain success.
func TestSessionsStopWithNoHostSaysItIsWaiting(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	cmd := newCoderSessionsCmd(cfg)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"stop", conv.ID})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"hostRunning": false`) && !strings.Contains(out.String(), `"hostRunning":false`) {
		t.Errorf("the answer does not say no host is running:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "no coder host is running") {
		t.Errorf("no warning on stderr: %q", errOut.String())
	}
}
