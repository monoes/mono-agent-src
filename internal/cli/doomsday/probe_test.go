package doomsday

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// bindingEngine is a fakeEngine that also knows whether binding is on and what status it reports.
type bindingEngine struct {
	fakeEngine
	binding error
	status  Status
	err     error
}

func (b *bindingEngine) RequireBinding(context.Context) error { return b.binding }
func (b *bindingEngine) Status(context.Context) (Status, error) { return b.status, b.err }

func probeOf(t *testing.T, e Engine) ProbeResult {
	t.Helper()
	d := newRig().deps
	d.Engine = func() Engine { return e }
	d.JSONOutput = func() bool { return true }
	out, err := run(t, d, "", "probe")
	if err != nil {
		t.Fatalf("probe: %v %s", err, out)
	}
	var r ProbeResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("probe output is not the document: %q", out)
	}
	return r
}

func TestProbeSaysWhyTheOpenModeIsOrIsNotAvailable(t *testing.T) {
	for name, c := range map[string]struct {
		e      *bindingEngine
		ok     bool
		run    bool
		reason string
	}{
		"available":         {&bindingEngine{}, true, false, ""},
		"a run exists":      {&bindingEngine{status: Status{Active: true, RunID: "p/1"}}, false, true, "already"},
		"containment fails": {&bindingEngine{fakeEngine: fakeEngine{containment: errors.New("containment unavailable: cgroup v2 delegation to the user: not writable")}}, false, false, "cgroup v2 delegation to the user"},
		"binding off":       {&bindingEngine{binding: ErrBindingOff}, false, false, "peercred-binding"},
		"status unreadable": {&bindingEngine{err: errors.New("database is busy")}, false, false, "database is busy"},
	} {
		t.Run(name, func(t *testing.T) {
			r := probeOf(t, c.e)
			if r.ModeAvailable != c.ok || r.RunExists != c.run || !strings.Contains(r.Reason, c.reason) || (!c.ok && r.Reason == "") {
				t.Fatalf("%+v", r)
			}
			if r.Platform == "" || !strings.Contains(r.NotDefended, "does NOT defend against") {
				t.Fatalf("the platform and the plain limits are part of the answer: %+v", r)
			}
		})
	}
}

func TestProbeOnTheOffEngineIsNotAvailableAndSaysSo(t *testing.T) {
	d := newRig().deps
	d.Engine = nil
	d.JSONOutput = func() bool { return true }
	out, err := run(t, d, "", "probe")
	if err != nil || !strings.Contains(out, `"mode_available":false`) || !strings.Contains(out, ErrOff.Error()) {
		t.Fatalf("%v %s", err, out)
	}
}

func TestProbeIsOperatorOnlyAndTouchesNothingWhenRefused(t *testing.T) {
	r := newRig()
	r.refusal = "an agent token is set"
	if _, err := run(t, r.deps, "", "probe"); exitOf(err) != 3 || r.e.canonCalls != 0 {
		t.Fatalf("%v", err)
	}
}
