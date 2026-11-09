package httpapi

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
)

// The API is a path an agent reaches: a browser form submit, a site an agent
// asked to use and a send are refused (403) and stay pending, while a
// workflow's Human-in-Loop item is decided as before (C10).
func TestHilRoutesRefuseWhatOnlyTheOperatorDecides(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	s := newTestServer(t, true)
	tok := testToken(t, s, "default")
	db, ctx := s.rt.db.DB, context.Background()
	submit, err := browserctl.CreateSubmitItem(ctx, db, browserctl.SubmitRequest{Profile: "default", URL: "https://shop.example.com/cart",
		Form: browserctl.FormSummary{Action: "https://shop.example.com/pay", Method: "post", Submitter: "#pay"}})
	if err != nil {
		t.Fatal(err)
	}
	site, err := browserctl.AskSite(ctx, db, "default", browserctl.KindOther, "https://shop.example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"send-1", `{"kind":"send"}`}, {"node-1", `{"outbound":false}`}, {"node-2", `{"outbound":false}`}} {
		if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, node_config, profile_id)
			VALUES (?, 'ex-1', 'wf-1', 'n', 'Review', 'pending', ?, 'default')`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	status := func(id string) string {
		var st string
		if err := db.QueryRow(`SELECT status FROM hil_pending WHERE id = ?`, id).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	for _, action := range []string{"approve", "reject"} {
		for _, id := range []string{submit.ID, site, "send-1"} {
			rw := doReq(t, s, "POST", "/profiles/default/hil/"+id+"/"+action, tok, nil)
			if rw.Code != http.StatusForbidden || !strings.Contains(rw.Body.String(), "hil approve "+id) || status(id) != "pending" {
				t.Errorf("%s %s: %d %s, left %s", action, id, rw.Code, rw.Body.String(), status(id))
			}
		}
	}
	if rw := doReq(t, s, "POST", "/profiles/default/hil/node-1/approve", tok, nil); rw.Code != http.StatusOK || status("node-1") != "approved" {
		t.Errorf("a workflow's item: %d %s", rw.Code, rw.Body.String())
	}
	if rw := doReq(t, s, "POST", "/profiles/default/hil/node-2/reject", tok, nil); rw.Code != http.StatusOK || status("node-2") != "rejected" {
		t.Errorf("a workflow's item: %d %s", rw.Code, rw.Body.String())
	}
}
