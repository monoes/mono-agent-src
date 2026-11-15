package chat

import (
	"strings"
	"testing"
)

// `doomsday start` restarts an idle stale host with the same machinery as a send, and its refusals name the start's own next step.
// Revert proof: making EnsureCurrentHostForStart refuse without stopping turns the first check red; wording it with StaleThenSend
// turns the second red.
func TestEnsureCurrentHostForStartRestartsAnIdleStaleHostAndRefusesABusyOne(t *testing.T) {
	cfg, stopped := staleHostFixture(t)
	if err := EnsureCurrentHostForStart(cfg); err != nil || *stopped != 1 {
		t.Fatalf("an idle stale host: err=%v stops=%d", err, *stopped)
	}

	cfg, stopped = staleHostFixture(t)
	execOn(t, cfg, `INSERT INTO profiles (id, name) VALUES ('p1','P'); INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p1', 1, 'c1', 'running', x'01')`)
	err := EnsureCurrentHostForStart(cfg)
	if err == nil || *stopped != 0 {
		t.Fatalf("a busy stale host: err=%v stops=%d", err, *stopped)
	}
	for _, want := range []string{"coder host stop", "run `doomsday start` again", "was not restarted automatically because an open-ended run is open"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal lacks %q: %v", want, err)
		}
	}
}

// A stop that fails is a refusal that says so, never a start on a stale host.
func TestEnsureCurrentHostForStartSaysWhenTheStopFails(t *testing.T) {
	cfg, _ := staleHostFixture(t)
	old := stopStaleProcess
	stopStaleProcess = func(*Deps, hostInfo) (string, error) { return "", errStopRefused }
	t.Cleanup(func() { stopStaleProcess = old })
	err := EnsureCurrentHostForStart(cfg)
	if err == nil || !strings.Contains(err.Error(), "could not be stopped by itself") || !strings.Contains(err.Error(), "run `doomsday start` again") {
		t.Fatalf("a failed stop: %v", err)
	}
}

var errStopRefused = errString("operation not permitted")

type errString string

func (e errString) Error() string { return string(e) }
