//go:build doomsday

package main

// The text and verbs of a build with the mode wired (see doomsday_default_stub_test.go for the names). After the tag is
// removed this is the only version of them.
const (
	governorVerbExpected        = true
	controlRoomNoRunReason      = "no_run"
	finishNoRunRefusal          = "is not a finish"
	startRenderNoCharterRefusal = "(refuses to start here|--charter <file> is required)"
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
