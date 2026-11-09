package mcp

import (
	"context"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/commsync"
)

// A workflow Human-in-Loop item whose approval lets a message to a person go
// out is the user's: the hil tools refuse it, marked or (queued before the
// mark) with a Communications row, and it stays pending. A plain workflow
// item is still decided.
func TestHilToolsRefuseAnItemThatMessagesSomeone(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	s := newTestServerAllowMutations(t, true)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	db, ctx := rt.db.DB, context.Background()
	for _, row := range [][2]string{{"out-1", `{"outbound":true}`}, {"old-1", `{}`}, {"old-2", `{"readonly_fields":null}`},
		{"plain-1", `{"outbound":false}`}, {"plain-2", `{"outbound":false}`}} {
		if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, node_config, profile_id)
			VALUES (?, 'ex-1', 'wf-1', 'n', 'Review', 'pending', ?, ?)`, row[0], row[1], testProfile); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := commsync.Queue(ctx, db, commsync.Item{HilRef: "old-1", Profile: testProfile, Platform: "email", Action: "email",
		Recipient: commsync.Recipient{Name: "Ada", Handle: "ada@example.test"}, Text: "Hi Ada"}); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"hil_approve", "hil_reject"} {
		for _, id := range []string{"out-1", "old-1", "old-2"} {
			_, err := callToolDefault(ctx, s, tool, json.RawMessage(`{"id":"`+id+`"}`))
			if err == nil || !strings.Contains(err.Error(), "message to a person") || !strings.Contains(err.Error(), "hil approve "+id) {
				t.Errorf("%s %s: %v", tool, id, err)
			}
			if st := hilStatus(t, db, id); st != "pending" {
				t.Errorf("%s %s left it %s", tool, id, st)
			}
		}
	}
	if _, err := callToolDefault(ctx, s, "hil_approve", json.RawMessage(`{"id":"plain-1"}`)); err != nil || hilStatus(t, db, "plain-1") != "approved" {
		t.Errorf("a plain workflow item: %v, %s", err, hilStatus(t, db, "plain-1"))
	}
	if _, err := callToolDefault(ctx, s, "hil_reject", json.RawMessage(`{"id":"plain-2"}`)); err != nil || hilStatus(t, db, "plain-2") != "rejected" {
		t.Errorf("a plain workflow item: %v, %s", err, hilStatus(t, db, "plain-2"))
	}
}
