package application

import (
	"errors"
	"strings"
	"testing"
)

func TestSendAndStatusSetAreOperatorOnly(t *testing.T) {
	dbPath := homeDB(t)
	id := addJob(t, dbPath)
	old := FillStartWiring.RequireOperator
	t.Cleanup(func() { FillStartWiring.RequireOperator = old })
	refuse := func(what string) error { return errors.New("only the user can " + what) }
	for _, args := range [][]string{{"send", id}, {"status", id, "set", "cancelled"}} {
		FillStartWiring.RequireOperator = refuse
		if _, err := run(t, dbPath, true, args...); err == nil || !strings.Contains(err.Error(), "only the user") {
			t.Fatalf("agent %v: %v", args, err)
		}
		FillStartWiring.RequireOperator = nil // unwired: refused too
		if _, err := run(t, dbPath, true, args...); err == nil {
			t.Fatalf("an unwired operator check let %v through", args)
		}
	}
	FillStartWiring.RequireOperator = func(string) error { return nil }
	if out, err := run(t, dbPath, true, "get", id); err != nil || !strings.Contains(out, "pending") {
		t.Fatalf("refused calls changed the application: %v %s", err, out)
	}
}
