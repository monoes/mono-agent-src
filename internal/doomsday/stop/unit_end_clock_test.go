package stop

import "testing"

// A config without a clock (the production Stop wiring builds one) must not leave EndUnit with a nil clock: it reads
// the clock whenever a ticket sub-cgroup exists, which a real Stop always meets.
func TestEndConfigWithoutAClockGetsTheSystemClock(t *testing.T) {
	if got := (EndConfig{}).norm().Clock; got == nil {
		t.Fatal("norm left the clock nil: a Stop over a unit with placed children would panic")
	}
	if _, ok := (EndConfig{}).norm().Clock.(SystemClock); !ok {
		t.Fatalf("default clock = %T, want SystemClock", (EndConfig{}).norm().Clock)
	}
}
