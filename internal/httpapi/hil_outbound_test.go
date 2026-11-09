package httpapi

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/commsync"
)

// A workflow Human-in-Loop item whose approval lets a message to a person go
// out is the user's: the API refuses it (403), marked or (queued before the
// mark) with a Communications row, and it stays pending. A plain workflow
// item is still decided.
func TestHilRoutesRefuseAnItemThatMessagesSomeone(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	s := newTestServer(t, true)
	tok := testToken(t, s, "default")
	db, ctx := s.rt.db.DB, context.Background()
	for _, row := range [][2]string{{"out-1", `{"outbound":true}`}, {"old-1", `{}`}, {"old-2", `{"readonly_fields":null}`},
		{"plain-1", `{"outbound":false}`}, {"plain-2", `{"outbound":false}`}} {
		if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, node_config, profile_id)
			VALUES (?, 'ex-1', 'wf-1', 'n', 'Review', 'pending', ?, 'default')`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := commsync.Queue(ctx, db, commsync.Item{HilRef: "old-1", Profile: "default", Platform: "email", Action: "email",
		Recipient: commsync.Recipient{Name: "Ada", Handle: "ada@example.test"}, Text: "Hi Ada"}); err != nil {
		t.Fatal(err)
	}
	status := func(id string) string {
		var st string
		if err := db.QueryRow(`SELECT status FROM hil_pending WHERE id = ?`, id).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	for _, action := range []string{"approve", "reject"} {
		for _, id := range []string{"out-1", "old-1", "old-2"} {
			rw := doReq(t, s, "POST", "/profiles/default/hil/"+id+"/"+action, tok, nil)
			if rw.Code != http.StatusForbidden || !strings.Contains(rw.Body.String(), "message to a person") || status(id) != "pending" {
				t.Errorf("%s %s: %d %s, left %s", action, id, rw.Code, rw.Body.String(), status(id))
			}
		}
	}
	if rw := doReq(t, s, "POST", "/profiles/default/hil/plain-1/approve", tok, nil); rw.Code != http.StatusOK || status("plain-1") != "approved" {
		t.Errorf("a plain workflow item: %d %s", rw.Code, rw.Body.String())
	}
	if rw := doReq(t, s, "POST", "/profiles/default/hil/plain-2/reject", tok, nil); rw.Code != http.StatusOK || status("plain-2") != "rejected" {
		t.Errorf("a plain workflow item: %d %s", rw.Code, rw.Body.String())
	}
}
