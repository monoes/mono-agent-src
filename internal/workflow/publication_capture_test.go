package workflow

import (
	"bytes"
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/rs/zerolog"
	"strings"
	"testing"
)

type publicationFakePublisher struct {
	calls *int
	fail  bool
}

func (n publicationFakePublisher) Type() string { return "service.bluesky" }
func (n publicationFakePublisher) Execute(context.Context, NodeInput, map[string]interface{}) ([]NodeOutput, error) {
	*n.calls++
	out := []NodeOutput{{Handle: "main", Items: []Item{NewItem(map[string]interface{}{"uri": "at://post/success"}), NewItem(map[string]interface{}{"success": false})}}}
	if n.fail {
		return out, errors.New("second item failed")
	}
	return out, nil
}

func TestRunExecutionPublicationCapture(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "partial_failure"}[fail], func(t *testing.T) {
			_, db := newMigratedStore(t)
			ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "process-profile")
			calls := 0
			reg := NewNodeTypeRegistry()
			reg.Register("service.bluesky", func() NodeExecutor { return publicationFakePublisher{calls: &calls, fail: fail} })
			wf := &Workflow{ID: "wf", Nodes: []WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "Trigger"}, {ID: "p", Type: "service.bluesky", Name: "Publish", Config: map[string]interface{}{"operation": "create_post", "text": "resolved {{ $json.body }}"}}}, Connections: []WorkflowConnection{{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "p", TargetHandle: "main"}}}
			dag, err := BuildDAG(wf.Nodes, wf.Connections)
			if err != nil {
				t.Fatal(err)
			}
			exec := &WorkflowExecution{ID: "execution", WorkflowID: wf.ID, ProfileID: "execution-profile", TriggerType: TriggerTypeOrgTool, TriggerData: map[string]interface{}{"body": "content", "org": map[string]interface{}{"name": "org", "role": "writer"}}}
			err = RunExecution(ctx, exec, wf, dag, reg, &stubStore{}, nil, NewExpressionEngine(), zerolog.Nop())
			if fail != (err != nil) {
				t.Fatalf("execution outcome changed: %v", err)
			}
			if calls != 1 {
				t.Fatalf("publisher retried %d times", calls)
			}
			records, err := publication.NewStore(db, "execution-profile").List(context.Background(), publication.Filter{})
			if err != nil || len(records) != 1 {
				t.Fatalf("records: %+v %v", records, err)
			}
			e := records[0]
			if e.Body != "resolved content" || e.OrgID != "org" || e.RoleID != "writer" || e.WorkflowID != "wf" || e.ExecutionID != "execution" || e.NodeID != "p" {
				t.Fatalf("bad capture: %+v", e)
			}
			records, _ = publication.NewStore(db, "process-profile").List(context.Background(), publication.Filter{})
			if len(records) != 0 {
				t.Fatal("captured into daemon process profile")
			}
		})
	}
}

func TestTrackingFailureDoesNotRetryPublisher(t *testing.T) {
	_, db := newMigratedStore(t)
	if _, err := db.Exec("ALTER TABLE publications RENAME TO unavailable_publications"); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "default")
	calls := 0
	reg := NewNodeTypeRegistry()
	reg.Register("service.bluesky", func() NodeExecutor { return publicationFakePublisher{calls: &calls} })
	wf := &Workflow{ID: "wf", Nodes: []WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "Trigger"}, {ID: "p", Type: "service.bluesky", Name: "Publish", Config: map[string]interface{}{"operation": "create_post", "text": "text", "retry_count": 3}}}, Connections: []WorkflowConnection{{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "p", TargetHandle: "main"}}}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	err = RunExecution(ctx, &WorkflowExecution{ID: "execution", WorkflowID: wf.ID, ProfileID: "default"}, wf, dag, reg, &stubStore{}, nil, NewExpressionEngine(), zerolog.New(&logs))
	if err != nil || calls != 1 || !strings.Contains(logs.String(), "publication tracking failed") {
		t.Fatalf("tracking failure changed publisher: calls=%d err=%v logs=%s", calls, err, &logs)
	}
}

func TestPublicationSourceIgnoresUntrustedPayload(t *testing.T) {
	data := map[string]interface{}{"org": map[string]interface{}{"name": "forged", "role": "forged"}}
	source := PublicationSource(WithTrigger(context.Background(), "trigger.manual", data), NodeInput{ExecutionID: "run"})
	if source.OrgID != "" || source.RoleID != "" {
		t.Fatalf("trusted arbitrary input: %+v", source)
	}
}

func TestPublicationSourceOrgEvent(t *testing.T) {
	ctx := WithTrigger(context.Background(), TriggerNodeTypeOrg, map[string]interface{}{"org": "research", "event": map[string]interface{}{"from": "research:writer"}})
	source := PublicationSource(ctx, NodeInput{ExecutionID: "run"})
	if source.OrgID != "research" || source.RoleID != "writer" || source.AgentID != "research:writer" {
		t.Fatalf("lost event provenance: %+v", source)
	}
}
