package service

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

// recorder is an httptest server that records each request's method, path
// and Authorization header and answers {"ok": true}.
type recorder struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []string
}

func newRecorder(t *testing.T, body string) *recorder {
	t.Helper()
	r := &recorder{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.reqs = append(r.reqs, req.Method+" "+req.URL.Path+" "+req.Header.Get("Authorization"))
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *recorder) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reqs...)
}

// installExtensions writes API packages into a fresh HOME's install dir.
// An installed package replaces the catalog package with the same id, so
// the fakes stand in for the real extends packages.
func installExtensions(t *testing.T, pkgs map[string]string) {
	t.Helper()
	home := t.TempDir()
	testhome.SetHome(t, home)
	for id, js := range pkgs {
		dir := filepath.Join(home, ".monoagent", "apipkgs", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(js), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	apipkg.Reload()
	t.Cleanup(apipkg.Reload)
}

// fakeExtension is an n8n-style package (resource "thing", operation
// "fetch") extending nativeType, served by baseURL.
func fakeExtension(id, nativeType, baseURL string) string {
	return fmt.Sprintf(`{"$schema":"apipkg.v1","id":%q,"extends":%q,"title":"Fake %s","base_url":%q,
  "credentials":[{"platform":"%s_test","methods":["bearer"],"fields":[{"key":"token","label":"Token","required":true}],
    "inject":{"headers":{"Authorization":"Bearer {{ cred.token }}"}}}],
  "resources":[{"value":"thing","label":"Thing"}],
  "operations":[{"resource":"thing","operation":"fetch","title":"Fetch a thing","method":"GET","path":"/things/{{ p.thingId }}",
    "fields":[{"key":"thingId","label":"Thing ID","type":"text","required":true}]}]}`, id, nativeType, id, baseURL, id)
}

// extensionCase is one native node with an extends package: its package id
// (the catalog id the real package uses), how to point the native node at
// a test server, and a native operation that must keep running natively.
type extensionCase struct {
	nodeType  string
	pkgID     string
	node      workflow.NodeExecutor
	setBase   func(url string) func()
	native    map[string]interface{}
	nativeHit string // path prefix the native op requests
}

func swap(p *string) func(string) func() {
	return func(url string) func() {
		orig := *p
		*p = url
		return func() { *p = orig }
	}
}

func extensionCases() []extensionCase {
	return []extensionCase{
		{nodeType: "service.airtable", pkgID: "airtable", node: &AirtableNode{}, setBase: swap(&airtableBaseURL),
			native:    map[string]interface{}{"operation": "list_records", "api_key": "K", "base_id": "app1", "table": "T"},
			nativeHit: "/app1/T"},
		{nodeType: "service.asana", pkgID: "asana", node: &AsanaNode{}, setBase: swap(&asanaBaseURL),
			native:    map[string]interface{}{"operation": "list_workspaces", "access_token": "K"},
			nativeHit: "/workspaces"},
		{nodeType: "service.google_sheets", pkgID: "google_sheets_more", node: &GoogleSheetsNode{}, setBase: swap(&sheetsBaseURL),
			native:    map[string]interface{}{"operation": "read_rows", "access_token": "K", "spreadsheet_id": "S1"},
			nativeHit: "/S1/values/"},
		{nodeType: "service.gmail", pkgID: "gmail_more", node: &GmailNode{}, setBase: swap(&gmailBaseURL),
			native:    map[string]interface{}{"operation": "list_labels", "access_token": "K"},
			nativeHit: "/labels"},
		{nodeType: "service.google_drive", pkgID: "google_drive_more", node: &GoogleDriveNode{}, setBase: swap(&driveBaseURL),
			native:    map[string]interface{}{"operation": "list_files", "access_token": "K"},
			nativeHit: "/files"},
		{nodeType: "service.youtube", pkgID: "youtube_more", node: &YouTubeNode{}, setBase: swap(&youtubeAPIBase),
			native:    map[string]interface{}{"operation": "get_video_stats", "access_token": "K", "video_id": "v1"},
			nativeHit: "/videos"},
	}
}

func TestNativeNodesHandResourceOpsToExtension(t *testing.T) {
	pkgSrv := newRecorder(t, `{"id":"x1","name":"from package"}`)
	cases := extensionCases()
	pkgs := map[string]string{}
	for _, c := range cases {
		pkgs[c.pkgID] = fakeExtension(c.pkgID, c.nodeType, pkgSrv.URL)
	}
	installExtensions(t, pkgs)

	for _, c := range cases {
		t.Run(c.nodeType, func(t *testing.T) {
			if !apipkg.HasExtension(c.nodeType, "thing", "fetch") {
				_, lerr := apipkg.Load()
				t.Logf("load: %v", lerr)
				t.Fatalf("fake package for %s not loaded", c.nodeType)
			}
			nativeSrv := newRecorder(t, `{"items":[{"id":"v1","statistics":{"viewCount":"1"}}]}`)
			defer c.setBase(nativeSrv.URL)()
			before := len(pkgSrv.requests())

			// (a) An n8n-style resource/operation config reaches the package.
			out, err := c.node.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
				"resource": "thing", "operation": "fetch", "thingId": "x1",
				"credential": map[string]interface{}{"token": "T"},
			})
			if err != nil {
				t.Fatalf("extension op: %v", err)
			}
			if len(out) != 1 || len(out[0].Items) != 1 || out[0].Items[0].JSON["name"] != "from package" {
				t.Fatalf("extension output: %+v", out)
			}
			got := pkgSrv.requests()[before:]
			if len(got) != 1 || got[0] != "GET /things/x1 Bearer T" {
				t.Errorf("package request: %v", got)
			}
			if n := len(nativeSrv.requests()); n != 0 {
				t.Errorf("extension op also called the native API %d time(s)", n)
			}

			// (b) A native operation still runs natively.
			before = len(pkgSrv.requests())
			if _, err := c.node.Execute(context.Background(), workflow.NodeInput{}, c.native); err != nil {
				t.Fatalf("native op %v: %v", c.native["operation"], err)
			}
			reqs := nativeSrv.requests()
			if len(reqs) == 0 || !strings.Contains(reqs[0], c.nativeHit) {
				t.Errorf("native op requests: %v, want a path with %q", reqs, c.nativeHit)
			}
			if n := len(pkgSrv.requests()) - before; n != 0 {
				t.Errorf("native op reached the package %d time(s)", n)
			}
		})
	}
}

// A package op without a resource is used only for operations the native
// switch does not know; a resource the package lacks stays native.
func TestNativeNodeUnknownOpGoesToExtension(t *testing.T) {
	pkgSrv := newRecorder(t, `{"login":"octo"}`)
	installExtensions(t, map[string]string{"github_more": fmt.Sprintf(`{"$schema":"apipkg.v1","id":"github_more",
	  "extends":"service.github","title":"GitHub more","base_url":%q,
	  "credentials":[{"platform":"github_more_test","methods":["bearer"],"fields":[{"key":"token","label":"Token","required":true}],
	    "inject":{"headers":{"Authorization":"Bearer {{ cred.token }}"}}}],
	  "operations":[{"resource":"","operation":"get_viewer","title":"Get the viewer","method":"GET","path":"/user"}]}`, pkgSrv.URL)})

	node := &GitHubNode{}
	out, err := node.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "get_viewer", "credential": map[string]interface{}{"token": "T"}})
	if err != nil || out[0].Items[0].JSON["login"] != "octo" {
		t.Fatalf("unknown op: %v %v", out, err)
	}
	if got := pkgSrv.requests(); len(got) != 1 || got[0] != "GET /user Bearer T" {
		t.Errorf("package request: %v", got)
	}

	// Still unknown everywhere: the native error, unchanged.
	if _, err := node.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "nope", "token": "T"}); err == nil || !strings.Contains(err.Error(), `unknown operation "nope"`) {
		t.Errorf("unknown op error: %v", err)
	}
}

// Old comm.* ids aliased to service.* reach the same hook: resource ops go
// to the package extending the service.* node, legacy configs stay native.
func TestAliasedNodeTypeUsesExtension(t *testing.T) {
	pkgSrv := newRecorder(t, `{"id":"x1"}`)
	installExtensions(t, map[string]string{"discord_more": fakeExtension("discord_more", "service.discord", pkgSrv.URL)})
	legacy := withLegacyDiscord(t)

	reg := workflow.NewNodeTypeRegistry()
	RegisterAll(reg)
	reg.Alias("comm.discord", "service.discord")
	f, ok := reg.Get("comm.discord")
	if !ok {
		t.Fatal("comm.discord alias not registered")
	}
	node := f()
	if _, err := node.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"resource": "thing", "operation": "fetch", "thingId": "x1", "credential": map[string]interface{}{"token": "T"}}); err != nil {
		t.Fatalf("extension op via alias: %v", err)
	}
	if got := pkgSrv.requests(); len(got) != 1 || got[0] != "GET /things/x1 Bearer T" {
		t.Errorf("package request: %v", got)
	}
	// A legacy comm.discord config: no operation means send_message.
	if _, err := node.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"bot_token": "abc", "channel_id": "C1", "content": "hi"}); err != nil {
		t.Fatalf("legacy config via alias: %v", err)
	}
	legacy.find(t, "POST /channels/C1/messages")
	if n := len(pkgSrv.requests()); n != 1 {
		t.Errorf("legacy config reached the package: %d requests", n)
	}
}
