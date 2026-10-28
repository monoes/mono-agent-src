package operator

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/testdb"
)

// hilItems stores one item of each kind in profile p1 and returns their ids:
// a browser form submit, a site an agent asked to use, a send and a workflow's
// Human-in-Loop node.
func hilItems(t *testing.T, db *sql.DB) (submit, site, send, node string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	it, err := browserctl.CreateSubmitItem(ctx, db, browserctl.SubmitRequest{Profile: "p1", URL: "https://shop.example.com/cart",
		Form: browserctl.FormSummary{Action: "https://shop.example.com/pay", Method: "post", Submitter: "#pay"}})
	if err != nil {
		t.Fatal(err)
	}
	site, err = browserctl.AskSite(ctx, db, "p1", browserctl.KindOther, "https://shop.example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"send-1", `{"kind":"send"}`}, {"node-1", `{"outbound":false}`}} {
		if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, node_config, profile_id)
			VALUES (?, 'ex-1', 'wf-1', 'n', 'Review', 'pending', ?, 'p1')`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	return it.ID, site, "send-1", "node-1"
}

// Only the operator decides a browser submit, a site or a send; a workflow's
// item, and one that isn't there, are the caller's to handle.
func TestItemRefusal(t *testing.T) {
	db := testdb.Open(t).DB
	submit, site, send, node := hilItems(t, db)
	ctx := context.Background()
	for _, id := range []string{submit, site, send} {
		err := ItemRefusal(ctx, db, "p1", id)
		var only *ItemError
		if !errors.As(err, &only) || !strings.Contains(err.Error(), "`monoagentcli --profile p1 hil approve "+id+"`") ||
			!strings.Contains(err.Error(), "Human in Loop page") {
			t.Errorf("item %s: %v", id, err)
		}
	}
	for _, id := range []string{node, "missing"} {
		if err := ItemRefusal(ctx, db, "p1", id); err != nil {
			t.Errorf("item %s: %v", id, err)
		}
	}
	if err := ItemRefusal(ctx, db, "p2", submit); err != nil {
		t.Errorf("another profile's item is not there: %v", err)
	}
}

// A browser's pairing card is the user's to decide: neither MCP nor the HTTP API may
// approve or reject (and so dismiss) it.
func TestItemRefusalCoversAPairingCard(t *testing.T) {
	db := testdb.Open(t).DB
	ctx := context.Background()
	claim := extension.PendingClaim{ID: "c1c1c1c1", Label: "Edge", Origin: "chrome-extension://aaaa", ExpiresAt: time.Now().Add(time.Minute)}
	if err := extension.OpenPairCard(ctx, db, "p1", claim); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM hil_pending WHERE execution_id = ?`, "extpair:"+claim.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var only *ItemError
	err := ItemRefusal(ctx, db, "p1", id)
	if !errors.As(err, &only) {
		t.Fatalf("a pairing card was left to an agent: %v", err)
	}
	if !strings.Contains(err.Error(), "pair") || strings.Contains(err.Error(), "message to a person") {
		t.Fatalf("the refusal gives the wrong reason: %v", err)
	}
}
