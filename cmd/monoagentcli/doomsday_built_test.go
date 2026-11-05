package main

import (
	"fmt"
	"sync/atomic"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// The text and verbs of the wired build: the names the build-agnostic tests read.
const (
	governorVerbExpected        = true
	controlRoomNoRunReason      = "no_run"
	finishNoRunRefusal          = "is not a finish"
	startRenderNoCharterRefusal = "refuses to start here: .*(test containment probe|pinned database connection)"
	stopNoRunText               = "no open run to stop"
)

// wiredPins is the number of pinned connections a verb makes with no charter in a wired build (counted with a refusing pin).
// current wording/behaviour, changes in the follow-up PR (busy-database wording, early Stop progress line, lazy Engine)
func wiredPins(verb string) int {
	switch verb {
	case "stop":
		return 2
	case "finish", "finish --begin", "rubric", "status":
		return 1
	}
	return 0
}

// noRealContainment replaces the engine of the verbs that probe containment with one whose probe is a fake. The real probe
// starts and removes a transient systemd unit through `systemctl --user` outside the scratch HOME, which a test must never
// do. The fake fails with the production refusal of a host without a systemd user manager (the same words the real
// "cgroup v2 delegation to the user" step gives), so the verb's own refusal is still what is asserted. It returns the number
// of probe calls (0 when the pin failed first: the engine then answers with the pin's error and never probes).
func noRealContainment(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	real := doomsdayEngine
	doomsdayEngine = func(cfg *globalConfig) doomsdaycli.Engine {
		db, err := initDB(cfg)
		if err != nil {
			return engineForOpenFailure(err)
		}
		w := doomsdayWiring(cfg, db.DB)
		w.Probe = func() error {
			calls.Add(1)
			return fmt.Errorf("%w: cgroup v2 delegation to the user: not inside the user's systemd manager subtree (no user manager) (test containment probe)", stop.ErrContainmentUnavailable)
		}
		return w.FinishEngine()
	}
	t.Cleanup(func() { doomsdayEngine = real })
	return &calls
}
