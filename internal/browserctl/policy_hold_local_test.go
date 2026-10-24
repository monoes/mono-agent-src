package browserctl

import "testing"

// The extension's confined tasks work on whatever page the person has open, so
// a form submit waits for a person on local and dev pages too. Without
// HoldLocalSubmits (every other caller) a local submit is not held.
func TestHoldLocalSubmitsGatesLocalPagesToo(t *testing.T) {
	local := []string{"http://localhost:3000/pay", "http://192.168.1.1/admin", "http://wiki.local/edit", "https://intranet.test/", "http://127.0.0.1:8080/"}
	for _, level := range []string{LevelAsk, LevelDev, LevelFull} {
		other := Policy{Agent: true, Kind: KindApply, Level: level}
		held := other
		held.HoldLocalSubmits = true
		for _, u := range local {
			if other.SubmitGated(u) {
				t.Errorf("%s: %s is gated without HoldLocalSubmits", level, u)
			}
			if !held.SubmitGated(u) {
				t.Errorf("%s: %s is not gated with HoldLocalSubmits", level, u)
			}
		}
		if !held.SubmitGated("https://allowed.example/") && level == LevelFull {
			t.Errorf("%s: a real site stopped being gated", level)
		}
	}
	// A page the policy refuses is not "allowed", so nothing is gated (nothing runs there at all).
	if (Policy{Agent: true, Kind: KindApply, Level: LevelDev, HoldLocalSubmits: true}).SubmitGated("https://evil.example/") {
		t.Error("a refused origin is gated")
	}
	// No level lets it through unasked.
	if (Policy{Agent: true, Kind: KindApply, Level: LevelFullNoApprovals, HoldLocalSubmits: true}).SubmitGated("http://localhost/") {
		t.Error("full-no-approvals is gated")
	}
}
