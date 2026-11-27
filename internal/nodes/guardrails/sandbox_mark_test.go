package guardrails

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

// An item judged by a turn that ran without a sandbox says so; an item judged
// in the sandbox does not.
func TestItemSaysItsJudgeTurnRanWithoutASandbox(t *testing.T) {
	fakeTurns(t, func(string, string) string { return `{"flagged": false, "confidenceScore": 0.1}` })
	answer := execTurn
	execTurn = func(ctx context.Context, o monomind.ExecOptions, f func(monomind.Event)) (*monomind.TurnResult, error) {
		if strings.Contains(o.Prompt, "loose") {
			monomind.NoteSandbox(ctx, monomind.SandboxStatusUnsupported) // as Exec does
		}
		return answer(ctx, o, f)
	}
	got := run(t, map[string]interface{}{"runtime": "opencode", "jailbreak": true}, "loose text", "confined text")[HandlePass]
	if len(got) != 2 {
		t.Fatalf("pass %d", len(got))
	}
	if got[0].JSON["_agent_sandbox"] != monomind.SandboxStatusUnsupported {
		t.Errorf("unsandboxed item: %v", got[0].JSON)
	}
	if v, ok := got[1].JSON["_agent_sandbox"]; ok {
		t.Errorf("sandboxed item says %v", v)
	}
}
