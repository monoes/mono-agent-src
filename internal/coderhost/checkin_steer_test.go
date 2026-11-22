package coderhost

import (
	"strings"
	"testing"
	"time"
)

// The digest gives the master the exact command for reaching a child, with the
// profile the host serves in it (a turn's token does not pin the profile, and the
// CLI reads none from the environment): #1765.
func TestTheDigestNamesTheCommandToReachAChild(t *testing.T) {
	h, _, st := newHost(t)
	h.SteerCommand = func(profileID string) string { return "'/bin/monoagentcli' --profile '" + profileID + "'" }
	family(t, st, 1)
	setSettings(t, st, "m", `{"skip_if_unchanged": false}`)
	h.checkinPass(t0)
	h.checkinPass(t0.Add(16 * time.Minute))
	got := checkins(t, st)
	if len(got) != 1 {
		t.Fatalf("check-ins = %+v", got)
	}
	msg := got[0].Message
	for _, want := range []string{"'/bin/monoagentcli' --profile 'p' coder sessions send", "coder sessions status"} {
		if !strings.Contains(msg, want) {
			t.Errorf("digest lacks %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "not `monomind`") {
		t.Errorf("digest does not steer away from whatever monomind is on PATH:\n%s", msg)
	}
}

// Without a command (a host with none wired) the digest is what it was.
func TestTheDigestHasNoCommandLineWithoutOne(t *testing.T) {
	if strings.Contains(digestText("scheduled", snapshot{Children: []childFact{{ID: "k1", State: StateIdle}}}), "coder sessions") {
		t.Fatal("digestText itself names a command")
	}
}
