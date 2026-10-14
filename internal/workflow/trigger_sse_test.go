package workflow

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSSEParserFollowsTheSpec(t *testing.T) {
	stream := ": comment\r\n" +
		"event: order\r\ndata: {\"id\": 7}\r\nid: 1\r\n\r\n" + // CRLF
		"data: line one\rdata: line two\r\r" + // CR
		"data\n\n" + // a field with no colon: empty data
		"id: 2\nretry: 250\nevent: ignored\n\n" + // no data: nothing dispatched, but id and retry stick
		"data: 42\n\n" +
		"data: unfinished\n" // no blank line before EOF: dropped
	p := &sseParser{}
	var got []sseEvent
	if err := p.read(bufio.NewReader(strings.NewReader(stream)), func(ev sseEvent) { got = append(got, ev) }); err != nil {
		t.Fatal(err)
	}
	want := []sseEvent{
		{event: "order", data: `{"id": 7}`, id: "1"},
		{event: "message", data: "line one\nline two", id: "1"},
		{event: "message", data: "", id: "1"},
		{event: "message", data: "42", id: "2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events\n got %+v\nwant %+v", got, want)
	}
	if p.retry != 250*time.Millisecond || p.lastID != "2" {
		t.Errorf("retry %s lastID %q", p.retry, p.lastID)
	}
	if it := got[0].item(); !reflect.DeepEqual(it.JSON["data"], map[string]interface{}{"id": float64(7)}) {
		t.Errorf("JSON data not parsed: %v", it.JSON)
	}
	if it := got[1].item(); it.JSON["data"] != "line one\nline two" || it.JSON["event"] != "message" || it.JSON["id"] != "1" {
		t.Errorf("text item %v", it.JSON)
	}
}

// One run per event; after the stream drops, the reconnect carries
// Last-Event-ID and the configured headers; deactivating ends an open stream.
func TestSSETriggerReconnectsWithLastEventID(t *testing.T) {
	var mu sync.Mutex
	var lastIDs, auths []string
	var conns int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&conns, 1)
		mu.Lock()
		lastIDs = append(lastIDs, r.Header.Get("Last-Event-ID"))
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, "retry: 5\nid: a\ndata: {\"n\":1}\n\nid: b\nevent: tick\ndata: two\n\n")
			return // the server drops the stream
		}
		fmt.Fprint(w, "data: three\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // stays open until the trigger stops
	}))
	defer srv.Close()

	fired := make(chan Item, 10)
	src := &SSETriggerSource{minRetry: time.Millisecond, maxRetry: 10 * time.Millisecond}
	node := &WorkflowNode{ID: "n", Type: TriggerNodeTypeSSE, Config: map[string]interface{}{
		"url": srv.URL, "headers": `{"Authorization": "Bearer t"}`,
	}}
	stop, err := src.Activate(&Workflow{ID: "w"}, node, func(items []Item) {
		for _, it := range items {
			fired <- it
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []Item
	for len(got) < 3 {
		select {
		case it := <-fired:
			got = append(got, it)
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d events fired", len(got))
		}
	}
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("deactivate did not end the open stream")
	}

	if got[0].JSON["data"].(map[string]interface{})["n"] != float64(1) || got[0].JSON["id"] != "a" {
		t.Errorf("first item %v", got[0].JSON)
	}
	if got[1].JSON["event"] != "tick" || got[1].JSON["data"] != "two" {
		t.Errorf("second item %v", got[1].JSON)
	}
	if got[2].JSON["data"] != "three" || got[2].JSON["id"] != "b" {
		t.Errorf("third item %v", got[2].JSON)
	}
	mu.Lock()
	defer mu.Unlock()
	if lastIDs[0] != "" || lastIDs[1] != "b" {
		t.Errorf("Last-Event-ID per connection = %q", lastIDs)
	}
	if auths[0] != "Bearer t" || auths[1] != "Bearer t" {
		t.Errorf("Authorization per connection = %q", auths)
	}
}

func TestSSETriggerStopsOn204AndRetriesErrors(t *testing.T) {
	var conns int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&conns, 1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	var logs []string
	var mu sync.Mutex
	src := &SSETriggerSource{minRetry: time.Millisecond, maxRetry: 5 * time.Millisecond,
		Logf: func(f string, a ...interface{}) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }}
	stop, err := src.Activate(&Workflow{ID: "w"}, &WorkflowNode{ID: "n", Config: map[string]interface{}{"url": srv.URL}}, func([]Item) {
		t.Error("nothing should fire")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&conns) < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // a reconnect after the 204 would land here
	if n := atomic.LoadInt32(&conns); n != 3 {
		t.Fatalf("connections = %d, want 2 failures then the 204", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 3 || !strings.Contains(logs[0], "HTTP 503") || !strings.Contains(logs[2], "204") {
		t.Errorf("logs = %q", logs)
	}
}

func TestSSETriggerRejectsBadConfig(t *testing.T) {
	src := &SSETriggerSource{}
	for _, cfg := range []map[string]interface{}{
		{},
		{"url": "ftp://x/events"},
		{"url": "https://x/events", "headers": "not json"},
		{"url": "https://x/events", "headers": []interface{}{"a"}},
	} {
		if _, err := src.Activate(&Workflow{}, &WorkflowNode{Config: cfg}, func([]Item) {}); err == nil {
			t.Errorf("config %v: want an error", cfg)
		}
	}
}
