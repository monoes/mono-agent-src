//go:build doomsday

package main

// The text and verbs of a build with the mode wired (see doomsday_default_stub_test.go for the names). After the tag is
// removed this is the only version of them.
const (
	governorVerbExpected   = true
	controlRoomNoRunReason = "no_run"
	finishNoRunRefusal     = "is not a finish"
	stopNoRunText          = "no open run to stop"
)
