package mcpnode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeServerEnv) == "1" {
		runFakeStdio()
		os.Exit(0)
	}
	keyring.MockInit()
	testhome.Main(m)
}

// stdioConfig returns a config that spawns this test binary as the fake
// stdio server.
func stdioConfig(extra map[string]interface{}) map[string]interface{} {
	cfg := map[string]interface{}{
		"transport": "stdio",
		"command":   os.Args[0],
		"args":      []interface{}{"-test.run=^$"},
		"env":       map[string]interface{}{fakeServerEnv: "1"},
		"timeout":   float64(10),
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func stubSecrets(t *testing.T, vals map[string]string) {
	t.Helper()
	prev := resolveSecret
	resolveSecret = func(_ context.Context, ref string) (string, error) {
		name := strings.TrimPrefix(ref, "@secret:")
		if v, ok := vals[name]; ok {
			return v, nil
		}
		return ref, fmt.Errorf("entry %q not found", name)
	}
	t.Cleanup(func() { resolveSecret = prev })
}

func run(t *testing.T, ctx context.Context, items []map[string]interface{}, cfg map[string]interface{}) ([]map[string]interface{}, error) {
	t.Helper()
	in := workflow.NodeInput{}
	for _, it := range items {
		in.Items = append(in.Items, workflow.NewItem(it))
	}
	outs, err := (&CallNode{}).Execute(ctx, in, cfg)
	if err != nil {
		return nil, err
	}
	if len(outs) != 1 || outs[0].Handle != "main" {
		t.Fatalf("outputs = %+v", outs)
	}
	var got []map[string]interface{}
	for _, it := range outs[0].Items {
		got = append(got, it.JSON)
	}
	return got, nil
}

func toolNames(items []map[string]interface{}) []string {
	var names []string
	for _, it := range items {
		names = append(names, fmt.Sprint(it["tool"]))
	}
	return names
}

func TestStdioListTools(t *testing.T) {
	got, err := run(t, context.Background(), nil, stdioConfig(map[string]interface{}{"operation": "list_tools"}))
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(toolNames(got), ","); names != "echo,fail,structured" {
		t.Fatalf("tools = %s (pagination followed?)", names)
	}
	if got[0]["description"] != "Echo the arguments" || got[0]["input_schema"] == nil {
		t.Errorf("echo item = %+v", got[0])
	}
}

func TestStdioCallToolPerItem(t *testing.T) {
	cfg := stdioConfig(map[string]interface{}{
		"tool":      "echo",
		"arguments": map[string]interface{}{"q": "{{ $json.q }}", "fixed": float64(1)},
	})
	got, err := run(t, context.Background(), []map[string]interface{}{{"q": "first"}, {"q": "second"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("items = %+v", got)
	}
	for i, want := range []string{"first", "second"} {
		res, ok := got[i]["result"].(map[string]interface{})
		if !ok || res["q"] != want || res["fixed"] != float64(1) {
			t.Errorf("item %d result = %#v", i, got[i]["result"])
		}
		if got[i]["tool"] != "echo" || got[i]["is_error"] != false {
			t.Errorf("item %d = %+v", i, got[i])
		}
		if c, ok := got[i]["content"].([]interface{}); !ok || len(c) != 1 {
			t.Errorf("item %d content = %#v", i, got[i]["content"])
		}
	}
}

func TestStdioCallToolNoItemsJSONStringArgs(t *testing.T) {
	cfg := stdioConfig(map[string]interface{}{"tool": "echo", "arguments": `{"a": "b"}`})
	got, err := run(t, context.Background(), nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["result"].(map[string]interface{})["a"] != "b" {
		t.Fatalf("items = %+v", got)
	}
}

func TestResultShapes(t *testing.T) {
	got, err := run(t, context.Background(),
		[]map[string]interface{}{{"t": "structured"}, {"t": "plain"}},
		stdioConfig(map[string]interface{}{"tool": "{{ $json.t }}"}))
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := got[0]["result"].(map[string]interface{}); res["source"] != "structured" {
		t.Errorf("structuredContent not preferred: %#v", got[0]["result"])
	}
	if got[1]["result"] != "hello there" {
		t.Errorf("plain text result = %#v", got[1]["result"])
	}
}

func TestToolErrorFailsNode(t *testing.T) {
	_, err := run(t, context.Background(), nil, stdioConfig(map[string]interface{}{"tool": "fail"}))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the tool's error text", err)
	}
}

func TestToolErrorAsItem(t *testing.T) {
	got, err := run(t, context.Background(), nil,
		stdioConfig(map[string]interface{}{"tool": "fail", "fail_on_tool_error": false}))
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["is_error"] != true || got[0]["result"] != "boom" {
		t.Fatalf("item = %+v", got[0])
	}
}

func TestStdioTimeout(t *testing.T) {
	start := time.Now()
	_, err := run(t, context.Background(), nil,
		stdioConfig(map[string]interface{}{"tool": "sleep", "timeout": 0.3}))
	if err == nil || !strings.Contains(err.Error(), "no response within") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("timeout took %s", d)
	}
}

func TestEnvSecretRefs(t *testing.T) {
	stubSecrets(t, map[string]string{"tok": "s3cret"})
	cfg := stdioConfig(map[string]interface{}{"tool": "env"})
	cfg["env"] = `{"` + fakeServerEnv + `": "1", "FAKE_TOKEN": "x-@secret:tok"}`
	got, err := run(t, context.Background(), nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["result"] != "x-s3cret" {
		t.Fatalf("env not resolved: %#v", got[0]["result"])
	}

	cfg["env"] = map[string]interface{}{fakeServerEnv: "1", "FAKE_TOKEN": "@secret:missing"}
	if _, err := run(t, context.Background(), nil, cfg); !errors.Is(err, workflow.ErrInvalidConfig) {
		t.Fatalf("unresolved secret: err = %v, want ErrInvalidConfig", err)
	}
}

func TestServerFromSecret(t *testing.T) {
	def, _ := json.Marshal(map[string]interface{}{
		"transport": "stdio",
		"command":   os.Args[0],
		"args":      []string{"-test.run=^$"},
		"env":       map[string]string{fakeServerEnv: "1", "FAKE_TOKEN": "@secret:tok"},
	})
	stubSecrets(t, map[string]string{"fake-mcp": string(def), "tok": "from-vault"})
	got, err := run(t, context.Background(), nil, map[string]interface{}{"server": "fake-mcp", "tool": "env"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["result"] != "from-vault" {
		t.Fatalf("result = %#v", got[0]["result"])
	}
	// The engine may already have replaced "@secret:fake-mcp" by the JSON.
	got, err = run(t, context.Background(), nil, map[string]interface{}{"server": string(def), "operation": "list_tools"})
	if err != nil || len(got) != 3 {
		t.Fatalf("pre-resolved server: %v %+v", err, got)
	}
}

func TestInvalidConfig(t *testing.T) {
	for name, cfg := range map[string]map[string]interface{}{
		"no command":    {"transport": "stdio", "tool": "x"},
		"bad transport": {"transport": "ws", "url": "ws://x", "tool": "x"},
		"bad url":       {"transport": "http", "url": "ftp://x", "tool": "x"},
		"bad operation": {"command": "x", "operation": "delete"},
		"call no tool":  {"command": "x", "operation": "call_tool"},
		"bad headers":   {"url": "http://x", "headers": "not json"},
	} {
		if _, err := (&CallNode{}).Execute(context.Background(), workflow.NodeInput{}, cfg); !errors.Is(err, workflow.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
}

// fakeHTTP is a Streamable HTTP MCP endpoint backed by fakeServer.
type fakeHTTP struct {
	t       *testing.T
	sse     bool
	srv     fakeServer
	mu      sync.Mutex
	deleted bool
	pinged  bool
	auth    []string
}

func (f *fakeHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if r.Method == http.MethodDelete {
		if r.Header.Get("Mcp-Session-Id") != "sess-1" {
			http.Error(w, "no session", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.deleted = true
		f.mu.Unlock()
		return
	}
	if !strings.Contains(r.Header.Get("Accept"), "application/json") || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		http.Error(w, "bad Accept", http.StatusNotAcceptable)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var m rpcMessage
	if err := json.Unmarshal(body, &m); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if m.Method != "initialize" {
		if r.Header.Get("Mcp-Session-Id") != "sess-1" {
			http.Error(w, "missing session", http.StatusNotFound)
			return
		}
		if r.Header.Get("MCP-Protocol-Version") != ProtocolVersion {
			http.Error(w, "missing protocol version", http.StatusBadRequest)
			return
		}
	}
	resp := f.srv.handle(&m)
	if resp == nil {
		if m.isResponse() && idKey(m.ID) == "srv-ping" {
			f.mu.Lock()
			f.pinged = true
			f.mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if m.Method == "initialize" {
		w.Header().Set("Mcp-Session-Id", "sess-1")
	}
	b, _ := json.Marshal(resp)
	if !f.sse {
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, ": keep-alive\n\n")
	fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
	if m.Method == "tools/call" {
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"srv-ping\",\"method\":\"ping\"}\n\n")
	}
	// Split the response across two data lines.
	s := string(b)
	half := strings.Index(s, ",")
	fmt.Fprintf(w, "id: 1\nevent: message\ndata: %s\ndata: %s\n\n", s[:half+1], s[half+1:])
}

func TestHTTP(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%v", sse), func(t *testing.T) {
			stubSecrets(t, map[string]string{"zapier": "tok123"})
			fh := &fakeHTTP{t: t, sse: sse}
			ts := httptest.NewServer(fh)
			defer ts.Close()
			base := map[string]interface{}{
				"url":     ts.URL + "/mcp",
				"headers": map[string]interface{}{"Authorization": "Bearer @secret:zapier"},
			}
			cfg := map[string]interface{}{"operation": "list_tools"}
			for k, v := range base {
				cfg[k] = v
			}
			got, err := run(t, context.Background(), nil, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(toolNames(got), ",") != "echo,fail,structured" {
				t.Fatalf("tools = %v", toolNames(got))
			}

			cfg = map[string]interface{}{"tool": "echo", "arguments": map[string]interface{}{"x": "{{ $json.x }}"}}
			for k, v := range base {
				cfg[k] = v
			}
			got, err = run(t, context.Background(), []map[string]interface{}{{"x": "one"}, {"x": "two"}}, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[1]["result"].(map[string]interface{})["x"] != "two" {
				t.Fatalf("items = %+v", got)
			}

			cfg["tool"] = "fail"
			if _, err := run(t, context.Background(), nil, cfg); err == nil || !strings.Contains(err.Error(), "boom") {
				t.Fatalf("isError: err = %v", err)
			}

			fh.mu.Lock()
			defer fh.mu.Unlock()
			if !fh.deleted {
				t.Error("session was not ended with DELETE")
			}
			if sse && !fh.pinged {
				t.Error("server ping on the event stream was not answered")
			}
			for _, a := range fh.auth {
				if a != "Bearer tok123" {
					t.Fatalf("Authorization = %q, want the resolved secret", a)
				}
			}
		})
	}
}

func TestHTTPTimeout(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()
	defer close(release)
	start := time.Now()
	_, err := run(t, context.Background(), nil, map[string]interface{}{"url": ts.URL, "tool": "x", "timeout": 0.3})
	if err == nil || !strings.Contains(err.Error(), "no response within") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not applied")
	}
}

func TestHTTPErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer ts.Close()
	_, err := run(t, context.Background(), nil, map[string]interface{}{"url": ts.URL, "tool": "x"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v", err)
	}
}

// The server process carries the run's marker, whatever the node's env
// says: what a workflow runs is never the operator.
func TestStdioServerMarksTheWorkflowRun(t *testing.T) {
	t.Setenv(workflow.EnvWorkflowRun, "")
	cfg := stdioConfig(map[string]interface{}{"tool": "run"})
	cfg["env"] = map[string]interface{}{fakeServerEnv: "1", workflow.EnvWorkflowRun: ""}
	outs, err := (&CallNode{}).Execute(context.Background(), workflow.NodeInput{ExecutionID: "exec-7"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := outs[0].Items[0].JSON["result"]; got != "exec-7" {
		t.Errorf("the server saw %#v, want exec-7", got)
	}
}

// The server process carries the run's token (and only that: not one the node's env names), so a tool it
// runs can prove its run is approved; a run with no token hands it none.
func TestStdioServerCarriesTheRunsToken(t *testing.T) {
	for _, c := range []struct{ name, ctxTok, want string }{{"the run's token", "tok-1", "tok-1"}, {"no token, a forged env one", "", ""}} {
		cfg := stdioConfig(map[string]interface{}{"tool": "runtoken"})
		cfg["env"] = map[string]interface{}{fakeServerEnv: "1", workflow.EnvRunToken: "forged"}
		outs, err := (&CallNode{}).Execute(workflow.WithRunToken(context.Background(), c.ctxTok), workflow.NodeInput{ExecutionID: "exec-7"}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got := outs[0].Items[0].JSON["result"]; got != c.want {
			t.Errorf("%s: the server saw %#v, want %q", c.name, got, c.want)
		}
	}
}

// A vault server entry's own url wins: a node url that names another
// address is refused, so the entry's headers cannot be sent there (#897).
func TestServerEntryURLWins(t *testing.T) {
	def := `{"url":"http://127.0.0.1:1/mcp","headers":{"X-Api-Key":"k"}}`
	_, err := loadServer(context.Background(), map[string]interface{}{"server": def, "url": "http://127.0.0.2:1/mcp"})
	if err == nil || !strings.Contains(err.Error(), "differs from the url of the server entry") {
		t.Fatalf("a differing url: %v", err)
	}
	srv, err := loadServer(context.Background(), map[string]interface{}{"server": def, "url": "http://127.0.0.1:1/mcp"})
	if err != nil || srv.URL != "http://127.0.0.1:1/mcp" {
		t.Fatalf("the same url: %v %+v", err, srv)
	}
	srv, err = loadServer(context.Background(), map[string]interface{}{"server": def})
	if err != nil || srv.URL != "http://127.0.0.1:1/mcp" {
		t.Fatalf("no node url: %v %+v", err, srv)
	}
}

// A stdio server entry's command and args are pinned like its url: the
// entry's env secrets go only to the program the entry names.
func TestServerEntryCommandAndArgsArePinned(t *testing.T) {
	def := `{"command":"/usr/bin/fake","args":["--serve"],"env":{"K":"v"}}`
	ctx := context.Background()
	for name, cfg := range map[string]map[string]interface{}{
		"command":   {"server": def, "command": "/bin/sh"},
		"args":      {"server": def, "args": []interface{}{"-c", "env"}},
		"extra arg": {"server": def, "args": []interface{}{"--serve", "-c"}},
	} {
		if _, err := loadServer(ctx, cfg); err == nil || !strings.Contains(err.Error(), "of the server entry") {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, cfg := range map[string]map[string]interface{}{
		"same":    {"server": def, "command": "/usr/bin/fake", "args": []interface{}{"--serve"}},
		"omitted": {"server": def},
	} {
		if srv, err := loadServer(ctx, cfg); err != nil || srv.Command != "/usr/bin/fake" || len(srv.Args) != 1 {
			t.Errorf("%s: %v %+v", name, err, srv)
		}
	}
	// Without an entry the node names its own program.
	if srv, err := loadServer(ctx, map[string]interface{}{"command": "/bin/echo", "args": []interface{}{"x"}}); err != nil || srv.Command != "/bin/echo" {
		t.Errorf("no entry: %v %+v", err, srv)
	}
}

// command and args must be written in the workflow, like url.
func TestCommandAndArgsAreLiteralFields(t *testing.T) {
	fields := (&CallNode{}).LiteralConfigFields()
	for _, want := range []string{"command", "args"} {
		if !slices.Contains(fields, want) {
			t.Errorf("%q is not a literal field: %v", want, fields)
		}
	}
}

// A trailing slash and the case of scheme and host don't make a node url
// differ from the entry's.
func TestServerEntryURLComparedNormalised(t *testing.T) {
	def := `{"url":"http://Example.com/mcp/","headers":{"X-Api-Key":"k"}}`
	for _, u := range []string{"http://example.com/mcp", "HTTP://EXAMPLE.COM/mcp/", "http://example.com/mcp/"} {
		if _, err := loadServer(context.Background(), map[string]interface{}{"server": def, "url": u}); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"http://example.com/MCP", "https://example.com/mcp", "http://example.com:81/mcp", "http://example.com/mcp?x=1"} {
		_, err := loadServer(context.Background(), map[string]interface{}{"server": def, "url": u})
		if err == nil || !strings.Contains(err.Error(), "leave url out") {
			t.Errorf("%s: %v", u, err)
		}
	}
}
