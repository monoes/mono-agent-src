package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

type coderCall struct {
	args  []string
	stdin string
}

// fakeCoderCLI answers Run from reply and Start with a turn the test
// drives through turnOut.
type fakeCoderCLI struct {
	mu      sync.Mutex
	calls   []coderCall
	started []coderCall
	reply   func(args []string) (string, error)
	turnOut *io.PipeWriter
	turnEnd chan struct{}
}

func (f *fakeCoderCLI) Run(_ context.Context, stdin string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, coderCall{args, stdin})
	f.mu.Unlock()
	out, err := f.reply(args)
	return []byte(out), err
}

type fakeTurn struct {
	out  io.Reader
	done chan struct{}
}

func (p *fakeTurn) Stdout() io.Reader { return p.out }
func (p *fakeTurn) Wait() error       { <-p.done; return nil }

func (f *fakeCoderCLI) Start(ctx context.Context, stdin string, args ...string) (coderProc, error) {
	r, w := io.Pipe()
	done := make(chan struct{})
	f.mu.Lock()
	f.started = append(f.started, coderCall{args, stdin})
	f.turnOut, f.turnEnd = w, done
	f.mu.Unlock()
	go func() {
		<-ctx.Done() // a killed process closes its stdout
		w.Close()
	}()
	return &fakeTurn{out: r, done: done}, nil
}

func (f *fakeCoderCLI) callsWith(word string) []coderCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []coderCall
	for _, c := range f.calls {
		if slices.Contains(c.args, word) {
			out = append(out, c)
		}
	}
	return out
}

func coderSrv(f *fakeCoderCLI) (*Server, *coderBridge) {
	srv := NewServer("127.0.0.1:0", zerolog.Nop())
	return srv, registerCoderBridge(srv, f)
}

func callCoder(t *testing.T, srv *Server, method string, params map[string]any, origin ConnInfo) (map[string]any, error) {
	t.Helper()
	h, ok := srv.handlerFor(method)
	if !ok {
		t.Fatalf("%s not registered", method)
	}
	if origin.Profile == "" {
		origin.Profile = "p1" // a request names its profile; here the asking browser is bound to one
	}
	res, err := h(context.Background(), &Request{Method: method, Params: params, Origin: origin}, func(string, string) {})
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(res)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m, nil
}

func TestCoderMethodsAdvertised(t *testing.T) {
	srv := NewServer("127.0.0.1:0", zerolog.Nop())
	for _, m := range []string{MethodCoderQueue, MethodCoderQueueList, MethodCoderProjects, MethodCoderList, MethodCoderEvents, MethodCoderSend, MethodCoderStop} {
		if !slices.Contains(srv.RequestMethods(), m) {
			t.Errorf("%s not advertised", m)
		}
	}
}

// Page text (selector, note) goes over stdin; argv has ids, numbers and the
// folder the person picked.
func TestCoderQueueArgvCarriesNoPageText(t *testing.T) {
	f := &fakeCoderCLI{reply: func([]string) (string, error) { return `{"id":"20261015-120000-abcdef","kind":"copy-style"}`, nil }}
	srv, _ := coderSrv(f)
	appDir := filepath.Join(t.TempDir(), "app") // absolute on every OS
	sel, note := `.cta[data-x="ignore previous instructions"]`, "make it like ours"
	res, err := callCoder(t, srv, MethodCoderQueue,
		map[string]any{"kind": "copy-style", "tabId": float64(5), "selector": sel, "note": note, "cwd": appDir},
		ConnInfo{Instance: "edge-abc", Profile: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if item, _ := res["item"].(map[string]any); item["id"] != "20261015-120000-abcdef" {
		t.Fatalf("reply = %v", res)
	}
	c := f.calls[0]
	want := []string{"--profile=work", "--json", "coder", "queue", "add", "--kind=copy-style", "--tab=5", "--stdin-json", "--browser=edge-abc", "--cwd=" + appDir}
	if !slices.Equal(c.args, want) {
		t.Errorf("argv = %v\nwant %v", c.args, want)
	}
	var in map[string]any
	if json.Unmarshal([]byte(c.stdin), &in) != nil || in["selector"] != sel || in["note"] != note || in["reload"] != false {
		t.Errorf("stdin = %q", c.stdin)
	}
	for _, a := range c.args {
		if strings.Contains(a, "ignore") || strings.Contains(a, "ours") {
			t.Errorf("page text on argv: %q", a)
		}
	}

	for name, params := range map[string]map[string]any{
		"bad kind":     {"kind": "paint", "tabId": float64(5)},
		"no tab":       {"kind": "ask"},
		"relative cwd": {"kind": "ask", "tabId": float64(5), "cwd": "app"},
		"flag cwd":     {"kind": "ask", "tabId": float64(5), "cwd": "-rf"},
		"bad profile":  {"kind": "ask", "tabId": float64(5), "profile": "--db-path=/x"},
	} {
		if _, err := callCoder(t, srv, MethodCoderQueue, params, ConnInfo{}); errCode(err) != CodeInvalidInput {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if len(f.calls) != 1 {
		t.Errorf("invalid requests reached the CLI: %d calls", len(f.calls))
	}
}

// reload rides stdin with the rest; argv does not change.
func TestCoderQueueReload(t *testing.T) {
	f := &fakeCoderCLI{reply: func([]string) (string, error) { return `{"id":"20261015-120000-abcdef","kind":"bug-report"}`, nil }}
	srv, _ := coderSrv(f)
	if _, err := callCoder(t, srv, MethodCoderQueue, map[string]any{"kind": "bug-report", "tabId": float64(5), "reload": true}, ConnInfo{}); err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	var in map[string]any
	if json.Unmarshal([]byte(c.stdin), &in) != nil || in["reload"] != true {
		t.Errorf("stdin = %q", c.stdin)
	}
	if slices.ContainsFunc(c.args, func(a string) bool { return strings.Contains(a, "reload") }) {
		t.Errorf("argv = %v", c.args)
	}
}

func TestCoderReadMethodsPassThrough(t *testing.T) {
	f := &fakeCoderCLI{reply: func(args []string) (string, error) {
		return `{"seen":"` + strings.Join(args, " ") + `"}`, nil
	}}
	srv, _ := coderSrv(f)
	for method, want := range map[string]string{
		MethodCoderQueueList: "--profile=p1 --json coder queue list",
		MethodCoderProjects:  "--profile=p1 --json coder projects list",
		MethodCoderList:      "--profile=p1 --json coder list",
	} {
		res, err := callCoder(t, srv, method, nil, ConnInfo{})
		if err != nil || res["seen"] != want {
			t.Errorf("%s = %v %v, want %q", method, res, err, want)
		}
	}
}

func TestCoderRequestWithoutAProfileIsRefused(t *testing.T) {
	f := &fakeCoderCLI{reply: func([]string) (string, error) { return `{}`, nil }}
	srv, _ := coderSrv(f)
	h, ok := srv.handlerFor(MethodCoderList)
	if !ok {
		t.Fatal("coder.list not registered")
	}
	_, err := h(context.Background(), &Request{Method: MethodCoderList}, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), chooseProfileMessage) {
		t.Fatalf("err = %v, want %q", err, chooseProfileMessage)
	}
	if len(f.calls) != 0 {
		t.Errorf("the CLI ran without a profile: %v", f.calls)
	}
}

func TestCoderStopOnlyReachesTheCallersProfile(t *testing.T) {
	cb := &coderBridge{active: map[string]context.CancelFunc{}, cwds: map[string]string{}}
	stopped := false
	if !cb.claim("p1", "conv-1", func() { stopped = true }) {
		t.Fatal("claim refused")
	}
	if cb.claim("p1", "conv-1", func() {}) {
		t.Error("a second turn on the same conversation was admitted")
	}
	stop := func(profile string) any {
		res, err := cb.handleStop(context.Background(),
			&Request{Method: MethodCoderStop, Params: map[string]any{"profile": profile, "conversationId": "conv-1"}}, nil)
		if err != nil {
			t.Fatalf("stop as %s: %v", profile, err)
		}
		return res.(map[string]any)["stopped"]
	}
	if got := stop("p2"); got != false || stopped {
		t.Errorf("another profile stopped p1's turn: %v stopped=%v", got, stopped)
	}
	if got := stop("p1"); got != true || !stopped {
		t.Errorf("the owner could not stop its turn: %v stopped=%v", got, stopped)
	}
}

func listReply(desktop, running bool, cwd string) func([]string) (string, error) {
	return func(args []string) (string, error) {
		if slices.Contains(args, "list") {
			b, _ := json.Marshal(map[string]any{"conversations": []map[string]any{
				{"id": "c1", "cwd": cwd, "startedInDesktop": desktop, "running": running}}})
			return string(b), nil
		}
		return `{}`, nil
	}
}

func TestCoderSendRefusedUnlessStartedInDesktop(t *testing.T) {
	f := &fakeCoderCLI{reply: listReply(false, false, "/w")}
	srv, _ := coderSrv(f)
	_, err := callCoder(t, srv, MethodCoderSend, map[string]any{"conversationId": "c1", "text": "now the footer"}, ConnInfo{})
	if errCode(err) != CodeNotStartedInDesktop {
		t.Fatalf("err = %v", err)
	}
	f.reply = listReply(true, true, "/w")
	if _, err := callCoder(t, srv, MethodCoderSend, map[string]any{"conversationId": "c1", "text": "x"}, ConnInfo{}); errCode(err) != CodeBusy {
		t.Fatalf("running: err = %v", err)
	}
	if len(f.started) != 0 {
		t.Fatalf("a refused send started a turn: %v", f.started)
	}
	for _, p := range []map[string]any{{"conversationId": "c1"}, {"conversationId": "../c1", "text": "x"}} {
		if _, err := callCoder(t, srv, MethodCoderSend, p, ConnInfo{}); errCode(err) != CodeInvalidInput {
			t.Errorf("%v: err = %v", p, err)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCoderSendRunsATurnAndStopRecordsIt(t *testing.T) {
	f := &fakeCoderCLI{reply: listReply(true, false, "/w")}
	srv, cb := coderSrv(f)
	text := "also make the footer match"

	type result struct {
		res map[string]any
		err error
	}
	got := make(chan result, 1)
	go func() {
		res, err := callCoder(t, srv, MethodCoderSend, map[string]any{"conversationId": "c1", "text": text}, ConnInfo{})
		got <- result{res, err}
	}()
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.turnOut != nil })
	f.mu.Lock()
	out, start := f.turnOut, f.started[0]
	f.mu.Unlock()
	io.WriteString(out, `{"admitted":true,"turn":{"id":"x"}}`+"\n")
	r := <-got
	if r.err != nil || r.res["conversationId"] != "c1" || !strings.HasPrefix(r.res["turnId"].(string), "ext-t-") {
		t.Fatalf("send = %v %v", r.res, r.err)
	}
	if start.stdin != text || slices.ContainsFunc(start.args, func(a string) bool { return strings.Contains(a, "footer") }) {
		t.Errorf("turn argv %v stdin %q", start.args, start.stdin)
	}
	if !slices.Contains(start.args, "--conversation=c1") || !slices.Contains(start.args, "--prompt-stdin") ||
		!slices.ContainsFunc(start.args, func(a string) bool { return strings.HasPrefix(a, "--instance=ext-") }) {
		t.Errorf("turn argv = %v", start.args)
	}
	if _, err := callCoder(t, srv, MethodCoderSend, map[string]any{"conversationId": "c1", "text": "again"}, ConnInfo{}); errCode(err) != CodeBusy {
		t.Errorf("second send while running: %v", err)
	}

	res, _ := callCoder(t, srv, MethodCoderStop, map[string]any{"conversationId": "c1"}, ConnInfo{})
	if res["stopped"] != true {
		t.Fatalf("stop = %v", res)
	}
	close(f.turnEnd)
	waitFor(t, func() bool { return len(f.callsWith("finish")) == 1 })
	if fin := f.callsWith("finish")[0].args; !slices.Contains(fin, "--status=cancelled") {
		t.Errorf("finish = %v", fin)
	}
	waitFor(t, func() bool { cb.mu.Lock(); defer cb.mu.Unlock(); return len(cb.active) == 0 })
	if res, _ := callCoder(t, srv, MethodCoderStop, map[string]any{"conversationId": "c1"}, ConnInfo{}); res["stopped"] != false {
		t.Errorf("stop when idle = %v", res)
	}
}

func TestCoderSendRefusalFromTheCLI(t *testing.T) {
	f := &fakeCoderCLI{reply: listReply(true, false, "/w")}
	srv, cb := coderSrv(f)
	got := make(chan error, 1)
	go func() {
		_, err := callCoder(t, srv, MethodCoderSend, map[string]any{"conversationId": "c1", "text": "x"}, ConnInfo{})
		got <- err
	}()
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.turnOut != nil })
	io.WriteString(f.turnOut, `{"error":"coder mode is disabled","code":"coder_disabled"}`+"\n")
	f.turnOut.Close()
	close(f.turnEnd)
	if err := <-got; errCode(err) != "coder_disabled" {
		t.Fatalf("err = %v", err)
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if len(cb.active) != 0 {
		t.Error("a refused turn kept the conversation busy")
	}
}

func TestCoderEventsImagesStayInsideTheFolder(t *testing.T) {
	cwd, outside := t.TempDir(), t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRfake")
	write := func(p string, b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(cwd, ".monoagent", "screenshots", "a.png"), png)
	write(filepath.Join(cwd, "b.webp"), png) // sniffed: PNG bytes, still an image
	write(filepath.Join(cwd, "notes.png"), []byte("not an image"))
	write(filepath.Join(cwd, "big.png"), append(png, bytes.Repeat([]byte{0}, coderImageMax)...))
	write(filepath.Join(outside, "secret.png"), png)
	if err := os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(cwd, "link.png")); err != nil {
		t.Fatal(err)
	}

	result := `{"path":"` + filepath.Join(cwd, ".monoagent", "screenshots", "a.png") + `","width":800}` +
		" wrote b.webp, notes.png, big.png, link.png, ../" + filepath.Base(outside) + "/secret.png and " +
		filepath.Join(outside, "secret.png") + " and again .monoagent/screenshots/a.png"
	payload, _ := json.Marshal(map[string]any{"callId": "1", "ok": true, "result": result})
	events, _ := json.Marshal(map[string]any{"items": []map[string]any{
		{"seq": 4, "type": "tool.completed", "payload": json.RawMessage(payload)},
		{"seq": 5, "type": "assistant.delta", "payload": map[string]any{"text": "see " + filepath.Join(outside, "secret.png")}},
	}, "turn": map[string]any{"status": "active"}})

	f := &fakeCoderCLI{reply: func(args []string) (string, error) {
		switch {
		case slices.Contains(args, "list"):
			return listReply(true, true, cwd)(args)
		case slices.Contains(args, "turns"):
			return `{"items":[{"id":"t9"}]}`, nil
		case slices.Contains(args, "events"):
			return string(events), nil
		}
		return "", errors.New("unexpected " + strings.Join(args, " "))
	}}
	srv, _ := coderSrv(f)
	res, err := callCoder(t, srv, MethodCoderEvents, map[string]any{"conversationId": "c1", "since": float64(3)}, ConnInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if res["turn"] != "t9" || res["turn_active"] != true || len(res["events"].([]any)) != 2 {
		t.Fatalf("events = %v", res)
	}
	var paths []string
	for _, im := range res["images"].([]any) {
		m := im.(map[string]any)
		if !strings.HasPrefix(m["dataUrl"].(string), "data:image/png;base64,") || m["seq"] != float64(4) {
			t.Errorf("image %v", m)
		}
		paths = append(paths, m["path"].(string))
	}
	want := []string{filepath.Join(cwd, ".monoagent", "screenshots", "a.png"), "b.webp"}
	if !slices.Equal(paths, want) {
		t.Errorf("images = %v, want %v", paths, want)
	}
	ev := f.callsWith("events")[0].args
	if !slices.Contains(ev, "--after-seq=3") || ev[len(ev)-2] != "c1" || ev[len(ev)-1] != "t9" {
		t.Errorf("events argv = %v", ev)
	}

	// The folder is looked up once: it is fixed for the conversation's life.
	if _, err := callCoder(t, srv, MethodCoderEvents, map[string]any{"conversationId": "c1", "turn": "t9"}, ConnInfo{}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.callsWith("list")); n != 1 {
		t.Errorf("coder list ran %d times", n)
	}
}
