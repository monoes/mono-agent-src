package action

import (
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"testing"
)

// A security refusal ends the run even when the step says onError continue.
func TestRefusalsAreFatal(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	imp := &coreTrustPkg{fakePkg: fakePkg{id: "imp"}, trust: "imported"}
	def := &ActionDef{ActionType: "t", SideEffects: "read", Steps: []StepDef{
		{ID: "up", Type: "upload", Selector: "#f", Text: "/etc/passwd", OnError: &ErrorHandlerDef{Action: ErrorActionContinue}},
		{ID: "after", Type: "log", Text: "must not run"},
	}}
	ae := newPkgExecutor(&markPage{}, imp)
	_, err := ae.ExecuteDef(&StorageAction{ID: "a", TargetPlatform: "imp", Type: "t"}, def)
	if !errors.Is(err, ErrRefused) || !errors.Is(err, ErrAbort) {
		t.Fatalf("err = %v, want a fatal refusal", err)
	}
	if ae.execCtx.GetStepResult("after") != nil {
		t.Fatal("run continued past a refusal")
	}

	for name, e := range map[string]error{
		"call_action": &CallActionError{Code: "call_action_not_allowed", Msg: "x"},
		"live run":    newPkgExecutor(nil, nil).checkLiveRun(&coreTrustPkg{fakePkg: fakePkg{id: "i"}, live: boolPtr(false)}, &ActionDef{SideEffects: "write"}),
		"upload":      ErrUploadNotAllowed,
	} {
		if !isFatalCause(e) {
			t.Errorf("%s refusal is not fatal: %v", name, e)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// A declarative package action aborts on a failed step without onError;
// legacy, local-* and native built-in actions keep master's continue.
func TestDefaultOnErrorByPackageKind(t *testing.T) {
	def := &ActionDef{ActionType: "t", SideEffects: "read", Steps: []StepDef{
		{ID: "miss", Type: "click", Selector: "#gone", Timeout: 0.01},
		{ID: "after", Type: "log", Text: "next"},
	}}
	cases := []struct {
		name  string
		pkg   PackageContext
		abort bool
	}{
		{"declarative", &fakePkg{id: "acme"}, true},
		{"legacy", nil, false},
		{"local", &fakePkg{id: "local-foo"}, false},
		{"native builtin", &fakePkg{id: "instagram", native: "instagram"}, false},
	}
	for _, c := range cases {
		ae := newPkgExecutor(&markPage{}, c.pkg)
		_, err := ae.ExecuteDef(&StorageAction{ID: "a", TargetPlatform: "p", Type: "t"}, def)
		ran := ae.execCtx.GetStepResult("after") != nil
		if c.abort && (err == nil || !errors.Is(err, ErrAbort) || ran) {
			t.Errorf("%s: err=%v ran-after=%v, want abort", c.name, err, ran)
		}
		if !c.abort && (err != nil || !ran) {
			t.Errorf("%s: err=%v ran-after=%v, want continue", c.name, err, ran)
		}
	}

	// An explicit onError still wins in a declarative action.
	def.Steps[0].OnError = &ErrorHandlerDef{Action: ErrorActionContinue}
	ae := newPkgExecutor(&markPage{}, &fakePkg{id: "acme"})
	if _, err := ae.ExecuteDef(&StorageAction{ID: "a", TargetPlatform: "p", Type: "t"}, def); err != nil {
		t.Fatalf("explicit continue: %v", err)
	}
}
