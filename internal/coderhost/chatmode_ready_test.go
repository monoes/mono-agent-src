package coderhost

import "testing"

// The open mode is offered only when openModeReady says the boss binding exists (the flip PR turned it on together with the boss
// creation and the task-session budget). While it is false the check is not asked and the reason says why.
func TestOpenModeIsNotOfferedWhileTheBossBindingIsPending(t *testing.T) {
	asked := 0
	defer SetOpenModeCheck(func() (bool, string) { asked++; return true, "" })()
	defer SetOpenModeReady(false)()
	ok, reason := OpenModeAvailability()
	if ok || reason != "not available yet: boss binding pending" || asked != 0 {
		t.Fatalf("ready=false: ok=%v reason=%q probe asked %d times", ok, reason, asked)
	}
}

func TestOpenModeFollowsTheProbeOnceReady(t *testing.T) {
	defer SetOpenModeReady(true)()
	defer SetOpenModeCheck(func() (bool, string) { return false, "a run exists" })()
	if ok, reason := OpenModeAvailability(); ok || reason != "a run exists" {
		t.Fatalf("%v %q", ok, reason)
	}
	SetOpenModeCheck(func() (bool, string) { return true, "" })
	if ok, _ := OpenModeAvailability(); !ok {
		t.Fatal("not available although ready and the probe says so")
	}
}

// The switch ships on (the flip PR): the open mode follows the cheap check. Turning it off again (SetOpenModeReady(false), the one
// seam) is the way to withdraw the mode; TestOpenModeIsNotOfferedWhileTheBossBindingIsPending holds that.
func TestOpenModeShipsReady(t *testing.T) {
	if !openModeReady {
		t.Fatal("openModeReady is false: the open mode is not offered although the boss binding exists")
	}
	defer SetOpenModeCheck(func() (bool, string) { return true, "" })()
	if ok, reason := OpenModeAvailability(); !ok || reason != "" {
		t.Fatalf("%v %q", ok, reason)
	}
	if r := OpenModeNotReady(); r != "" {
		t.Fatalf("not ready reason %q", r)
	}
}
