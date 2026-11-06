package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
)

func newStateDB(t *testing.T) *sql.DB {
	t.Helper()
	return testdb.Open(t).DB
}

func stateInput(wf, node string) NodeInput {
	return NodeInput{WorkflowID: wf, NodeID: node, ExecutionID: "e1"}
}

func checkSeen(t *testing.T, s *SeenKeys, values ...string) []bool {
	t.Helper()
	fresh, err := s.Check(context.Background(), values)
	if err != nil {
		t.Fatalf("Check(%v): %v", values, err)
	}
	return fresh
}

func TestSeenKeys_RemembersAcrossInstances(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newStateDB(t))
	s1, err := SeenKeysFor(ctx, stateInput("wf1", "n1"), StateScopeNode, SeenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := checkSeen(t, s1, "a", "b", "a"); fmt.Sprint(got) != "[true true false]" {
		t.Fatalf("first run = %v, want [true true false]", got)
	}
	// A later execution gets a fresh handle over the same rows.
	s2, _ := SeenKeysFor(ctx, stateInput("wf1", "n1"), StateScopeNode, SeenOptions{})
	if got := checkSeen(t, s2, "b", "c"); fmt.Sprint(got) != "[false true]" {
		t.Fatalf("second run = %v, want [false true]", got)
	}
}

func TestSeenKeys_ScopesAreIsolated(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newStateDB(t))
	n1, _ := SeenKeysFor(ctx, stateInput("wf1", "n1"), StateScopeNode, SeenOptions{})
	n2, _ := SeenKeysFor(ctx, stateInput("wf1", "n2"), StateScopeNode, SeenOptions{})
	other, _ := SeenKeysFor(ctx, stateInput("wf2", "n1"), StateScopeNode, SeenOptions{})
	wfA, _ := SeenKeysFor(ctx, stateInput("wf1", "n1"), StateScopeWorkflow, SeenOptions{})
	wfB, _ := SeenKeysFor(ctx, stateInput("wf1", "n2"), StateScopeWorkflow, SeenOptions{})

	checkSeen(t, n1, "x")
	if got := checkSeen(t, n2, "x"); !got[0] {
		t.Error("another node of the same workflow must not see n1's values")
	}
	if got := checkSeen(t, other, "x"); !got[0] {
		t.Error("another workflow must not see wf1's values")
	}
	if got := checkSeen(t, wfA, "x"); !got[0] {
		t.Error("workflow scope must not see node-scoped values")
	}
	if got := checkSeen(t, wfB, "x"); got[0] {
		t.Error("workflow scope must be shared by every node of the workflow")
	}
}

func TestSeenKeys_HistorySizeKeepsMostRecentlySeen(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newStateDB(t))
	s, _ := SeenKeysFor(ctx, stateInput("wf1", "n1"), StateScopeNode, SeenOptions{HistorySize: 2})
	checkSeen(t, s, "a", "b")
	checkSeen(t, s, "a") // refreshes a, so b is now the least recently seen
	checkSeen(t, s, "c") // evicts b
	if got := checkSeen(t, s, "a", "c", "b"); fmt.Sprint(got) != "[false false true]" {
		t.Fatalf("got %v, want [false false true]", got)
	}
}

func TestSeenKeys_TTLForgetsOldValues(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newStateDB(t))
	s, _ := SeenKeysFor(ctx, stateInput("wf1", "n1"), StateScopeNode, SeenOptions{TTL: time.Hour})
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	checkSeen(t, s, "a")
	now = now.Add(30 * time.Minute)
	if got := checkSeen(t, s, "a"); got[0] {
		t.Fatal("a value inside the TTL must still count as seen")
	}
	now = now.Add(61 * time.Minute)
	if got := checkSeen(t, s, "a"); !got[0] {
		t.Fatal("a value not seen for longer than the TTL must count as new")
	}
}

func TestSeenKeys_Clear(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newStateDB(t))
	s, _ := SeenKeysFor(ctx, stateInput("wf1", "n1"), StateScopeNode, SeenOptions{})
	keep, _ := SeenKeysFor(ctx, stateInput("wf1", "n2"), StateScopeNode, SeenOptions{})
	checkSeen(t, s, "a")
	checkSeen(t, keep, "a")
	if err := s.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if got := checkSeen(t, s, "a"); !got[0] {
		t.Error("cleared history must forget a")
	}
	if got := checkSeen(t, keep, "a"); got[0] {
		t.Error("Clear must not touch another node's history")
	}
}

func TestStaticData_GetSetDelete(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newStateDB(t))
	sd, err := StaticDataFor(ctx, stateInput("wf1", "n1"), StateScopeNode)
	if err != nil {
		t.Fatal(err)
	}
	var cur struct{ Last string }
	if ok, err := sd.Get(ctx, "cursor", &cur); err != nil || ok {
		t.Fatalf("Get on empty = %v, %v; want false, nil", ok, err)
	}
	if err := sd.Set(ctx, "cursor", map[string]string{"Last": "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := sd.Set(ctx, "cursor", map[string]string{"Last": "v2"}); err != nil {
		t.Fatal(err)
	}
	again, _ := StaticDataFor(ctx, stateInput("wf1", "n1"), StateScopeNode)
	if ok, err := again.Get(ctx, "cursor", &cur); err != nil || !ok || cur.Last != "v2" {
		t.Fatalf("Get = %v, %v, %+v; want true, nil, v2", ok, err, cur)
	}
	global, _ := StaticDataFor(ctx, stateInput("wf1", "n1"), StateScopeWorkflow)
	if ok, _ := global.Get(ctx, "cursor", &cur); ok {
		t.Fatal("workflow-global data must not see node data")
	}
	if err := sd.Delete(ctx, "cursor"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := sd.Get(ctx, "cursor", &cur); ok {
		t.Fatal("Get after Delete must report missing")
	}
}

func TestDeleteWorkflowState(t *testing.T) {
	db := newStateDB(t)
	ctx := vault.ContextWithDB(context.Background(), db)
	gone, _ := SeenKeysFor(ctx, stateInput("wf1", "n1"), StateScopeNode, SeenOptions{})
	kept, _ := SeenKeysFor(ctx, stateInput("wf2", "n1"), StateScopeNode, SeenOptions{})
	checkSeen(t, gone, "a")
	checkSeen(t, kept, "a")
	sd, _ := StaticDataFor(ctx, stateInput("wf1", ""), StateScopeWorkflow)
	_ = sd.Set(ctx, "k", 1)

	if err := DeleteWorkflowState(ctx, db, "wf1"); err != nil {
		t.Fatal(err)
	}
	if got := checkSeen(t, gone, "a"); !got[0] {
		t.Error("wf1 history must be gone")
	}
	if got := checkSeen(t, kept, "a"); got[0] {
		t.Error("wf2 history must be kept")
	}
	var v int
	if ok, _ := sd.Get(ctx, "k", &v); ok {
		t.Error("wf1 static data must be gone")
	}
}

func TestStateFor_NeedsDBAndIDs(t *testing.T) {
	db := newStateDB(t)
	cases := []struct {
		name  string
		ctx   context.Context
		input NodeInput
		scope StateScope
	}{
		{"no db", context.Background(), stateInput("wf1", "n1"), StateScopeNode},
		{"no workflow id", vault.ContextWithDB(context.Background(), db), stateInput("", "n1"), StateScopeWorkflow},
		{"no node id", vault.ContextWithDB(context.Background(), db), stateInput("wf1", ""), StateScopeNode},
	}
	for _, c := range cases {
		if _, err := SeenKeysFor(c.ctx, c.input, c.scope, SeenOptions{}); !errors.Is(err, ErrNoWorkflowState) {
			t.Errorf("%s: SeenKeysFor err = %v, want ErrNoWorkflowState", c.name, err)
		}
		if _, err := StaticDataFor(c.ctx, c.input, c.scope); !errors.Is(err, ErrNoWorkflowState) {
			t.Errorf("%s: StaticDataFor err = %v, want ErrNoWorkflowState", c.name, err)
		}
	}
	if _, err := SeenKeysFor(vault.ContextWithDB(context.Background(), db), stateInput("wf1", "n1"), "bogus", SeenOptions{}); err == nil {
		t.Error("unknown scope must be rejected")
	}
}

func TestEngineDeleteWorkflow_RemovesState(t *testing.T) {
	store, db := newMigratedStore(t)
	eng := newTestEngine(t, store, NewNodeTypeRegistry())
	ctx := context.Background()
	wf := &Workflow{ID: "wf-del", Name: "wf-del", ProfileID: "default"}
	if err := eng.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sd, _ := StaticDataFor(vault.ContextWithDB(ctx, db), stateInput("wf-del", "n1"), StateScopeNode)
	if err := sd.Set(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := eng.DeleteWorkflow(ctx, "wf-del"); err != nil {
		t.Fatalf("delete workflow: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM workflow_static_data WHERE workflow_id = 'wf-del'`).Scan(&n)
	if n != 0 {
		t.Fatalf("static data rows after delete = %d, want 0", n)
	}
}
