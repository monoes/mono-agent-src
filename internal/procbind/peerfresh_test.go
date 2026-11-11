package procbind

import (
	"testing"
	"time"
)

// filetimeOf is the FILETIME tick count (100 ns since 1601) of t, as the Windows pin reads it from a process.
func filetimeOf(t time.Time) uint64 {
	return uint64(t.UnixNano()/100) + filetimeUnixEpoch
}

func TestAPeerCreatedAfterTheConnectionWasAcceptedCannotBeTheConnector(t *testing.T) {
	accepted := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		created time.Time
		after   bool
	}{
		{"created long before the connection", accepted.Add(-time.Hour), false},
		{"created a moment before", accepted.Add(-time.Millisecond), false},
		{"created at the accept", accepted, false},
		{"created within the clock slack after", accepted.Add(peerClockSlack - time.Millisecond), false},
		{"created after the slack", accepted.Add(peerClockSlack + time.Millisecond), true},
		{"created a minute after", accepted.Add(time.Minute), true},
	}
	for _, c := range cases {
		if got := createdAfterAccept(filetimeOf(c.created), accepted); got != c.after {
			t.Errorf("%s: createdAfterAccept = %v, want %v", c.name, got, c.after)
		}
	}
	if !createdAfterAccept(0, accepted) {
		t.Error("an unknown (zero) creation time must be refused, not served")
	}
	if !createdAfterAccept(filetimeUnixEpoch-1, accepted) {
		t.Error("a creation time before the Unix epoch must be refused")
	}
}
