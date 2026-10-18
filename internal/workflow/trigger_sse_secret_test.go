package workflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
)

// A trigger.sse of a workflow the user hasn't approved sends no secret to
// its URL, which the chat or an agent may have chosen: its @secret header is
// refused, naming the reference. Approved, it connects with the value.
func TestAnUnapprovedSSETriggerSendsNoSecret(t *testing.T) {
	keyring.MockInit()
	db, store := approvalsTestDB(t)
	ctx := context.Background()
	if _, err := secrets.Add(ctx, db.DB, "default", "secret", "feed", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	wf := approvalsTestWorkflow("wf-sse", 1)
	wf.Nodes[0] = WorkflowNode{ID: "wf-sse-t", WorkflowID: "wf-sse", Type: TriggerNodeTypeSSE, Name: "Stream",
		Config: map[string]interface{}{"url": srv.URL, "headers": `{"Authorization": "@secret:feed"}`}}
	createSQLWorkflow(t, store, wf)
	src := NewSSETriggerSource(db.DB, nil)

	_, err := src.Activate(wf, &wf.Nodes[0], func([]Item) {})
	if err == nil || !strings.Contains(err.Error(), "@secret:feed is not read from the vault") || strings.Contains(err.Error(), "vsecret-777") {
		t.Fatalf("an unapproved stream: %v", err)
	}
	if err := store.ApproveDefinition(ctx, wf.ID); err != nil {
		t.Fatal(err)
	}
	stop, err := src.Activate(wf, &wf.Nodes[0], func([]Item) {})
	if err != nil {
		t.Fatalf("the approved stream: %v", err)
	}
	defer stop()
	for i := 0; i < 200; i++ {
		mu.Lock()
		n := len(auths)
		mu.Unlock()
		if n > 0 {
			break
		}
		<-time.After(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) == 0 || auths[0] != "vsecret-777" {
		t.Fatalf("the approved stream sent %q", auths)
	}
}
