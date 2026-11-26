package matchingnodes_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/matching"
	"github.com/monoes/mono-agent/internal/monomind"
	matchingnodes "github.com/monoes/mono-agent/internal/nodes/matching"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The node's item says when the evaluating turn ran without a sandbox.
func TestEvaluateNodeSaysItsTurnRanWithoutASandbox(t *testing.T) {
	origEnsure, origSearch, origExec := matching.EnsureFunc, matching.SearchKnowledgeFunc, matching.ExecFunc
	t.Cleanup(func() {
		matching.EnsureFunc, matching.SearchKnowledgeFunc, matching.ExecFunc = origEnsure, origSearch, origExec
	})
	matching.EnsureFunc = func(context.Context) (string, *monomind.VersionInfo, error) { return "/fake/monomind", nil, nil }
	matching.SearchKnowledgeFunc = func(context.Context, *sql.DB, string, string) ([]monomind.KnowledgeResult, error) { return nil, nil }

	db := newTestDB(t)
	matchingnodes.SetGlobalDB(db.DB)
	app := &applications.Application{ProfileID: "default", Kind: applications.KindJob,
		Job: &applications.JobDetails{Title: "Backend Engineer", Company: "Acme", URL: "https://a.example"}}
	if err := applications.NewStore(db.DB).Create(context.Background(), app); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, status := range []string{monomind.SandboxStatusOff, monomind.SandboxStatusSandboxed} {
		matching.ExecFunc = func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
			return &monomind.TurnResult{SandboxStatus: status, ResultText: `{"eligibility_pass":true,"language_pass":true,"location_pass":true,"technical_score":80,"experience_score":80,"behavioral_score":80,"career_score":80,"overall_score":80,"verdict":"Strong Fit","rationale":"Good."}`}, nil
		}
		out, err := (&matchingnodes.EvaluateNode{}).Execute(vault.ContextWithProfileID(context.Background(), "default"), workflow.NodeInput{}, map[string]interface{}{"application_id": app.ID})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		v, ok := out[0].Items[0].JSON["_agent_sandbox"]
		if monomind.SandboxMissing(status) && v != status {
			t.Errorf("%s: mark = %v", status, v)
		}
		if !monomind.SandboxMissing(status) && ok {
			t.Errorf("%s: sandboxed item says %v", status, v)
		}
	}
}
