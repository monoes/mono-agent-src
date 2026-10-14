package application

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/matching"
	"github.com/monoes/mono-agent/internal/monomind"
)

// These tests replace internal/matching's package-level seams, so they do not run in parallel.

func fakeEvaluateDeps(t *testing.T) {
	t.Helper()
	origEnsure := matching.EnsureFunc
	matching.EnsureFunc = func(ctx context.Context) (string, *monomind.VersionInfo, error) { return "/fake/monomind", nil, nil }
	t.Cleanup(func() { matching.EnsureFunc = origEnsure })

	origSearch := matching.SearchKnowledgeFunc
	matching.SearchKnowledgeFunc = func(ctx context.Context, db *sql.DB, profileID, query string) ([]monomind.KnowledgeResult, error) {
		return nil, nil
	}
	t.Cleanup(func() { matching.SearchKnowledgeFunc = origSearch })

	origExec := matching.ExecFunc
	matching.ExecFunc = func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		return &monomind.TurnResult{ResultText: `{"eligibility_pass":true,"language_pass":true,"location_pass":true,"technical_score":80,"experience_score":80,"behavioral_score":80,"career_score":80,"overall_score":80,"verdict":"Strong Fit","rationale":"Good match."}`}, nil
	}
	t.Cleanup(func() { matching.ExecFunc = origExec })
}

func TestApplicationEvaluate(t *testing.T) {
	fakeEvaluateDeps(t)
	dbPath := homeDB(t)

	addOut, err := run(t, dbPath, true, "add", "--kind", "job", "--title", "Backend Engineer", "--company", "Acme", "--url", "https://acme.example/1")
	if err != nil {
		t.Fatalf("application add: %v (%s)", err, addOut)
	}
	var id string
	for _, tok := range strings.Split(addOut, `"`) {
		if len(tok) == 36 && strings.Count(tok, "-") == 4 {
			id = tok
			break
		}
	}
	if id == "" {
		t.Fatalf("could not extract id from add output: %s", addOut)
	}

	evalOut, err := run(t, dbPath, true, "evaluate", id)
	if err != nil {
		t.Fatalf("application evaluate: %v (%s)", err, evalOut)
	}
	if !strings.Contains(evalOut, "Strong Fit") {
		t.Fatalf("expected verdict in output, got: %s", evalOut)
	}
}

func TestApplicationEvaluateRejectsNotFound(t *testing.T) {
	fakeEvaluateDeps(t)
	dbPath := homeDB(t)
	if _, err := run(t, dbPath, true, "evaluate", "does-not-exist"); err == nil {
		t.Fatal("expected error for unknown id, got nil")
	}
}
