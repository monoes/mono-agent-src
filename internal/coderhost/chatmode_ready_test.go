package coderhost

import "testing"

// The open mode is offered only when openModeReady says the boss binding exists (T4 flips it together with the boss
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

// The switch ships off. The change that adds the boss creation and the task-session budget flips it and this test in the
// same commit; nothing else may.
func TestOpenModeShipsNotReady(t *testing.T) {
	if openModeReady {
		t.Fatal("openModeReady is true: the open mode is offered although the boss binding is not there")
	}
	if ok, reason := OpenModeAvailability(); ok || reason != "not available yet: boss binding pending" {
		t.Fatalf("%v %q", ok, reason)
	}
}
