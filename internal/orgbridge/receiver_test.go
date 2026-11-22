package orgbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

func postDelivery(t *testing.T, srv *httptest.Server, id string, d EndpointDelivery) (int, map[string]interface{}) {
	t.Helper()
	b, _ := json.Marshal(d)
	resp, err := http.Post(srv.URL+"/org-endpoint/"+id, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestReceiverVerifiesDispatchesAndReplies(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO workflows (id, name, profile_id, is_active) VALUES ('wf-pub', 'Publish', 'p', 0)`); err != nil {
		t.Fatal(err)
	}
	ep, err := orggrant.NewStore(db).CreateEndpoint(ctx, "p", "growth", "bot", "wf-pub")
	if err != nil {
		t.Fatal(err)
	}
	store := workflow.NewSQLiteWorkflowStore(db)
	hqRoot := t.TempDir()
	if _, err := orgdesign.Save(hqRoot, &orgdesign.Doc{Name: "hq", Status: "stopped", Schedule: json.RawMessage("null"),
		Roles: []orgdesign.Role{{ID: "ceo", Title: "CEO", Type: "boss", Responsibilities: []string{"x"}}}}); err != nil {
		t.Fatal(err)
	}
	rcv := &Receiver{DB: db, Store: store, VerifyWindow: 50 * time.Millisecond, RootOf: func(string) string { return hqRoot },
		Mux: NewMux(func(ctx context.Context, _, _, _ string, _ func([]byte)) error { <-ctx.Done(); return nil })}
	mux := http.NewServeMux()
	rcv.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := EndpointDelivery{OrgName: "growth", Run: "run-1", From: "hq:ceo", To: "growth:bot", Subject: "post this", Body: "[trace chn_p hop=2]\nhello world", MessageID: "msg-1"}

	if code, _ := postDelivery(t, srv, orggrant.NewEndpointID(), d); code != http.StatusNotFound {
		t.Fatalf("unknown endpoint id: %d", code)
	}
	wrongOrg := d
	wrongOrg.OrgName = "other"
	if code, _ := postDelivery(t, srv, ep.ID, wrongOrg); code != http.StatusNotFound {
		t.Fatalf("org mismatch: %d", code)
	}
	noID := d
	noID.MessageID = ""
	if code, _ := postDelivery(t, srv, ep.ID, noID); code != http.StatusBadRequest {
		t.Fatalf("missing messageId: %d", code)
	}

	code, out := postDelivery(t, srv, ep.ID, d)
	if code != http.StatusAccepted || out["verified"] != false {
		t.Fatalf("first delivery: %d %v", code, out)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM workflow_executions`).Scan(&n)
	if n != 0 {
		t.Fatal("workflow started before the bus confirmed the message")
	}

	// The org's bus shows the message: the run starts, as an unowned
	// org_message execution carrying the next hop.
	rcv.onEvent("growth", Event{Type: "message", From: "hq:ceo", To: "bot", Subject: d.Subject, Msg: d.Body, Data: map[string]interface{}{"messageId": "msg-1"}})
	var execID, trigger, status, data string
	if err := db.QueryRow(`SELECT id, trigger_type, status, trigger_data FROM workflow_executions`).Scan(&execID, &trigger, &status, &data); err != nil {
		t.Fatalf("no execution: %v", err)
	}
	if trigger != workflow.TriggerTypeOrgMessage || status != "QUEUED" || !strings.Contains(data, `"hop":3`) || !strings.Contains(data, `"text":"hello world"`) {
		t.Fatalf("execution = %s %s %s", trigger, status, data)
	}
	if code, out := postDelivery(t, srv, ep.ID, d); code != http.StatusOK || out["duplicate"] != true {
		t.Fatalf("retry of a dispatched message: %d %v", code, out)
	}

	// A direct POST that never went through the org is refused after the
	// window (C-4).
	forged := d
	forged.MessageID = "msg-forged"
	if code, _ := postDelivery(t, srv, ep.ID, forged); code != http.StatusAccepted {
		t.Fatalf("forged: %d", code)
	}
	time.Sleep(80 * time.Millisecond)

	var sent monomind.InboxMessage
	var sentOrg string
	inboxFunc = func(_ context.Context, _, name string, msg monomind.InboxMessage) (*monomind.InboxReceipt, error) {
		sent, sentOrg = msg, name
		return &monomind.InboxReceipt{V: 1, Org: name, Delivery: "live"}, nil
	}
	t.Cleanup(func() { inboxFunc = monomind.OrgInbox })

	// Finish the run with an output, then sweep: forged refused, reply sent.
	now := time.Now().UTC()
	if _, err := db.Exec(`UPDATE workflow_executions SET status = 'SUCCESS' WHERE id = ?`, execID); err != nil {
		t.Fatal(err)
	}
	en := &workflow.WorkflowExecutionNode{ExecutionID: execID, NodeID: "n1", NodeName: "publish", Status: "SUCCESS",
		OutputItems: []workflow.Item{{JSON: map[string]interface{}{"url": "https://example.test/post/1", "api_key": "secret-value"}}},
		StartedAt:   &now, FinishedAt: &now}
	if err := store.CreateExecutionNode(ctx, en); err != nil {
		t.Fatal(err)
	}
	rcv.Sweep(ctx)

	var refused int
	_ = db.QueryRow(`SELECT COUNT(*) FROM org_bridge_calls WHERE status = 'refused_grant' AND endpoint_id = ?`, ep.ID).Scan(&refused)
	if refused != 1 {
		t.Fatalf("forged delivery refusals = %d", refused)
	}
	if sentOrg != "hq" || sent.To != "ceo" || sent.From != "growth:bot" || sent.Subject != "re: post this" {
		t.Fatalf("reply = org %q %+v", sentOrg, sent)
	}
	if !strings.HasPrefix(sent.Body, "[trace chn_p hop=4]\n") || !strings.Contains(sent.Body, "example.test/post/1") || strings.Contains(sent.Body, "secret-value") {
		t.Fatalf("reply body = %q", sent.Body)
	}
	sent = monomind.InboxMessage{}
	rcv.Sweep(ctx)
	if sent.From != "" {
		t.Fatal("replied twice")
	}
}

// An automation role whose workflow itself sends org messages must still
// reply to its sender: the workflow's own org.send crossing is not the
// reply (found in a no-model ping/pong run, where no role ever got a reply
// from a workflow that used org.send).
func TestReceiverRepliesWhenWorkflowAlsoSends(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO workflows (id, name, profile_id, is_active) VALUES ('wf-ping', 'Ping', 'p', 0)`); err != nil {
		t.Fatal(err)
	}
	ep, err := orggrant.NewStore(db).CreateEndpoint(ctx, "p", "h3", "ping-bot", "wf-ping")
	if err != nil {
		t.Fatal(err)
	}
	store := workflow.NewSQLiteWorkflowStore(db)
	rcv := &Receiver{DB: db, Store: store, RootOf: func(string) string { return t.TempDir() },
		Mux: NewMux(func(ctx context.Context, _, _, _ string, _ func([]byte)) error { <-ctx.Done(); return nil })}
	row, err := orggrant.NewStore(db).LookupEndpoint(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	rcv.dispatch(ctx, *row, EndpointDelivery{OrgName: "h3", Run: "run-1", From: "operator:lead", To: "ping-bot", Subject: "start", Body: "start", MessageID: "msg-1"})
	var execID string
	if err := db.QueryRow(`SELECT id FROM workflow_executions`).Scan(&execID); err != nil {
		t.Fatalf("no execution: %v", err)
	}

	var sent []monomind.InboxMessage
	inboxFunc = func(_ context.Context, _, name string, msg monomind.InboxMessage) (*monomind.InboxReceipt, error) {
		sent = append(sent, msg)
		return &monomind.InboxReceipt{V: 1, Org: name, Delivery: "live"}, nil
	}
	t.Cleanup(func() { inboxFunc = monomind.OrgInbox })

	// The run's org.send node messages pong-bot, then the run finishes.
	if _, err := Send(ctx, NewLedger(db), SendRequest{ProfileID: "p", Root: t.TempDir(), Org: "h3", To: "pong-bot", From: "h3:ping-bot",
		Subject: "ping", Body: "ping", Trace: Trace{ChainID: "chn_x", Hop: 1}, WorkflowID: "wf-ping", ExecutionID: execID}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_executions SET status = 'SUCCESS' WHERE id = ?`, execID); err != nil {
		t.Fatal(err)
	}
	sent = nil
	rcv.Sweep(ctx)
	if len(sent) != 1 || sent[0].To != "lead" || sent[0].Subject != "re: start" {
		t.Fatalf("replies after sweep = %+v, want one reply to lead", sent)
	}
	sent = nil
	rcv.Sweep(ctx)
	if len(sent) != 0 {
		t.Fatalf("replied twice: %+v", sent)
	}
}

func TestReceiverAnswersOrgAsk(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWaitingExecution(t, db, "exec-ask")
	ep, err := orggrant.NewStore(db).CreateEndpoint(ctx, "p", "growth", "bot", "wf")
	if err != nil {
		t.Fatal(err)
	}
	ask := Ask{ID: NewAskID(), ProfileID: "p", OrgName: "growth", EndpointRoleID: "bot", ExecutionID: "exec-ask", NodeID: "n", DeadlineAt: time.Now().Add(time.Hour)}
	if err := NewAskStore(db).Create(ctx, ask); err != nil {
		t.Fatal(err)
	}
	var resumed string
	rcv := &Receiver{DB: db, Store: workflow.NewSQLiteWorkflowStore(db), Resume: func(id string) error { resumed = id; return nil }}
	rcv.dispatch(ctx, *ep, EndpointDelivery{OrgName: "growth", From: "lead", To: "bot", Subject: "re: ask:" + ask.ID, Body: "42", MessageID: "m"})
	got, _ := NewAskStore(db).Get(ctx, ask.ID)
	if got.Status != AskReplied || resumed != "exec-ask" {
		t.Fatalf("ask = %+v resumed=%q", got, resumed)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM workflow_executions WHERE trigger_type = ?`, workflow.TriggerTypeOrgMessage).Scan(&n)
	if n != 0 {
		t.Fatal("an ask reply started a new automation run")
	}
}

// One failed `monomind org inbox` delivery must not drop an automation
// role's reply. Ledger.Admit records the endpoint_reply row inside Send
// BEFORE the delivery is attempted and a failed delivery only flips that
// row to 'error', so treating the bare row as the has-replied marker made
// a single transient failure permanent and left the sender waiting forever.
func TestReceiverRetriesReplyAfterDeliveryFailure(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO workflows (id, name, profile_id, is_active) VALUES ('wf-retry', 'Retry', 'p', 0)`); err != nil {
		t.Fatal(err)
	}
	ep, err := orggrant.NewStore(db).CreateEndpoint(ctx, "p", "growth", "bot", "wf-retry")
	if err != nil {
		t.Fatal(err)
	}
	rcv := &Receiver{DB: db, Store: workflow.NewSQLiteWorkflowStore(db), RootOf: func(string) string { return t.TempDir() },
		Mux: NewMux(func(ctx context.Context, _, _, _ string, _ func([]byte)) error { <-ctx.Done(); return nil })}
	rcv.dispatch(ctx, *ep, EndpointDelivery{OrgName: "growth", Run: "run-1", From: "operator:lead", To: "bot", Subject: "go", Body: "go", MessageID: "msg-1"})
	var execID string
	if err := db.QueryRow(`SELECT id FROM workflow_executions`).Scan(&execID); err != nil {
		t.Fatalf("no execution: %v", err)
	}
	if _, err := db.Exec(`UPDATE workflow_executions SET status = 'SUCCESS' WHERE id = ?`, execID); err != nil {
		t.Fatal(err)
	}

	var sent []monomind.InboxMessage
	down := true
	inboxFunc = func(_ context.Context, _, name string, msg monomind.InboxMessage) (*monomind.InboxReceipt, error) {
		if down {
			return nil, errors.New("monomind org inbox: connection refused")
		}
		sent = append(sent, msg)
		return &monomind.InboxReceipt{V: 1, Org: name, Delivery: "live"}, nil
	}
	t.Cleanup(func() { inboxFunc = monomind.OrgInbox })

	rcv.Sweep(ctx)
	if len(sent) != 0 {
		t.Fatalf("a failed delivery counted as a reply: %+v", sent)
	}
	down = false
	rcv.Sweep(ctx)
	if len(sent) != 1 || sent[0].To != "lead" || sent[0].Subject != "re: go" {
		t.Fatalf("replies once the org came back = %+v, want one reply to lead", sent)
	}
	rcv.Sweep(ctx)
	if len(sent) != 1 {
		t.Fatalf("replied again after a delivered reply: %+v", sent)
	}
}

// A permanently unreachable org must not be retried forever: the ledger is
// append-only and every attempt inserts a row.
func TestReceiverGivesUpReplyingAfterMaxAttempts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO workflows (id, name, profile_id, is_active) VALUES ('wf-down', 'Down', 'p', 0)`); err != nil {
		t.Fatal(err)
	}
	ep, err := orggrant.NewStore(db).CreateEndpoint(ctx, "p", "growth", "bot", "wf-down")
	if err != nil {
		t.Fatal(err)
	}
	rcv := &Receiver{DB: db, Store: workflow.NewSQLiteWorkflowStore(db), RootOf: func(string) string { return t.TempDir() },
		Mux: NewMux(func(ctx context.Context, _, _, _ string, _ func([]byte)) error { <-ctx.Done(); return nil })}
	rcv.dispatch(ctx, *ep, EndpointDelivery{OrgName: "growth", Run: "run-1", From: "operator:lead", To: "bot", Subject: "go", Body: "go", MessageID: "msg-1"})
	var execID string
	if err := db.QueryRow(`SELECT id FROM workflow_executions`).Scan(&execID); err != nil {
		t.Fatalf("no execution: %v", err)
	}
	if _, err := db.Exec(`UPDATE workflow_executions SET status = 'SUCCESS' WHERE id = ?`, execID); err != nil {
		t.Fatal(err)
	}
	var attempts int
	inboxFunc = func(context.Context, string, string, monomind.InboxMessage) (*monomind.InboxReceipt, error) {
		attempts++
		return nil, errors.New("monomind org inbox: connection refused")
	}
	t.Cleanup(func() { inboxFunc = monomind.OrgInbox })

	for i := 0; i < maxReplyAttempts+3; i++ {
		rcv.Sweep(ctx)
	}
	if attempts != maxReplyAttempts {
		t.Fatalf("attempted the reply %d times, want %d", attempts, maxReplyAttempts)
	}
}

// The daemon must already be watching an org that has a live automation
// endpoint. Mux.Subscribe does not replay, and monomind emits the
// confirming bus event right after its 202, so subscribing lazily on the
// first POST loses that event: the first delivery to an org after each
// daemon start sat pending for the whole VerifyWindow and Sweep refused it
// as refused_grant ("direct POST?").
func TestReceiverSubscribesEndpointOrgsAtStartup(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := orggrant.NewStore(db).CreateEndpoint(ctx, "p", "growth", "bot", "wf"); err != nil {
		t.Fatal(err)
	}
	followed := make(chan string, 4)
	mux := NewMux(func(ctx context.Context, _, org, _ string, _ func([]byte)) error {
		followed <- org
		<-ctx.Done()
		return nil
	})
	root := t.TempDir()
	rcv := &Receiver{DB: db, Store: workflow.NewSQLiteWorkflowStore(db), Mux: mux,
		RootOf: func(string) string { return root }}
	go rcv.Run(ctx)

	select {
	case org := <-followed:
		if org != "growth" {
			t.Fatalf("followed %q, want growth", org)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no tail for the org of a live automation endpoint at startup — the first delivery's " +
			"confirming bus event is missed and Sweep refuses it as refused_grant")
	}
}
