package matching

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

// The verdict says when its turn ran without a sandbox, and only then.
func TestVerdictSaysItsTurnRanWithoutASandbox(t *testing.T) {
	db, applicationID := setupEvaluateTest(t)
	orig := ExecFunc
	t.Cleanup(func() { ExecFunc = orig })
	for status, want := range map[string]string{
		monomind.SandboxStatusUnsupported: monomind.SandboxStatusUnsupported,
		monomind.SandboxStatusSandboxed:   "",
		"":                                "",
	} {
		ExecFunc = func(ctx context.Context, o monomind.ExecOptions, f func(monomind.Event)) (*monomind.TurnResult, error) {
			res, err := fakeGoodResponse(ctx, o, f)
			res.SandboxStatus = status
			return res, err
		}
		v, err := Evaluate(context.Background(), db, "default", applicationID, "claude")
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if v.SandboxStatus != want {
			t.Errorf("status %q: verdict says %q, want %q", status, v.SandboxStatus, want)
		}
	}
}

// The model's own reply cannot set the status.
func TestVerdictSandboxStatusIsNotTheModels(t *testing.T) {
	db, applicationID := setupEvaluateTest(t)
	orig := ExecFunc
	t.Cleanup(func() { ExecFunc = orig })
	ExecFunc = func(ctx context.Context, o monomind.ExecOptions, f func(monomind.Event)) (*monomind.TurnResult, error) {
		res, err := fakeGoodResponse(ctx, o, f)
		res.ResultText = `{"sandbox_status":"off",` + res.ResultText[1:]
		res.SandboxStatus = monomind.SandboxStatusSandboxed
		return res, err
	}
	v, err := Evaluate(context.Background(), db, "default", applicationID, "claude")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if v.SandboxStatus != "" {
		t.Errorf("the model's reply set the sandbox status: %q", v.SandboxStatus)
	}
}
