package evalnode

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A metric scored by a turn that ran without a sandbox says so on its item.
func TestMetricItemSaysItsJudgeTurnRanWithoutASandbox(t *testing.T) {
	fakeTurns(t, func(string, string) string {
		return `{"extended_reasoning": "x", "reasoning_summary": "y", "score": 4}`
	})
	answer := execTurn
	execTurn = func(ctx context.Context, o monomind.ExecOptions, f func(monomind.Event)) (*monomind.TurnResult, error) {
		if strings.Contains(o.Prompt, "loose") {
			monomind.NoteSandbox(ctx, monomind.SandboxStatusOff) // as Exec does
		}
		return answer(ctx, o, f)
	}
	in := workflow.NodeInput{Items: items(map[string]interface{}{"out": "loose"}, map[string]interface{}{"out": "confined"})}
	got := exec(t, context.Background(), in, map[string]interface{}{"operation": "setMetrics", "metric": "correctness",
		"runtime": "claude", "expected_answer": "x", "actual_answer": "{{ $json.out }}"})["main"]
	if len(got) != 2 || got[0].JSON["_agent_sandbox"] != monomind.SandboxStatusOff || got[0].JSON["Correctness"] != 4.0 {
		t.Fatalf("items: %v", got)
	}
	if v, ok := got[1].JSON["_agent_sandbox"]; ok {
		t.Errorf("sandboxed item says %v", v)
	}
}
