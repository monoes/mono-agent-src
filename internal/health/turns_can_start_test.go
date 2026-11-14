package health

import (
	"context"
	"strings"
	"testing"
)

func TestCoderTurnsCanStartRow(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*OpenModeFacts)
		status Status
		text   string
		fix    string
	}{
		{"binding off, no run: a plain turn starts", func(f *OpenModeFacts) { f.BindingOn = false }, StatusOK, "plain turns start", ""},
		{"binding on and ready", func(*OpenModeFacts) {}, StatusOK, "bound launcher", ""},
		{"binding on, machine cannot bind", func(f *OpenModeFacts) { f.BindingReadyErr = "needs Linux 6.5" }, StatusWarn, "every turn is refused", "coder set --peercred-binding off"},
		{"binding off with a run open", func(f *OpenModeFacts) { f.BindingOn, f.ActiveRun = false, true }, StatusWarn, "refuses every turn", "doomsday stop"},
		{"setting unreadable", func(f *OpenModeFacts) { f.BindingErr = "locked" }, StatusWarn, "cannot read", ""},
		{"run table unreadable", func(f *OpenModeFacts) { f.BindingOn, f.ActiveErr = false, "busy" }, StatusWarn, "cannot read whether", ""},
	}
	for _, c := range cases {
		f := readyFacts()
		c.mut(&f)
		res := checkTurnsCanStart(context.Background(), openModeEnv(f, "ok"))
		if res.Status != c.status || !strings.Contains(res.Summary, c.text) || (c.fix != "" && !strings.Contains(res.Detail, c.fix)) {
			t.Errorf("%s: %+v", c.name, res)
		}
	}
	if res := checkTurnsCanStart(context.Background(), &Env{ProfileID: "P"}); res.Status != StatusSkip {
		t.Errorf("no hook: %+v", res)
	}
	env := openModeEnv(readyFacts(), "ok")
	env.ProfileID = ""
	if res := checkTurnsCanStart(context.Background(), env); res.Status == StatusOK || res.Status == StatusFail {
		t.Errorf("no profile: %+v", res)
	}
}

// The row is advice: it is in the registry, never required, never a failure whatever the facts.
func TestCoderTurnsCanStartIsRegisteredAndNeverFails(t *testing.T) {
	found := false
	for _, c := range turnsCanStartChecks() {
		found = found || c.ID == CheckTurnsCanStart
	}
	if !found {
		t.Fatal("no row")
	}
	for _, c := range Default().Checks() {
		if c.ID == CheckTurnsCanStart {
			if c.Required || c.Group != GroupCore {
				t.Fatalf("the row is required or outside the core group: %+v", c)
			}
			return
		}
	}
	t.Fatal("the row is not in the registry")
}
