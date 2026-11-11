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
	if err := CheckSendContent(step, false, "body", "hello "+value); !errors.Is(err, ErrPublishesSecret) {
		t.Errorf("a body holding the secret: %v", err)
	}
	if err := CheckSendContent(step, true, "body", "hello "+value); err != nil {
		t.Errorf("a host on this machine is exempt: %v", err)
	}
	if err := CheckSendContent(step, false, "body", "hello"); err != nil {
		t.Errorf("a body without the secret: %v", err)
	}
	if err := CheckSendContent(ctx, false, "body", value); err != nil {
		t.Errorf("a run under no agent access: %v", err)
	}
}

// Review of #1418 slice 1 (P3): a plaintext read at Reveal level taints the
// run as a read through a reference does.
func TestARevealReadTaintsTheRun(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetAgentAccessGrant(ctx, db.DB, "default", AccessGrant{AnyHost: true, Level: "reveal"}); err != nil {
		t.Fatal(err)
	}
	run := WithAccessNode(AllowResolving(ctx, db.DB, "default", AccessOrigin{WorkflowID: "wf", ExecutionID: "x"}), "n1", "Get")
	if err := CheckPublish(run, "comm.slack", true); err != nil {
		t.Fatalf("before the read: %v", err)
	}
	if err := AuthorizeAgentReveal(run, db.DB, "default", "vs"); err != nil {
		t.Fatal(err)
	}
	if err := CheckPublish(run, "comm.slack", true); !errors.Is(err, ErrPublishesSecret) {
		t.Errorf("a publishing node after a plaintext read: %v", err)
	}
}

// The way out is named by where the step runs: a workflow is approved; a
// step run by itself has no workflow to approve.
func TestPublishRefusalNamesTheNodeAndTheWayOut(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetAgentAccessGrant(ctx, db.DB, "default", AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	for origin, want := range map[AccessOrigin]string{
		{WorkflowID: "wf", ExecutionID: "x"}: "approve the workflow",
		{}:                                   "belongs to no workflow",
	} {
		run := WithAccessNodeType(WithAccessNode(AllowResolving(ctx, db.DB, "default", origin), "n1", "Post it"), "comm.slack")
		if _, err := Resolve(run, db.DB, "default", "@secret:vs"); err != nil {
			t.Fatal(err)
		}
		for what, err := range map[string]error{
			"publish": CheckPublish(run, "comm.slack", true),
			"content": CheckSendContent(run, false, "body", "x vsecret-777"),
		} {
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), `"Post it"`) || strings.Contains(err.Error(), "vsecret-777") {
				t.Errorf("%s refusal for %+v: %v, want the node's name and %q, never the value", what, origin, err, want)
			}
		}
	}
}

// A form of a value that is also another secret's raw value is named for the
// secret that holds it raw (the refusal's name, track's preference).
func TestTrackPrefersTheSecretThatHoldsAFormRaw(t *testing.T) {
	a := &accessState{seen: map[string]struct{}{}, names: map[string]struct{}{}, nodes: map[string]struct{}{}}
	const raw = "c2VjcmV0LXZhbHVl" // base64 of "secret-value"
	a.track("holder", "secret-value")
	a.track("other", raw) // the raw value of "other" is the base64 form of "holder"
	if got := a.forms[raw].name; got != "other" {
		t.Errorf("%q is named for %q, want the secret that holds it raw (other)", raw, got)
	}
	a2 := &accessState{seen: map[string]struct{}{}, names: map[string]struct{}{}, nodes: map[string]struct{}{}}
	a2.track("other", raw)
	a2.track("holder", "secret-value") // arrives later: must not take the form from the raw holder
	if got := a2.forms[raw].name; got != "other" {
		t.Errorf("a later secret took %q from its raw holder: named %q", raw, got)
	}
}
