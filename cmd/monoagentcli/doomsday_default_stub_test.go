//go:build !doomsday

package main

import (
	"strings"
	"sync/atomic"
	"testing"
)

// What differs while the mode is compiled out (the stub in doomsday_off.go). Everything here is about the stub only: the
// build-agnostic tests read these constants for the text and verbs of this build, and the removal of the tag deletes this
// file together with the stub (doomsday_built_test.go carries the same names for the wired build).
const (
	governorVerbExpected        = false
	controlRoomNoRunReason      = "off"
	finishNoRunRefusal          = "not available on this install"
	startRenderNoCharterRefusal = "not available on this install"
	stopNoRunText               = "not available on this install"
)

// A default install wires no engine at all, so nothing can read a run or start one.
func TestDefaultInstallWiresNoEngine(t *testing.T) {
	if doomsdayEngine != nil || doomsdayReadEngine != nil || doomsdayStrategyReader != nil || doomsdayExtra != nil || doomsdayChat != nil {
		t.Fatal("a hook of the mode is wired by default: the mode would be on")
	}
}

// start, stop and render answer that the mode is not available on this install, for the user and with a good charter.
func TestDefaultInstallRefusesStartStopAndRenderAsOff(t *testing.T) {
	file := writeDoomsdayCharter(t)
	for name, args := range map[string][]string{
		"start":  {"start", "--charter", file, "--yes-i-lift", "--hash", strings.Repeat("a", 64)},
		"stop":   {"stop"},
		"render": {"render", "--charter", file},
	} {
		cfg := sendEnv(t)
		out, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), args...)
		if err == nil || !strings.Contains(err.Error(), "not available on this install") {
			t.Fatalf("%s did not refuse as off: %v\n%s", name, err, out)
		}
	}
}

// wiredPins: a build without the mode wires no engine, so no verb pins.
func wiredPins(string) int { return 0 }

// noRealContainment: a build without the mode has no engine and so no containment probe to replace.
func noRealContainment(*testing.T) *atomic.Int32 { return new(atomic.Int32) }
