package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Issue #1418: under agent access, a node that publishes is refused once the
// run has read a secret; a request's body is content, its auth is not.
func TestCheckPublishRefusesOnlyAPublishingNodeOfATaintedRun(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	const value = "vsecret-777"
	if _, err := Add(ctx, db.DB, "default", "secret", "vs", map[string]string{"secret": value}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetAgentAccessGrant(ctx, db.DB, "default", AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	run := AllowResolving(ctx, db.DB, "default", AccessOrigin{WorkflowID: "wf"})
	step := WithAccessNode(run, "n1", "Post")

	if err := CheckPublish(step, "comm.slack", true); err != nil {
		t.Errorf("a run that read nothing was refused: %v", err)
	}
	if _, err := Resolve(step, db.DB, "default", "@secret:vs"); err != nil {
		t.Fatal(err)
	}
	err := CheckPublish(step, "comm.slack", true)
	if !errors.Is(err, ErrPublishesSecret) {
		t.Fatalf("a publishing node of a run that read a secret: %v", err)
	}
	if strings.Contains(err.Error(), value) || !strings.Contains(err.Error(), "approve the workflow") || !strings.Contains(err.Error(), "comm.slack") {
		t.Errorf("the message must name the node, say the person can approve, and never hold the value: %v", err)
	}
	if err := CheckPublish(step, "data.set", false); err != nil {
		t.Errorf("a local-only node was refused: %v", err)
	}
	for _, typ := range []string{"http.request", "http.graphql", "mcp.call"} {
		if err := CheckPublish(step, typ, true); err != nil {
			t.Errorf("%s holds its own sends to the grant's hosts and is checked for content, not refused by the taint: %v", typ, err)
		}
	}
	if err := CheckPublish(ctx, "comm.slack", true); err != nil {
		t.Errorf("a run under no agent access (the user's own) was refused: %v", err)
	}

	// Content: the body, not the auth.
	if err := CheckSendContent(step, false, "hello "+value); !errors.Is(err, ErrPublishesSecret) {
		t.Errorf("a body holding the secret: %v", err)
	}
	if err := CheckSendContent(step, true, "hello "+value); err != nil {
		t.Errorf("a host on this machine is exempt: %v", err)
	}
	if err := CheckSendContent(step, false, "hello"); err != nil {
		t.Errorf("a body without the secret: %v", err)
	}
	if err := CheckSendContent(ctx, false, value); err != nil {
		t.Errorf("a run under no agent access: %v", err)
	}
}
