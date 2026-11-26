package browserjev

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The node's item says when its text helper's turn ran without a sandbox, and
// never carries that mark over from an incoming item.
func TestItemSaysItsTextTurnRanWithoutASandbox(t *testing.T) {
	for _, status := range []string{monomind.SandboxStatusUnsupported, monomind.SandboxStatusSandboxed} {
		policyServer(t, nil)
		n := testNode(&fakeDriver{}, func(ctx context.Context, _ map[string]any) (string, error) {
			monomind.NoteSandbox(ctx, status) // as Exec does
			return "zurich", nil
		})
		in := workflow.NodeInput{Items: []workflow.Item{{JSON: map[string]interface{}{"_text_agent_sandbox": "off", "keep": 1}}}}
		out, err := n.Execute(testCtx(), in, map[string]interface{}{"url": "https://example.test/", "goal": "Search for zurich", "api_key": "k"})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		got := out[0].Items[0].JSON
		v, ok := got["_text_agent_sandbox"]
		if monomind.SandboxMissing(status) {
			if v != status {
				t.Errorf("%s: mark = %v", status, v)
			}
		} else if ok {
			t.Errorf("%s: sandboxed item says %v", status, v)
		}
		if got["keep"] != 1 {
			t.Errorf("incoming data lost: %v", got["keep"])
		}
	}
}
