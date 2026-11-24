package application

import (
	"errors"
	"strings"
	"testing"
)

// `application apply` opens the job URL in the operator's real browser, so an agent is refused (R10-14).
func TestApplyIsOperatorOnly(t *testing.T) {
	dbPath := homeDB(t)
	id := addJob(t, dbPath)
	old := FillStartWiring.RequireOperator
	t.Cleanup(func() { FillStartWiring.RequireOperator = old })
	FillStartWiring.RequireOperator = func(what string) error { return errors.New("only the user can " + what) }
	if _, err := run(t, dbPath, true, "apply", id); err == nil || !strings.Contains(err.Error(), "only the user") {
		t.Fatalf("agent apply: %v", err)
	}
	FillStartWiring.RequireOperator = nil // unwired: refused too
	if _, err := run(t, dbPath, true, "apply", id); err == nil {
		t.Fatal("an unwired operator check let apply through")
	}
}
