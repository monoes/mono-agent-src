package action

import (
	"context"
	"errors"
	"github.com/rs/zerolog"
	"testing"
)

// Publication observers receive exact resolved arguments once after success,
// even when a subsequent step fails and the action cannot return final output.
func TestSuccessfulStepObserverUsesResolvedSuccessfulItems(t *testing.T) {
	ae := NewActionExecutor(context.Background(), nil, nil, nil, nil, nil, zerolog.Nop())
	ae.action = &StorageAction{ID: "action"}
	ae.execCtx.SetVariable("commentText", "personalized comment")
	calls := 0
	ae.handlers["test_publish"] = func(context.Context, StepDef) (*StepResult, error) {
		calls++
		return &StepResult{Success: true, Data: map[string]interface{}{"success": true}}, nil
	}
	ae.handlers["test_fail"] = func(context.Context, StepDef) (*StepResult, error) { return nil, errors.New("later target failed") }
	observed := []StepDef{}
	ae.SuccessfulStep = func(step StepDef, result *StepResult) { observed = append(observed, step) }
	err := ae.executeSteps(context.Background(), []StepDef{{ID: "published", Type: "test_publish", Args: []interface{}{"{{commentText}}"}}, {ID: "failed", Type: "test_fail", OnError: &ErrorHandlerDef{Action: "abort"}}})
	if err == nil {
		t.Fatal("expected later failure")
	}
	if calls != 1 || len(observed) != 1 || len(observed[0].Args) != 1 || observed[0].Args[0] != "personalized comment" {
		t.Fatalf("observed %+v, remote calls %d", observed, calls)
	}
}
