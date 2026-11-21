package main

import (
	"strings"
	"testing"
)

// The GUI's decider-kind list matches the CLI's (jev plan WS4).
func TestAutonomySetArgsDeciderKinds(t *testing.T) {
	for _, c := range []struct {
		kind string
		ok   bool
	}{{"model", true}, {"boss", true}, {"parent", true}, {"jev", true}, {"bogus", false}} {
		args, err := autonomySetArgs("growth", `{"decider":{"kind":"`+c.kind+`"}}`)
		if (err == nil) != c.ok {
			t.Errorf("kind %s: args=%v err=%v", c.kind, args, err)
		}
		if c.ok && !strings.Contains(strings.Join(args, " "), "--decider "+c.kind) {
			t.Errorf("kind %s: args %v", c.kind, args)
		}
	}
}

func TestAutonomySetArgsDeciderThreshold(t *testing.T) {
	a, err := autonomySetArgs("growth", `{"decider":{"kind":"jev","threshold":0.85}}`)
	eqArgs(t, a, err, []string{"autonomy", "set", "growth", "--by", "gui", "--decider", "jev", "--decider-threshold", "0.85"})
	a, err = autonomySetArgs("growth", `{"decider":{"threshold":1}}`)
	eqArgs(t, a, err, []string{"autonomy", "set", "growth", "--by", "gui", "--decider-threshold", "1"})
	for _, bad := range []string{"0", "-0.1", "1.05"} {
		if _, err := autonomySetArgs("growth", `{"decider":{"kind":"jev","threshold":`+bad+`}}`); err == nil || !strings.Contains(err.Error(), "threshold must be") {
			t.Errorf("threshold %s: err=%v", bad, err)
		}
	}
}

// The jev decider's provider (decision providers plan, WS5/WS6): a name is
// sent as --decider-provider, "" clears it back to the machine default, and
// a spec without it sends no flag at all (today's behaviour).
func TestAutonomySetArgsDeciderProvider(t *testing.T) {
	a, err := autonomySetArgs("growth", `{"decider":{"kind":"jev","provider":"local"}}`)
	eqArgs(t, a, err, []string{"autonomy", "set", "growth", "--by", "gui", "--decider", "jev", "--decider-provider", "local"})
	a, err = autonomySetArgs("growth", `{"decider":{"provider":""}}`)
	eqArgs(t, a, err, []string{"autonomy", "set", "growth", "--by", "gui", "--decider-provider", ""})
	a, err = autonomySetArgs("growth", `{"decider":{"kind":"jev"}}`)
	eqArgs(t, a, err, []string{"autonomy", "set", "growth", "--by", "gui", "--decider", "jev"})
	for _, bad := range []string{"-x", "--yes", "Local", "a b", "a/b"} {
		if _, err := autonomySetArgs("growth", `{"decider":{"provider":"`+bad+`"}}`); err == nil || !strings.Contains(err.Error(), "provider") {
			t.Errorf("provider %q: err=%v", bad, err)
		}
	}
}
