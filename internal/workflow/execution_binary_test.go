package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"sync"
	"testing"

	"github.com/rs/zerolog"
)

// persistedTriggerData is what a webhook run's execution row holds: the
// trigger item's JSON after a JSON round trip.
func persistedTriggerData(t *testing.T, item Item) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(item.JSON)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A multipart webhook file reaches the run's nodes on Item.Binary, through
// the persisted trigger data and a pause/resume, and its JSON form stays.
func TestRunExecution_WebhookFileOnItemBinary(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", "report")
	fw, _ := mw.CreateFormFile("doc", "r.bin")
	_, _ = fw.Write([]byte{0, 1, 2, 'x'})
	_ = mw.Close()
	s, fired := itemHook(t, &WebhookRegistration{})
	_ = firedJSON(t, send(s, http.MethodPost, "/webhook/hook", mw.FormDataContentType(), buf.Bytes()), fired)
	trigger := persistedTriggerData(t, (*fired)[0])

	var mu sync.Mutex
	var got []Item
	gateOpen := false
	reg := NewNodeTypeRegistry()
	reg.Register("test.gate", func() NodeExecutor { return gateNode{&gateOpen} })
	reg.Register("test.capture", func() NodeExecutor { return captureNode{mu: &mu, items: &got} })
	wf := &Workflow{ID: "wf", Nodes: []WorkflowNode{
		{ID: "t", Type: "trigger.webhook", Name: "T"},
		{ID: "g", Type: "test.gate", Name: "Gate"},
		{ID: "c", Type: "test.capture", Name: "C"},
	}, Connections: []WorkflowConnection{
		{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "g", TargetHandle: "main"},
		{SourceNodeID: "g", SourceHandle: "main", TargetNodeID: "c", TargetHandle: "main"},
	}}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatal(err)
	}
	store := &stubStore{}
	exec := &WorkflowExecution{ID: "e", WorkflowID: "wf", TriggerData: trigger}
	if err := RunExecution(context.Background(), exec, wf, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop()); !errors.Is(err, ErrExecutionPaused) {
		t.Fatalf("first run: %v, want paused", err)
	}
	gateOpen = true
	exec.ResumeState = store.waitingState
	if err := RunExecution(context.Background(), exec, wf, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("capture got %d items, want 1", len(got))
	}
	if b := got[0].Binary["doc"]; !bytes.Equal(b, []byte{0, 1, 2, 'x'}) {
		t.Errorf("Item.Binary[doc] = %v", b)
	}
	bin, _ := got[0].JSON["binary"].(map[string]interface{})
	if doc, _ := bin["doc"].(map[string]interface{}); doc["file_name"] != "r.bin" || doc["data"] == nil {
		t.Errorf("JSON binary.doc = %v, want the JSON form kept", bin["doc"])
	}
}

// Trigger data whose "binary" is not the webhook's file map (a body field
// of that name, a manual run's data) leaves Item.Binary empty.
func TestBuildTriggerItems_BinaryOnlyFromWebhookFiles(t *testing.T) {
	for _, v := range []interface{}{
		"plain",
		map[string]interface{}{"doc": "x"},
		map[string]interface{}{"doc": map[string]interface{}{"data": "not base64!"}},
		map[string]interface{}{"doc": map[string]interface{}{"data": "eA==", "file_size": 7.0}},
	} {
		items := buildTriggerItems(map[string]interface{}{"binary": v})
		if len(items) != 1 || items[0].Binary != nil {
			t.Errorf("binary=%v: Item.Binary = %v, want none", v, items[0].Binary)
		}
		if items[0].JSON["binary"] == nil {
			t.Errorf("binary=%v: JSON form dropped", v)
		}
	}
}
