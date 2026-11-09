package workflow

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/tracesig"
)

type admitCall struct {
	workflowID, chain string
	hop               int
}

// newTraceHook serves one webhook and records what it fired with.
func newTraceHook(t *testing.T, admit TraceAdmitter) (*WebhookServer, *[]Item) {
	t.Helper()
	testhome.SetHome(t, t.TempDir()) // tracesig's key lives under HOME
	s := NewWebhookServer(":0", zerolog.Nop())
	var fired []Item
	if err := s.Register(&WebhookRegistration{
		WorkflowID: "wf-hook", Path: "hook", Method: "POST",
		TriggerFn: func(items []Item) { fired = append(fired, items...) },
	}); err != nil {
		t.Fatal(err)
	}
	if admit != nil {
		s.SetTraceAdmitter(admit)
	}
	return s, &fired
}

func post(s *WebhookServer, body, traceHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook/hook", strings.NewReader(body))
	if traceHeader != "" {
		req.Header.Set(tracesig.Header, traceHeader)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func signed(t *testing.T, chain string, hop int) string {
	t.Helper()
	key, err := tracesig.Key()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := tracesig.Sign(key, chain, hop)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// A verified signed header is recorded through the admitter, and the run
// continues the chain at the hop the admitter gave it.
func TestWebhookContinuesASignedChain(t *testing.T) {
	var calls []admitCall
	s, fired := newTraceHook(t, func(ctx context.Context, wf, chain string, hop int) (int, string, error) {
		calls = append(calls, admitCall{wf, chain, hop})
		return hop + 1, "", nil
	})
	rec := post(s, `{"x":1}`, signed(t, "chn_loop", 3))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(calls) != 1 || calls[0] != (admitCall{"wf-hook", "chn_loop", 3}) {
		t.Fatalf("admitter calls = %+v", calls)
	}
	chain, hop, ok := ParseTraceAt((*fired)[0].JSON, WebhookTraceKey)
	if !ok || chain != "chn_loop" || hop != 4 {
		t.Fatalf("run trace = %q %d %v, want chn_loop at 4", chain, hop, ok)
	}
}

// The body cannot choose the chain: the reserved key is dropped, and a
// payload's own `trace` field is left alone as ordinary data.
func TestWebhookBodyCannotChooseTheChain(t *testing.T) {
	admitted := false
	s, fired := newTraceHook(t, func(context.Context, string, string, int) (int, string, error) {
		admitted = true
		return 1, "", nil
	})
	rec := post(s, `{"monoagent_trace":{"chain_id":"chn_victim","hop":0},"trace":{"id":"apm-123"}}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	item := (*fired)[0].JSON
	if chain, hop, ok := ParseTraceAt(item, WebhookTraceKey); !ok || chain == "chn_victim" || hop != 0 || admitted {
		t.Fatalf("run chain = %q at %d (ok %v, admitted %v), want a fresh chain at 0", chain, hop, ok, admitted)
	}
	if tr, _ := item["trace"].(map[string]interface{}); tr["id"] != "apm-123" {
		t.Fatalf("payload's own trace field lost: %v", item)
	}
}

// A header that does not verify (forged, tampered, another key) starts a
// fresh chain, as no header does, without touching the ledger.
func TestWebhookIgnoresAnUnverifiedHeader(t *testing.T) {
	admitted := false
	s, fired := newTraceHook(t, func(context.Context, string, string, int) (int, string, error) {
		admitted = true
		return 1, "", nil
	})
	good := signed(t, "chn_loop", 5)
	parts := strings.Split(good, ".")
	for _, bad := range []string{
		"v1.chn_loop.0.forged",
		strings.Join([]string{parts[0], parts[1], "0", parts[3], parts[4]}, "."), // hop lowered
		`{"chain_id":"chn_loop","hop":0}`,
	} {
		if rec := post(s, `{}`, bad); rec.Code != http.StatusOK {
			t.Fatalf("%q: status %d", bad, rec.Code)
		}
	}
	for _, it := range *fired {
		if chain, hop, _ := ParseTraceAt(it.JSON, WebhookTraceKey); chain == "chn_loop" || hop != 0 {
			t.Fatalf("unverified header continued its chain: %v", it.JSON)
		}
	}
	if admitted {
		t.Fatal("an unverified header reached the ledger")
	}
}

// A refused crossing (the chain reached its hop limit) refuses the request,
// and a ledger failure fails closed rather than let the loop through.
func TestWebhookRefusesWhatTheLedgerRefuses(t *testing.T) {
	s, fired := newTraceHook(t, func(context.Context, string, string, int) (int, string, error) {
		return 0, "chain chn_loop reached hop 9, over the limit of 8", nil
	})
	rec := post(s, `{}`, signed(t, "chn_loop", 8))
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "hop 9") || len(*fired) != 0 {
		t.Fatalf("refusal: status %d body %s fired %d", rec.Code, rec.Body, len(*fired))
	}

	s, fired = newTraceHook(t, func(context.Context, string, string, int) (int, string, error) {
		return 0, "", errors.New("database is locked")
	})
	if rec := post(s, `{}`, signed(t, "chn_loop", 1)); rec.Code != http.StatusServiceUnavailable || len(*fired) != 0 {
		t.Fatalf("ledger error: status %d fired %d", rec.Code, len(*fired))
	}
}

// Without an admitter (no daemon ledger) a signed header is ignored: the
// run starts a fresh chain rather than continue one unrecorded.
func TestWebhookWithoutAdmitterStartsFresh(t *testing.T) {
	s, fired := newTraceHook(t, nil)
	if rec := post(s, `{}`, signed(t, "chn_loop", 2)); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if _, present := (*fired)[0].JSON[WebhookTraceKey]; present {
		t.Fatal("continued a chain with nothing recording it")
	}
}

// Which runs carry a chain, and where it is read from.
func TestRunTraceReadsTheRightField(t *testing.T) {
	tr := map[string]interface{}{"chain_id": "chn_a", "hop": float64(2)}
	cases := []struct {
		triggerType string
		data        map[string]interface{}
		want        bool
	}{
		{TriggerTypeOrgTool, map[string]interface{}{"trace": tr}, true},
		{TriggerNodeTypeOrg, map[string]interface{}{"trace": tr}, true},
		{TriggerNodeTypeWebhook, map[string]interface{}{WebhookTraceKey: tr}, true},
		{TriggerNodeTypeWebhook, map[string]interface{}{"trace": tr}, false}, // a payload's own field
		{"trigger.manual", map[string]interface{}{"trace": tr, WebhookTraceKey: tr}, false},
		{"trigger.schedule", map[string]interface{}{"trace": tr}, false},
	}
	for _, c := range cases {
		ctx := WithTrigger(context.Background(), c.triggerType, c.data)
		chain, hop, ok := RunTrace(ctx, nil)
		if ok != c.want || (ok && (chain != "chn_a" || hop != 2)) {
			t.Errorf("%s %v: RunTrace = %q %d %v, want ok=%v", c.triggerType, c.data, chain, hop, ok, c.want)
		}
	}
	if _, _, ok := ParseTraceAt(map[string]interface{}{"trace": map[string]interface{}{"chain_id": "chn_a", "hop": 2.5}}, "trace"); ok {
		t.Error("a fractional hop parsed")
	}
}

// Every webhook run is on a chain, even one nothing started from an org, so
// a workflow that calls its own webhook is a loop from the first request.
func TestWebhookGivesEveryRunAChain(t *testing.T) {
	s, fired := newTraceHook(t, func(context.Context, string, string, int) (int, string, error) {
		t.Fatal("a request with no header reached the ledger")
		return 0, "", nil
	})
	post(s, `{}`, "")
	post(s, `{}`, "")
	a, _, okA := ParseTraceAt((*fired)[0].JSON, WebhookTraceKey)
	b, _, okB := ParseTraceAt((*fired)[1].JSON, WebhookTraceKey)
	if !okA || !okB || a == b {
		t.Fatalf("runs got chains %q (%v) and %q (%v), want two fresh ones", a, okA, b, okB)
	}
}

// A JSON body of null is an empty item, as before, not a panic.
func TestWebhookNullBody(t *testing.T) {
	s, fired := newTraceHook(t, func(context.Context, string, string, int) (int, string, error) { return 1, "", nil })
	if rec := post(s, `null`, signed(t, "chn_loop", 0)); rec.Code != http.StatusOK || len(*fired) != 1 {
		t.Fatalf("null body: status %d fired %d", rec.Code, len(*fired))
	}
	if _, _, ok := ParseTraceAt((*fired)[0].JSON, WebhookTraceKey); !ok {
		t.Fatal("null body lost its chain")
	}
}

// #132 item 2: a run that crossed again after it started (its own org.send,
// whose output item carries the new hop) signs the deeper hop; an item on
// another chain, or at a lower hop, changes nothing.
func TestRunTraceTakesTheItemsDeeperHop(t *testing.T) {
	ctx := WithTrigger(context.Background(), TriggerNodeTypeOrg,
		map[string]interface{}{"trace": map[string]interface{}{"chain_id": "chn_a", "hop": float64(2)}})
	item := func(chain string, hop float64) map[string]interface{} {
		return map[string]interface{}{"trace": map[string]interface{}{"chain_id": chain, "hop": hop}}
	}
	for _, c := range []struct {
		item    map[string]interface{}
		wantHop int
	}{
		{nil, 2},
		{item("chn_a", 5), 5},
		{item("chn_a", 1), 2},
		{item("chn_other", 7), 2},
		{map[string]interface{}{"trace": "not a trace"}, 2},
	} {
		chain, hop, ok := RunTrace(ctx, c.item)
		if !ok || chain != "chn_a" || hop != c.wantHop {
			t.Errorf("item %v: RunTrace = %q %d %v, want chn_a at %d", c.item, chain, hop, ok, c.wantHop)
		}
	}
	// A run on no chain gets none from its item either.
	manual := WithTrigger(context.Background(), "trigger.manual", nil)
	if _, _, ok := RunTrace(manual, item("chn_a", 5)); ok {
		t.Error("a manual run took its item's trace")
	}
}

// traceProbeNode records its input item and the chain its run carries.
type traceProbeNode struct {
	item  *map[string]interface{}
	chain *string
}

func (traceProbeNode) Type() string { return "test.traceprobe" }
func (n traceProbeNode) Execute(ctx context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	*n.item = in.Items[0].JSON
	*n.chain, _, _ = RunTrace(ctx, nil)
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// A webhook run's chain trace is internal: the item the trigger hands on
// (and so a lastNode or Respond to Webhook body echoing $json) does not
// carry it, while the run still continues the chain.
func TestWebhookTraceStaysOutOfItems(t *testing.T) {
	var item map[string]interface{}
	var chain string
	reg := NewNodeTypeRegistry()
	reg.Register("test.traceprobe", func() NodeExecutor { return traceProbeNode{item: &item, chain: &chain} })
	wf := &Workflow{
		ID: "wf-trace",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-trace", Type: TriggerNodeTypeWebhook, Name: "T"},
			{ID: "p", WorkflowID: "wf-trace", Type: "test.traceprobe", Name: "Probe"},
		},
		Connections: []WorkflowConnection{{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "p", TargetHandle: "main"}},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatal(err)
	}
	exec := &WorkflowExecution{ID: "e-trace", WorkflowID: wf.ID, TriggerType: TriggerNodeTypeWebhook, TriggerNodeID: "t",
		TriggerData: map[string]interface{}{"name": "zed", WebhookTraceKey: map[string]interface{}{"chain_id": "chn_hook", "hop": float64(1)}}}
	if err := RunExecution(context.Background(), exec, wf, dag, reg, &stubStore{}, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("RunExecution: %v", err)
	}
	if _, leaked := item[WebhookTraceKey]; leaked || item["name"] != "zed" {
		t.Errorf("trigger item = %v, want the body without %s", item, WebhookTraceKey)
	}
	if chain != "chn_hook" {
		t.Errorf("run chain = %q, want chn_hook", chain)
	}
	if _, kept := exec.TriggerData[WebhookTraceKey]; !kept {
		t.Error("the execution's trigger data lost its trace")
	}
}
