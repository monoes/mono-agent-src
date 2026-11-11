package workflow

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/secrets"
)

// Issue #1418: a granted @secret: must not be published as content. A node
// that publishes to third parties is refused, under agent access, when its
// run has read a secret (taint) or its own config holds one. These tests are
// red until the guard lands in RunExecGuard's seam; test.publishprobe stands
// for a registered publishing node (an undeclared type is publishing).

type publishProbe struct {
	mu   sync.Mutex
	runs []string
}

func (p *publishProbe) Type() string { return "test.publishprobe" }
func (p *publishProbe) Execute(_ context.Context, _ NodeInput, config map[string]interface{}) ([]NodeOutput, error) {
	text, _ := config["text"].(string)
	p.mu.Lock()
	p.runs = append(p.runs, text)
	p.mu.Unlock()
	return []NodeOutput{{Handle: "main", Items: []Item{NewItem(map[string]interface{}{"published": true})}}}, nil
}

func (p *publishProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.runs)
}

// registerPublish registers the probe as a fresh instance and returns it.
func (e *accessEnv) registerPublish() *publishProbe {
	p := &publishProbe{}
	e.reg.Register("test.publishprobe", func() NodeExecutor { return p })
	return p
}

// P2: node A (an http.request to a granted host) echoes the secret; node B
// publishes it by data flow. B resolves no @secret: itself, so only the
// run's taint can stop it.
func TestAgentAccessPublishingNodeRefusedWhenRunReadASecret(t *testing.T) {
	e := newAccessEnv(t)
	e.reg.Register("http.request", func() NodeExecutor { return echoProbe{} })
	pub := e.registerPublish()
	e.grant(t, secrets.AccessGrant{Hosts: []string{"api.example.com"}})
	wf := chain("wf-p2",
		step("a1", "http.request", "Call", map[string]interface{}{"v": "@secret:vs"}),
		step("b1", "test.publishprobe", "Post", map[string]interface{}{"text": "{{ $json.v }}"}),
	)
	execID, err := e.run(t, wf, nil)
	if err == nil {
		t.Errorf("a publishing node ran in a run that read a granted secret (issue #1418, P2): want a refusal, got none")
	}
	if n := pub.count(); n != 0 {
		t.Errorf("the publishing node executed %d time(s) with %q; the refusal must come before anything is sent", n, pub.runs)
	}
	if strings.Contains(e.stored(t, execID), valVS) {
		t.Errorf("the stored run holds the value")
	}
}

// P3: --any-host lets every node type read a secret, and it does not lift
// the content guard: a publishing node whose own config is the reference is
// refused.
func TestAgentAccessAnyHostGrantStillRefusesSecretInPublishedContent(t *testing.T) {
	e := newAccessEnv(t)
	pub := e.registerPublish()
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	_, err := e.run(t, chain("wf-p3", step("b1", "test.publishprobe", "Post", map[string]interface{}{"text": "@secret:vs"})), nil)
	if err == nil {
		t.Errorf("--any-host let a publishing node read a granted secret as its content (issue #1418, P3): want a refusal, got none")
	}
	if n := pub.count(); n != 0 {
		t.Errorf("the publishing node executed %d time(s) with %q", n, pub.runs)
	}
}

// Controls the guard must keep: a run that read nothing publishes, and an
// operator-approved workflow is untouched (it resolves with no grant).
func TestAgentAccessPublishGuardLeavesCleanAndApprovedRunsAlone(t *testing.T) {
	e := newAccessEnv(t)
	pub := e.registerPublish()
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	if _, err := e.run(t, chain("wf-clean", step("b1", "test.publishprobe", "Post", map[string]interface{}{"text": "status: done"})), nil); err != nil {
		t.Errorf("a run that read no secret was refused: %v", err)
	}
	if pub.count() != 1 {
		t.Errorf("the clean run published %d times, want 1", pub.count())
	}
	approved := chain("wf-ok", step("b1", "test.publishprobe", "Post", map[string]interface{}{"text": "@secret:vs"}))
	createSQLWorkflow(t, e.store, approved)
	if err := e.store.ApproveDefinition(e.ctx, approved.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.store.GetWorkflow(e.ctx, approved.ID)
	dag, _ := BuildDAG(cur.Nodes, cur.Connections)
	exec := &WorkflowExecution{ID: "ok1", WorkflowID: approved.ID, ProfileID: "default", TriggerType: "trigger.schedule", TriggerNodeID: approved.ID + "-t"}
	if err := e.store.CreateExecution(e.ctx, exec); err != nil {
		t.Fatal(err)
	}
	if err := RunExecution(e.ctx, exec, cur, dag, e.reg, e.store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Errorf("an operator-approved workflow was refused: %v", err)
	}
}
