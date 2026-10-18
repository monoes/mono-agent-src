package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/testdb"
)

// created_at holds a time.Time's text (this store's own writes) beside
// RFC3339 (the chat tools, the actions migration). Workflows and their nodes
// are ordered by the instant, not by the text, in which every RFC3339 value
// of a day sorts after every time.Time value of it.
func TestWorkflowsAndNodesOrderByInstantAcrossStoredFormats(t *testing.T) {
	ctx := context.Background()
	db := testdb.Open(t).DB
	s := NewSQLiteWorkflowStore(db)
	nine := "2026-10-19T09:00:00Z"                        // RFC3339
	ten := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC) // stored as "2026-10-19 10:00:00 +0000 UTC"
	for _, w := range []struct {
		id string
		at any
	}{{"older", nine}, {"newer", ten}} {
		if _, err := db.Exec(`INSERT INTO workflows (id, name, profile_id, created_at, updated_at) VALUES (?, ?, 'p1', ?, ?)`, w.id, w.id, w.at, w.at); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(ws []Workflow) (out []string) {
		for _, w := range ws {
			out = append(out, w.ID)
		}
		return out
	}
	list, err := s.ListWorkflows(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(list); len(got) != 2 || got[0] != "newer" {
		t.Errorf("ListWorkflows = %v, want newer first", got)
	}
	all, err := s.ListAllProfilesWorkflows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(all); len(got) != 2 || got[0] != "newer" {
		t.Errorf("ListAllProfilesWorkflows = %v, want newer first", got)
	}

	for _, n := range []struct {
		id string
		at any
	}{{"second", ten}, {"first", nine}} {
		if _, err := db.Exec(`INSERT INTO workflow_nodes (id, workflow_id, node_type, name, created_at, updated_at) VALUES (?, 'older', 'test.echo', ?, ?, ?)`, n.id, n.id, n.at, n.at); err != nil {
			t.Fatal(err)
		}
	}
	nodes, err := s.loadWorkflowNodes(ctx, "older")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].ID != "first" || nodes[1].ID != "second" {
		t.Errorf("nodes = %v, want first, second", nodes)
	}
}
