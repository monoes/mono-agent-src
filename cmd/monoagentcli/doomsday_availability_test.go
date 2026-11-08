package main

import (
	"context"
	"strings"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// availEngine counts every call a host step or the witness would sit behind: none may happen on a chat path.
type availEngine struct {
	doomsdaycli.Off
	calls *int
}

func (a availEngine) Containment() error { *a.calls++; return nil }
func (a availEngine) Status(context.Context) (doomsdaycli.Status, error) {
	*a.calls++
	return doomsdaycli.Status{Active: true, RunID: "secret-profile/7"}, nil
}

func chatPathSetup(t *testing.T, ready bool) (*globalConfig, *int) {
	t.Helper()
	oldEngine := doomsdayEngine
	t.Cleanup(func() { doomsdayEngine = oldEngine })
	t.Cleanup(coderhost.SetOpenModeReady(ready))
	t.Cleanup(coderhost.SetOpenModeBindingProbe("linux", func() error { return nil }))
	t.Cleanup(coderhost.SetOpenModeCheck(nil))
	calls := 0
	doomsdayEngine = func(*globalConfig) doomsdaycli.Engine { return availEngine{calls: &calls} }
	cfg := sendEnv(t)
	_ = chatDeps(cfg)
	return cfg, &calls
}

// The chat paths (chat-modes, settings get, chat new, the mode setter) are reachable by an agent. What they ask is the
// cheap check: no engine call (no host step, no witness, no unit), no run id in the answer.
func TestChatPathsRunNoHostStepAndLeakNoRunID(t *testing.T) {
	cfg, calls := chatPathSetup(t, true)
	ok, reason := coderhost.OpenModeAvailability()
	if !ok || reason != "" || *calls != 0 {
		t.Fatalf("no run: ok=%v reason=%q engine calls %d", ok, reason, *calls)
	}
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES (?, 7, 'charter-xyz', 'running', x'00')`, cfg.ProfileID); err != nil {
		t.Fatal(err)
	}
	ok, reason = coderhost.OpenModeAvailability()
	if ok || !strings.Contains(reason, "already active") || *calls != 0 {
		t.Fatalf("a run: ok=%v reason=%q engine calls %d", ok, reason, *calls)
	}
	for _, id := range []string{"7", "charter-xyz", cfg.ProfileID + "/"} {
		if strings.Contains(reason, id) {
			t.Fatalf("the reason leaks %q: %q", id, reason)
		}
	}
}

// While the boss binding is pending the check is not even asked.
func TestWiredCheckDoesNotOfferTheModeWhileTheBossBindingIsPending(t *testing.T) {
	_, calls := chatPathSetup(t, false)
	if ok, reason := coderhost.OpenModeAvailability(); ok || !strings.Contains(reason, "boss binding pending") || *calls != 0 {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
}
