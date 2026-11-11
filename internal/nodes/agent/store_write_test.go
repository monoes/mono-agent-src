package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// guarded runs a node the way the engine does: through the guard seam, then
// the node unless the guard held it.
func guarded(ctx context.Context, node workflow.NodeExecutor, in workflow.NodeInput, cfg map[string]interface{}) error {
	if _, handled, err := workflow.RunExecGuardFor(ctx, node, in, cfg); handled {
		return err
	}
	_, err := node.Execute(ctx, in, cfg)
	return err
}

// Issue #1418 (cross-run persistence): agent.memory's insert writes message
// content that a later run reads back (agent.memory get, agent.ask). A tainted
// run may not store a value it read there, whether the value is in the
// message or reached through another node's output.
func TestAgentAccessTaintedRunDoesNotWriteASecretIntoChatMemory(t *testing.T) {
	keyring.MockInit()
	db := testdb.Open(t).DB
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "default")
	const value = "vsecret-777"
	if _, err := secrets.Add(base, db, "default", "secret", "vs", map[string]string{"secret": value}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(base, db, "default", secrets.AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	ctx := secrets.WithAccessNodeType(secrets.WithAccessNode(secrets.AllowResolving(base, db, "default", secrets.AccessOrigin{WorkflowID: "wf1"}), "n1", "Memory"), "agent.memory")
	cfg := map[string]interface{}{"x": "@secret:vs"}
	if err := secrets.ResolveConfig(ctx, db, "default", map[string]interface{}{"x": "@secret:vs"}, cfg); err != nil {
		t.Fatal(err)
	}
	node := &MemoryNode{}
	for name, c := range map[string]struct {
		in  workflow.NodeInput
		cfg map[string]interface{}
	}{
		"in the message": {workflow.NodeInput{Items: session("s1")}, map[string]interface{}{"operation": "insert",
			"messages": []interface{}{map[string]interface{}{"role": "user", "content": cfg["x"]}}}},
		"through another node's output": {workflow.NodeInput{Items: session("s2"),
			NodeOutputs: map[string][]workflow.Item{"Src": {workflow.NewItem(map[string]interface{}{"v": value})}}},
			map[string]interface{}{"operation": "insert",
				"messages": []interface{}{map[string]interface{}{"role": "user", "content": `{{ $node["Src"].json.v }}`}}}},
	} {
		err := guarded(ctx, node, c.in, c.cfg)
		if !errors.Is(err, secrets.ErrPublishesSecret) {
			t.Errorf("%s: a tainted run wrote the secret into chat memory (issue #1418): %v", name, err)
		}
	}
	// A fresh run (clean access state) reads the sessions back: nothing there.
	for _, sid := range []string{"s1", "s2"} {
		if got := stored(t, db, "default", sid); len(got) != 0 {
			t.Errorf("session %s holds %v for the next run to read", sid, got)
		}
	}
}
