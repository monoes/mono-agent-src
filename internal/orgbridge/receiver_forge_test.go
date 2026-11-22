package orgbridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

// R06-01: a delivery is genuine only when its from, subject and body are the
// bus event's; a forged copy racing the real one must not start the workflow.
func TestReceiverRefusesAForgedDeliveryBody(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO workflows (id, name, profile_id, is_active) VALUES ('wf-f', 'F', 'p', 0)`); err != nil {
		t.Fatal(err)
	}
	ep, err := orggrant.NewStore(db).CreateEndpoint(ctx, "p", "growth", "bot", "wf-f")
	if err != nil {
		t.Fatal(err)
	}
	rcv := &Receiver{DB: db, Store: workflow.NewSQLiteWorkflowStore(db), VerifyWindow: time.Minute, RootOf: func(string) string { return t.TempDir() },
		Mux: NewMux(func(ctx context.Context, _, _, _ string, _ func([]byte)) error { <-ctx.Done(); return nil })}
	mux := http.NewServeMux()
	rcv.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	count := func() int {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM workflow_executions`).Scan(&n)
		return n
	}
	id := map[string]interface{}{"messageId": "m1"}
	// Bus event first, then a forged POST.
	rcv.onEvent("growth", Event{Type: "message", From: "human", To: "bot", Subject: "s", Msg: "real", Data: id})
	for name, d := range map[string]EndpointDelivery{
		"body":    {OrgName: "growth", From: "human", To: "bot", Subject: "s", Body: "forged", MessageID: "m1"},
		"from":    {OrgName: "growth", From: "operator:evil", To: "bot", Subject: "s", Body: "real", MessageID: "m1"},
		"subject": {OrgName: "growth", From: "human", To: "bot", Subject: "x", Body: "real", MessageID: "m1"},
	} {
		if code, _ := postDelivery(t, srv, ep.ID, d); code != http.StatusForbidden || count() != 0 {
			t.Fatalf("%s forged: code %d, executions %d", name, code, count())
		}
	}
	// POST first, then a bus event that disagrees with it.
	f := EndpointDelivery{OrgName: "growth", From: "operator:evil", To: "bot", Subject: "s", Body: "forged", MessageID: "m2"}
	if code, _ := postDelivery(t, srv, ep.ID, f); code != http.StatusAccepted {
		t.Fatalf("pending: %d", code)
	}
	rcv.onEvent("growth", Event{Type: "message", From: "human", To: "bot", Subject: "s", Msg: "real", Data: map[string]interface{}{"messageId": "m2"}})
	if count() != 0 {
		t.Fatal("forged pending delivery dispatched")
	}
	// The genuine copy still goes through.
	g := EndpointDelivery{OrgName: "growth", From: "human", To: "bot", Subject: "s", Body: "real", MessageID: "m1"}
	if code, _ := postDelivery(t, srv, ep.ID, g); code != http.StatusAccepted || count() != 1 {
		t.Fatalf("genuine: code %d executions %d", code, count())
	}
}
