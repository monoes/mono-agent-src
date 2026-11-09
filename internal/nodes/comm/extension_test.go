package comm

import (
	"context"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestCommNodesHandResourceOpsToExtension(t *testing.T) {
	var mu sync.Mutex
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(reqs) }

	home := t.TempDir()
	testhome.SetHome(t, home)
	for _, nt := range []string{"comm.slack", "comm.email_send"} {
		id := strings.ReplaceAll(nt, ".", "_") + "_more"
		dir := filepath.Join(home, ".monoagent", "apipkgs", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		js := fmt.Sprintf(`{"$schema":"apipkg.v1","id":%q,"extends":%q,"title":"More","base_url":%q,
		  "credentials":[{"platform":"%s_test","methods":["bearer"],"fields":[{"key":"token","label":"Token","required":true}],
		    "inject":{"headers":{"Authorization":"Bearer {{ cred.token }}"}}}],
		  "resources":[{"value":"channel","label":"Channel"}],
		  "operations":[{"resource":"channel","operation":"archive","title":"Archive","method":"POST","path":"/channels/{{ p.channelId }}/archive",
		    "fields":[{"key":"channelId","label":"Channel","type":"text","required":true}]}]}`, id, nt, srv.URL, id)
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(js), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	apipkg.Reload()
	t.Cleanup(apipkg.Reload)

	for _, node := range []workflow.NodeExecutor{&SlackNode{}, &EmailSendNode{}} {
		before := count()
		_, err := node.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
			"resource": "channel", "operation": "archive", "channelId": "C1", "credential": map[string]interface{}{"token": "T"}})
		if err != nil {
			t.Fatalf("%s extension op: %v", node.Type(), err)
		}
		mu.Lock()
		got := reqs[before:]
		mu.Unlock()
		if len(got) != 1 || got[0] != "POST /channels/C1/archive Bearer T" {
			t.Errorf("%s package request: %v", node.Type(), got)
		}
	}

	// Native configs stay native: the native validation error, no package call.
	before := count()
	if _, err := (&SlackNode{}).Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "post_message"}); err == nil || !strings.HasPrefix(err.Error(), "comm.slack") {
		t.Errorf("native slack op: %v", err)
	}
	if _, err := (&EmailSendNode{}).Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"to": "a@b.c"}); err == nil || !strings.Contains(err.Error(), "smtp_host") {
		t.Errorf("native email_send: %v", err)
	}
	if n := count() - before; n != 0 {
		t.Errorf("native configs reached the package %d time(s)", n)
	}
}
