package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
)

// The hil tools are a path an agent reaches: a browser form submit, a site an
// agent asked to use and a send are refused and stay pending, while a
// workflow's Human-in-Loop item is decided as before (C10).
func TestHilToolsRefuseWhatOnlyTheOperatorDecides(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	s := newTestServerAllowMutations(t, true)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	db, ctx := rt.db.DB, context.Background()
	submit, err := browserctl.CreateSubmitItem(ctx, db, browserctl.SubmitRequest{Profile: testProfile, URL: "https://shop.example.com/cart",
		Form: browserctl.FormSummary{Action: "https://shop.example.com/pay", Method: "post", Submitter: "#pay"}})
	if err != nil {
		t.Fatal(err)
	}
	site, err := browserctl.AskSite(ctx, db, testProfile, browserctl.KindOther, "https://shop.example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"send-1", `{"kind":"send"}`}, {"node-1", `{"outbound":false}`}, {"node-2", `{"outbound":false}`}} {
		if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, node_config, profile_id)
			VALUES (?, 'ex-1', 'wf-1', 'n', 'Review', 'pending', ?, ?)`, row[0], row[1], testProfile); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"hil_approve", "hil_reject"} {
		for _, id := range []string{submit.ID, site, "send-1"} {
			_, err := callToolDefault(ctx, s, tool, json.RawMessage(`{"id":"`+id+`"}`))
			if err == nil || !strings.Contains(err.Error(), "hil approve "+id) {
				t.Errorf("%s %s: %v", tool, id, err)
			}
			if st := hilStatus(t, db, id); st != "pending" {
				t.Errorf("%s %s left it %s", tool, id, st)
			}
		}
	}
	if _, err := callToolDefault(ctx, s, "hil_approve", json.RawMessage(`{"id":"node-1"}`)); err != nil || hilStatus(t, db, "node-1") != "approved" {
		t.Errorf("a workflow's item: %v, %s", err, hilStatus(t, db, "node-1"))
	}
	if _, err := callToolDefault(ctx, s, "hil_reject", json.RawMessage(`{"id":"node-2"}`)); err != nil || hilStatus(t, db, "node-2") != "rejected" {
		t.Errorf("a workflow's item: %v, %s", err, hilStatus(t, db, "node-2"))
	}
}

func hilStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var st string
	if err := db.QueryRow(`SELECT status FROM hil_pending WHERE id = ?`, id).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}
