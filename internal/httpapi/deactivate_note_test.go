package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Deactivating under an unfinished change succeeds in the database only; the response says the file
// was left, instead of reporting plain success.
func TestDeactivateRouteReportsARowsOnlySwitch(t *testing.T) {
	s := newTestServer(t, true)
	seedWorkflow(t, s, "wf-note", "default")
	path := "/profiles/default/workflows/wf-note/deactivate"
	tok := testToken(t, s, "default")

	rw := doReq(t, s, "POST", path, tok, nil)
	var plain map[string]any
	if rw.Code != http.StatusOK || json.Unmarshal(rw.Body.Bytes(), &plain) != nil || plain["note"] != nil {
		t.Fatalf("an ordinary deactivation: %d %s", rw.Code, rw.Body)
	}

	seedWorkflow(t, s, "wf-stuck", "default")
	if _, err := s.rt.db.DB.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('wf-stuck', 'save', 'unresolved', 0, ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rw = doReq(t, s, "POST", "/profiles/default/workflows/wf-stuck/deactivate", tok, nil)
	var out map[string]any
	if rw.Code != http.StatusOK || json.Unmarshal(rw.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", rw.Code, rw.Body)
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "the file was left as it is") {
		t.Fatalf("no note in %s", rw.Body)
	}
}
