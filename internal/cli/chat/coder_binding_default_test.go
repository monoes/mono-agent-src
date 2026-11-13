package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The default of the peer-credential setting is ON where the machine can run the binding service. These tests fail if the
// default flips back to off, if an explicit off is lost, or if an unsupported machine is refused instead of degraded.
func machineBinding(t *testing.T, why string) {
	t.Helper()
	old := bindingReadiness
	bindingReadiness = func() string { return why }
	t.Cleanup(func() { bindingReadiness = old })
}

func TestBindingDefaultIsOnWhereTheMachineCanRunIt(t *testing.T) {
	machineBinding(t, "")
	if r := (coderSettings{}).resolveBinding(); !r.On || r.Explicit || r.Source != bindingSourceDefault {
		t.Fatalf("unset on a capable machine: %+v", r)
	}
	if r := (coderSettings{PeercredBinding: bindingSet(false)}).resolveBinding(); r.On || !r.Explicit {
		t.Fatalf("explicit off was not kept: %+v", r)
	}
	dbPath := newChatCLITestDB(t)
	out, code := runCoderCLI(t, dbPath, "status")
	if code != 0 || !strings.Contains(out, `"peercredBindingEffective":true`) || !strings.Contains(out, `"peercredBindingSource":"default"`) {
		t.Fatalf("status (%d): %s", code, out)
	}
}

// An explicit off is stored as false (not dropped like the old omitempty bool) and survives another setting's save, a reload
// and `coder set` of something else; only an explicit on changes it.
func TestExplicitBindingOffIsPreserved(t *testing.T) {
	machineBinding(t, "")
	asOperator(t)
	dbPath := newChatCLITestDB(t)
	if _, code := runCoderCLI(t, dbPath, "set", "--peercred-binding=off"); code != 0 {
		t.Fatalf("set off: exit %d", code)
	}
	if got := storedCoderJSON(t, dbPath); string(got["peercredBinding"]) != "false" {
		t.Fatalf("an explicit off is not stored: %v", got)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--box=compat"); code != 0 {
		t.Fatalf("set box: exit %d", code)
	}
	out, _ := runCoderCLI(t, dbPath, "status")
	if !strings.Contains(out, `"peercredBinding":false`) || !strings.Contains(out, `"peercredBindingEffective":false`) || !strings.Contains(out, `"peercredBindingSource":"set"`) {
		t.Fatalf("the explicit off did not survive: %s", out)
	}
	g, inner := gateRig(t, `{"enabled":true,"peercredBinding":false}`)
	if _, err := g.Start("p", "c", "t", "m", nil, ""); err != nil || len(inner.calls) != 1 {
		t.Fatalf("explicit off must pass through: %v %d", err, len(inner.calls))
	}
}

// Where the machine cannot run the service the default is the previous behaviour with the honest reason in `coder status`
// (text and JSON) and the host still starts a turn.
func TestBindingDefaultDegradesWhereUnsupported(t *testing.T) {
	machineBinding(t, "the kernel cannot tell which process connected")
	dbPath := newChatCLITestDB(t)
	out, code := runCoderCLI(t, dbPath, "status")
	if code != 0 || !strings.Contains(out, `"peercredBindingEffective":false`) || !strings.Contains(out, "the kernel cannot tell which process connected") {
		t.Fatalf("status (%d): %s", code, out)
	}
	if !strings.Contains(out, `"peercredBindingUnbound":"the kernel cannot tell which process connected"`) {
		t.Fatalf("status json lacks the unbound reason: %s", out)
	}
	if text := bindingStatusText(coderStatus{PeercredBindingSource: bindingSourceDefault, PeercredBindingNote: (coderSettings{}).resolveBinding().Note, PeercredBindingUnbound: "the kernel cannot tell which process connected"}); !strings.HasPrefix(text, "off (") || !strings.Contains(text, "cannot run it") || !strings.Contains(text, "running without peer-credential binding: the kernel cannot tell which process connected") {
		t.Fatalf("status text: %q", text)
	}
	g, inner := gateRig(t, `{"enabled":true}`)
	g.svc = &fakeService{}
	g.reason = func(bool) string { return "the kernel cannot tell which process connected" }
	if _, err := g.Start("p", "c", "t", "m", nil, ""); err != nil || len(inner.calls) != 1 {
		t.Fatalf("a start on an unsupported machine was refused: %v (calls %d)", err, len(inner.calls))
	}
	// an explicit on is still refused, never weakened
	g2, inner2 := gateRig(t, `{"enabled":true,"peercredBinding":true}`)
	g2.reason = g.reason
	if _, err := g2.Start("p", "c", "t", "m", nil, ""); err == nil || len(inner2.calls) != 0 {
		t.Fatalf("an explicit on went through on an unsupported machine: %v", err)
	}
}

// The default is on and capable, but the service fails to come up (or the host has no bound launcher): the turn still starts,
// unbound, with a logged reason; the host is never locked out by a default.
func TestBindingDefaultDegradesWhenTheServiceFailsToInitialise(t *testing.T) {
	machineBinding(t, "")
	g, inner := gateRig(t, `{"enabled":true}`)
	g.svc = &fakeService{}
	var logged []string
	g.logf = func(f string, a ...any) { logged = append(logged, f) }
	g.reason = func(running bool) string {
		if running {
			return ""
		}
		return "its service is not running"
	}
	g.svc = failingService{}
	if _, err := g.Start("p", "c", "t", "m", nil, ""); err != nil || len(inner.calls) != 1 {
		t.Fatalf("start refused although the default could not initialise: %v", err)
	}
	if len(logged) == 0 {
		t.Fatal("the degrade was not logged")
	}
	g2, inner2 := gateRig(t, `{"enabled":true,"peercredBinding":true}`)
	g2.svc, g2.reason = failingService{}, g.reason
	if _, err := g2.Start("p", "c", "t", "m", nil, ""); err == nil || len(inner2.calls) != 0 {
		t.Fatalf("an explicit on must still refuse: %v", err)
	}
}

type failingService struct{}

func (failingService) Ensure(_ context.Context, _ string) error { return errors.New("no socket") }
func (failingService) Running(string) bool                      { return false }
