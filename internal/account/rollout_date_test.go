package account

import (
	"testing"
	"time"
)

// wantEnforceFrom is release R's enforcement date: the owner's decision, 00:00 UTC. It is the zero
// time (dormant) until the owner sets it. To activate, the owner changes exactly two things together:
// this value and the initializer in rollout.go (`var enforceFrom = time.Date(Y, time.M, D, 0, 0, 0, 0,
// time.UTC)`). The release guard (scripts/check-rollout-ready.sh) then requires the repository variable
// ACCOUNT_ENFORCEMENT_RELEASE=true, a date at least 21 days ahead and the production-token fixture.
var wantEnforceFrom = time.Date(2026, time.October, 11, 0, 0, 0, 0, time.UTC)

func TestEnforceDateIsTheOwnersDate(t *testing.T) {
	if got := EnforceDate(); !got.Equal(wantEnforceFrom) {
		t.Fatalf("EnforceDate() = %v, want %v: the date is the owner's decision, made here and in rollout.go together", got, wantEnforceFrom)
	}
}

// The mechanism, with a date installed through the test hook: enforcement starts at that instant.
func TestEnforcedFlipsAtTheDate(t *testing.T) {
	d := time.Date(2030, time.January, 15, 0, 0, 0, 0, time.UTC)
	SetEnforceFromForTest(t, d)
	for _, c := range []struct {
		now  time.Time
		want bool
	}{{d.Add(-24 * time.Hour), false}, {d.Add(-time.Nanosecond), false}, {d, true}, {d.Add(time.Nanosecond), true}, {d.AddDate(1, 0, 0), true}} {
		if got := Enforced(c.now, time.Time{}); got != c.want {
			t.Errorf("Enforced(%v from the date, no high-water mark) = %v, want %v", c.now.Sub(d), got, c.want)
		}
	}
}

// A dormant package never enforces, whatever the clock or the high-water mark say.
func TestDormantNeverEnforces(t *testing.T) {
	SetEnforceFromForTest(t, time.Time{})
	far := time.Date(2999, time.January, 1, 0, 0, 0, 0, time.UTC)
	if Enforced(far, far) {
		t.Error("a dormant gate must not enforce")
	}
	if st := Evaluate(nil, far); st.Enforced || !st.Allowed() {
		t.Errorf("a dormant gate locked a machine with no session: %+v", st)
	}
}

// Setting the clock back does not postpone the date: it is judged against the later of now and the
// highest time the gate has seen, and a high-water mark before the date does not enforce early.
func TestTheClockGuardCannotPostponeTheDate(t *testing.T) {
	d := time.Date(2030, time.January, 15, 0, 0, 0, 0, time.UTC)
	SetEnforceFromForTest(t, d)
	if !Enforced(d.AddDate(-1, 0, 0), d.Add(time.Hour)) {
		t.Error("a clock set back a year, with the high-water mark past the date, must still be enforced")
	}
	if Enforced(d.Add(-48*time.Hour), d.Add(-24*time.Hour)) {
		t.Error("a high-water mark before the date must not enforce early")
	}
	if st := Evaluate(&Session{V: 1, HW: d.Add(time.Hour)}, d.AddDate(-1, 0, 0)); !st.Enforced || st.Allowed() {
		t.Errorf("a session whose high-water mark is past the date, read by a clock a year behind: %+v", st)
	}
	if st := Evaluate(nil, d.Add(-time.Hour)); st.Enforced || !st.Allowed() {
		t.Errorf("before the date a machine with no session is not locked: %+v", st)
	}
}

// A clock set back with no high-water mark (account folder deleted) cannot un-enforce a binary built after the date.
func TestBuildDateFloorsTheClock(t *testing.T) {
	d := time.Date(2030, time.January, 15, 0, 0, 0, 0, time.UTC)
	SetEnforceFromForTest(t, d)
	SetBuildFloor(d.Add(24 * time.Hour))
	t.Cleanup(func() { SetBuildFloor(time.Time{}) })
	if !Enforced(d.Add(-48*time.Hour), time.Time{}) {
		t.Error("a clock before the date must not un-enforce a binary built after it")
	}
	SetBuildFloor(time.Time{})
	if Enforced(d.Add(-48*time.Hour), time.Time{}) {
		t.Error("an unstamped build has no floor")
	}
}
