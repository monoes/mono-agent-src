package agent

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/monomind/sandboxtest"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A turn that reports no verdict does not keep the incoming item's, which was
// another turn's.
func TestAskNodeDropsAnIncomingSandboxVerdict(t *testing.T) {
	sandboxtest.Install(t, sandboxtest.Kinds[0], "hello")
	orig := execTurn
	t.Cleanup(func() { execTurn = orig })
	execTurn = func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
		return &monomind.TurnResult{ResultText: "hello"}, nil
	}
	out, err := (&AskNode{}).Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{{JSON: map[string]interface{}{"_agent_sandbox": "off"}}},
	}, map[string]interface{}{"runtime": "codex", "prompt": "say hi"})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := out[0].Items[0].JSON["_agent_sandbox"]; ok {
		t.Errorf("kept the incoming verdict %v", v)
	}
}
