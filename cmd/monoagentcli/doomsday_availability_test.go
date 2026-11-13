package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/storage"
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
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	setBindingStored(t, db, true)
	ok, reason := coderhost.OpenModeAvailability()
	if !ok || reason != "" || *calls != 0 {
		t.Fatalf("no run: ok=%v reason=%q engine calls %d", ok, reason, *calls)
	}
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

func setBindingStored(t *testing.T, db *storage.Database, on bool) {
	t.Helper()
	v := "false"
	if on {
		v = "true"
	}
	if _, err := db.DB.Exec(`INSERT OR REPLACE INTO configs (name, config_data) VALUES ('coder_mode', ?)`, `{"peercredBinding":`+v+`}`); err != nil {
		t.Fatal(err)
	}
}

// The open-ended mode needs peer-credential binding to be on in this profile (#1568): unset follows the machine (on where it
// can run the service, since the default flipped), an explicit off makes the mode unavailable with the reason naming Settings,
// and switching it off takes it away again. Where the machine cannot run the binding service the older reason still wins.
func TestOpenModeFollowsTheStoredBindingSetting(t *testing.T) {
	procbind.ForTestSetIssuance(t, true)
	procbind.ForTestSetReady(t, nil)
	cfg, _ := chatPathSetup(t, true)
	if ok, reason := coderhost.OpenModeAvailability(); !ok {
		t.Fatalf("never set on a capable machine: the default must offer the mode, got %q", reason)
	}
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	setBindingStored(t, db, false)
	if ok, reason := coderhost.OpenModeAvailability(); ok || !strings.Contains(reason, "peer-credential binding is off") || !strings.Contains(reason, "Settings") {
		t.Fatalf("stored off: ok=%v reason=%q", ok, reason)
	}
	setBindingStored(t, db, true)
	if ok, reason := coderhost.OpenModeAvailability(); !ok {
		t.Fatalf("stored on: not offered: %q", reason)
	}
	setBindingStored(t, db, false)
	if ok, _ := coderhost.OpenModeAvailability(); ok {
		t.Fatal("switched off again: the mode is still offered")
	}
	// Where the machine cannot run the binding service the mode is still offered: the run starts unbound, with a warning.
	restore := coderhost.SetOpenModeBindingProbe("windows", func() error { return errors.New("no service here") })
	defer restore()
	setBindingStored(t, db, true)
	if ok, reason := coderhost.OpenModeAvailability(); !ok {
		t.Fatalf("cannot run binding: the mode must still be offered: %q", reason)
	}
}

// Where the machine cannot run the binding service the unset default is off and the mode is STILL offered (the start goes unbound
// with a warning); an explicit off where the machine could run it is not offered, as before.
func TestOpenModeIsOfferedWhereBindingCannotRunAndRefusedForAnExplicitOffWhereItCan(t *testing.T) {
	procbind.ForTestSetIssuance(t, true)
	procbind.ForTestSetReady(t, errors.New("old kernel"))
	cfg, _ := chatPathSetup(t, true)
	if ok, reason := coderhost.OpenModeAvailability(); !ok {
		t.Fatalf("unsupported machine, nothing stored: not offered: %q", reason)
	}
	pre, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pre.Close()
	if got := unboundReason(pre.DB); !strings.Contains(got, "old kernel") { // what a start now would warn about
		t.Fatalf("unbound reason = %q", got)
	}
	procbind.ForTestSetReady(t, nil)
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	setBindingStored(t, db, false)
	if ok, _ := coderhost.OpenModeAvailability(); ok {
		t.Fatal("explicit off on a machine that can bind: the mode is offered")
	}
	if got := unboundReason(db.DB); got != "" {
		t.Fatalf("a machine that can bind has no unbound reason: %q", got)
	}
}

// #1582: where the mode cannot run (macOS) the real wiring's binding answer is the OS refusal, so a start creates no run.
func TestRealWiringRefusesTheStartWhereTheModeCannotRun(t *testing.T) {
	cfg := sendEnv(t)
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Cleanup(coderhost.SetOpenModeBindingProbe("darwin", func() error { return nil }))
	_, err = doomsdayWiringReal(cfg, db.DB).Binding(context.Background())
	var un doomsdaycli.UnsupportedHere
	if !errors.As(err, &un) || !strings.Contains(err.Error(), "macOS") {
		t.Fatalf("err = %v", err)
	}
	t.Cleanup(coderhost.SetOpenModeBindingProbe("linux", func() error { return nil }))
	if _, err := doomsdayWiringReal(cfg, db.DB).Binding(context.Background()); errors.As(err, &un) {
		t.Fatalf("linux refused: %v", err)
	}
}

// A stored `on` that this machine cannot honour stops `doomsday start` BEFORE a run exists (its turns would all be refused); an
// unset default on the same machine does not. The status warning comes from the run's recorded start mode, not the live setting.
// Revert proof: dropping ExplicitOnBroken in the wiring's Binding turns the first check red.
func TestStartRefusesAnExplicitOnThatCannotBeHonouredAndStatusReadsTheRecordedMode(t *testing.T) {
	procbind.ForTestSetIssuance(t, true)
	procbind.ForTestSetReady(t, errors.New("old kernel"))
	cfg, _ := chatPathSetup(t, true)
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b := doomsdayWiringReal(cfg, db.DB).Binding
	if on, err := b(context.Background()); on || err != nil {
		t.Fatalf("unset on an unsupported machine: on=%v err=%v", on, err)
	}
	setBindingStored(t, db, true)
	var un doomsdaycli.UnsupportedHere
	if _, err := b(context.Background()); !errors.As(err, &un) || !strings.Contains(err.Error(), "switched on") || !strings.Contains(err.Error(), "old kernel") {
		t.Fatalf("an explicit on that cannot be honoured: %v", err)
	}
	if ok, reason := coderhost.OpenModeAvailability(); ok || !strings.Contains(reason, "switched on") {
		t.Fatalf("availability: ok=%v %q", ok, reason)
	}
	// status: from the recorded run row
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES (?, 'P')`, cfg.ProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES (?, 1, 'c1', 'running', x'01')`, cfg.ProfileID); err != nil {
		t.Fatal(err)
	}
	warn := unboundWarningFor(cfg)
	if got := warn(context.Background()); got != "" {
		t.Fatalf("a bound run shows a warning from the live setting: %q", got)
	}
	if _, err := db.DB.Exec(`INSERT INTO doomsday_audit (profile, generation, seq, kind, payload, prev, mac) VALUES (?, 1, 1, 'run.unbound', 'unbound: old kernel', x'00', x'00')`, cfg.ProfileID); err != nil {
		t.Fatal(err)
	}
	if got := warn(context.Background()); got != "running without peer-credential binding: old kernel" {
		t.Fatalf("recorded mode: %q", got)
	}
}
