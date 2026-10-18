package extension

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/recording"
)

// nextAck reads frames until the recording ack for id arrives.
func (f *fakeExtension) nextAck(id string) *Response {
	f.t.Helper()
	deadline := time.Now().Add(waitBudget(f.t))
	for {
		_ = f.conn.SetReadDeadline(deadline)
		_, msg, err := f.conn.ReadMessage()
		if err != nil {
			f.t.Fatalf("read ack %s: %v", id, err)
		}
		var resp Response
		if json.Unmarshal(msg, &resp) == nil && resp.Type == RecordingAckType && resp.ID == id {
			return &resp
		}
	}
}

func recFrame(id, op string, extra map[string]any) map[string]any {
	f := map[string]any{"kind": KindRecording, "op": op, "recordingId": "rec-ws"}
	if id != "" {
		f["id"] = id
	}
	for k, v := range extra {
		f[k] = v
	}
	return f
}

func TestRecordingFramesOverWebSocketLandAsEnvelope(t *testing.T) {
	_, ext, inbox := startCaptureServer(t)

	ext.send(recFrame("a1", "start", map[string]any{"url": "https://example.com/x", "title": "X", "goal": "g", "tabId": 3}))
	if ack := ext.nextAck("a1"); !ack.Success {
		t.Fatalf("start refused: %s", ack.Error)
	}
	ext.send(recFrame("a2", "event", map[string]any{"event": map[string]any{
		"id": "e1", "seq": 1, "type": "type", "url": "https://example.com/x", "value": "s3cret",
		"target": map[string]any{"tag": "input", "inputType": "password", "candidates": []any{}}}}))
	if ack := ext.nextAck("a2"); !ack.Success {
		t.Fatalf("event refused: %s", ack.Error)
	}
	// No id: applied, not acked.
	ext.send(recFrame("", "snapshot", map[string]any{"eventId": "e1", "name": "dom-e1.html", "data": "<input>"}))
	ext.send(recFrame("a3", "snapshot", map[string]any{"eventId": "e1", "name": "../../evil", "data": "x"}))
	if ack := ext.nextAck("a3"); ack.Success {
		t.Fatal("traversal snapshot name accepted")
	}
	ext.send(recFrame("a4", "stop", map[string]any{"reason": "user"}))
	ack := ext.nextAck("a4")
	if !ack.Success {
		t.Fatalf("stop failed: %s", ack.Error)
	}
	data, _ := ack.Data.(map[string]any)
	path, _ := data["path"].(string)
	store, _ := recording.StoreDir("")
	if filepath.Dir(path) != store || data["recordingId"] != "rec-ws" {
		t.Fatalf("stop ack data = %v, want a path in %s", data, store)
	}
	// Never in the capture inbox, which feeds the knowledge brain.
	if entries, _ := capture.List(inbox); len(entries) != 0 {
		t.Fatalf("recording landed in the capture inbox: %+v", entries)
	}
	if fi, err := os.Stat(store); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("store dir mode: %v %v", fi, err)
	}
	meta, err := capture.ReadMeta(path)
	if err != nil || meta.Source != recording.SourceRecording || meta.URL != "https://example.com/x" {
		t.Fatalf("meta = %+v, %v", meta, err)
	}
	raw, _ := os.ReadFile(filepath.Join(path, recording.EventsArtifact))
	if strings.Contains(string(raw), "s3cret") || !strings.Contains(string(raw), `"masked":true`) {
		t.Fatalf("events.jsonl = %s", raw)
	}
	if _, err := os.Stat(filepath.Join(path, "dom-e1.html")); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
}

func TestRecordingIdleReaperRunsWithoutAConnection(t *testing.T) {
	srv, ext, inbox := startCaptureServerWith(t, func(s *Server) { s.SetRecordingReapInterval(20 * time.Millisecond) })

	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	srv.SetRecordingIngest(&recording.Ingest{Writer: &capture.Writer{Inbox: inbox}, Now: clock})
	done := make(chan string, 1)
	srv.OnRecording(func(res *capture.Result, err error) {
		if err == nil {
			done <- res.Path
		}
	})
	ext.send(recFrame("b1", "start", map[string]any{"url": "https://example.com/"}))
	ext.nextAck("b1")
	ext.send(recFrame("b2", "event", map[string]any{"event": map[string]any{"id": "e1", "seq": 1, "type": "click", "url": "https://example.com/"}}))
	ext.nextAck("b2")
	_ = ext.conn.Close() // the service worker goes away mid-recording

	mu.Lock()
	now = now.Add(recording.DefaultIdleTimeout + time.Minute)
	mu.Unlock()
	select {
	case path := <-done:
		meta, _ := capture.ReadMeta(path)
		var reason string
		_ = json.Unmarshal(meta.Extra[recording.ExtraStopReason], &reason)
		if reason != recording.StopIdle {
			t.Fatalf("stopReason = %q", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle recording never reaped")
	}
}

// argvRunner records the argv it was asked to run and answers with out.
type argvRunner struct {
	mu   sync.Mutex
	args []string
	out  string
}

func (r *argvRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args = args
	return []byte(r.out), nil
}

func TestRecordMethodsExecTheCLI(t *testing.T) {
	srv, ext, _ := startCaptureServer(t)
	run := &argvRunner{out: `{"recordings":[]}`}
	srv.SetRecordRunner(run)

	ext.ask("q1", MethodRecordList, map[string]any{"profile": "work"})
	reply := ext.settled()
	if !reply.OK {
		t.Fatalf("record.list: %s", reply.Error)
	}
	if got := strings.Join(run.args, " "); got != "--profile=work record list --json" {
		t.Fatalf("argv = %q", got)
	}

	ext.ask("q2", MethodRecordAnalyze, map[string]any{"recordingId": "--help"})
	if reply := ext.settled(); reply.OK || reply.Code != CodeBadParams {
		t.Fatalf("injection accepted: %+v", reply)
	}
}

func TestRecordMethodArgsValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	drafts, _ := recording.DraftsDir()
	draft := filepath.Join(drafts, "rec-1")
	if err := os.MkdirAll(draft, 0o700); err != nil {
		t.Fatal(err)
	}
	realDraft, _ := filepath.EvalSymlinks(draft)
	req := func(p map[string]any) *Request { return &Request{Params: p} }

	good := []struct {
		build func(*Request) ([]string, error)
		p     map[string]any
		want  string
	}{
		{recordAnalyzeArgs, map[string]any{"recordingId": "2026-09-25T09-00-00Z-x", "automation": "acme"},
			"record analyze 2026-09-25T09-00-00Z-x --automation=acme --json"},
		{verifyArgsOnly, map[string]any{"draftDir": "rec-1"}, "record verify " + realDraft + " --stop-at-first-action --json"},
		{recordSaveArgs, map[string]any{"draftDir": draft, "saveAs": "fragment", "new": "my-site", "name": "Create contact"},
			"record save " + realDraft + " --as=fragment --new=my-site --name=Create contact --json"},
		{recordSaveArgs, map[string]any{"draftDir": draft, "force": true}, "record save " + realDraft + " --force --json"},
		{recordSaveArgs, map[string]any{"draftDir": draft, "force": false}, "record save " + realDraft + " --json"},
		{recordSaveArgs, map[string]any{"draftDir": draft, "keepPackageSelectors": true}, "record save " + realDraft + " --keep-package-selectors --json"},
		{recordSaveArgs, map[string]any{"draftDir": draft, "force": true, "keepPackageSelectors": true}, "record save " + realDraft + " --force --keep-package-selectors --json"},
	}
	for _, g := range good {
		g.p["profile"] = "work"
		g.want = "--profile=work " + g.want
		args, err := g.build(req(g.p))
		if err != nil || strings.Join(args, " ") != g.want {
			t.Errorf("%v → %q, %v; want %q", g.p, strings.Join(args, " "), err, g.want)
		}
	}

	bad := []struct {
		build func(*Request) ([]string, error)
		p     map[string]any
	}{
		{recordAnalyzeArgs, map[string]any{}},
		{recordAnalyzeArgs, map[string]any{"recordingId": "../x"}},
		{recordAnalyzeArgs, map[string]any{"recordingId": "-rf"}},
		{recordAnalyzeArgs, map[string]any{"recordingId": "ok", "automation": "--exec=x"}},
		{recordAnalyzeArgs, map[string]any{"recordingId": "ok", "profile": "../../x"}},
		{recordListArgs, map[string]any{"profile": "-x"}},
		{verifyArgsOnly, map[string]any{"draftDir": "/etc"}},
		{verifyArgsOnly, map[string]any{"draftDir": drafts + "/../"}},
		{verifyArgsOnly, map[string]any{"draftDir": "--full"}},
		{verifyArgsOnly, map[string]any{"draftDir": filepath.Join(drafts, "missing")}},
		{recordSaveArgs, map[string]any{"draftDir": draft, "saveAs": "shell"}},
		{recordSaveArgs, map[string]any{"draftDir": draft, "automation": "a1", "new": "b2"}},
		{recordSaveArgs, map[string]any{"draftDir": draft, "new": "UPPER"}},
		{recordSaveArgs, map[string]any{"draftDir": draft, "name": "-x"}},
		{recordSaveArgs, map[string]any{"draftDir": draft, "name": "a\nb"}},
		{recordSaveArgs, map[string]any{"draftDir": draft, "force": "true"}},
		{recordSaveArgs, map[string]any{"draftDir": draft, "force": 1.0}},
		{recordSaveArgs, map[string]any{"draftDir": draft, "keepPackageSelectors": "true"}},
		{recordSaveArgs, map[string]any{"draftDir": draft, "keepPackageSelectors": 1.0}},
	}
	for _, b := range bad {
		if args, err := b.build(req(b.p)); err == nil {
			t.Errorf("%v accepted: %v", b.p, args)
		} else {
			var re *RequestError
			if !asRequestError(err, &re) || re.Code != CodeBadParams {
				t.Errorf("%v: error %v has no bad_params code", b.p, err)
			}
		}
	}
}

func TestRecordMethodDeadlines(t *testing.T) {
	srv := NewServer("127.0.0.1:0", zerolog.Nop())
	if got := srv.requestDeadline(MethodRecordAnalyze); got != recordAnalyzeTimeout {
		t.Fatalf("analyze deadline = %s", got)
	}
	if got := srv.requestDeadline(MethodDocLookup); got != requestTimeout {
		t.Fatalf("default deadline = %s", got)
	}
}

func TestSelfRunnerRefusesForeignBinary(t *testing.T) {
	_, err := selfRunner{}.Run(context.Background(), "record", "list", "--json")
	var re *RequestError
	if err == nil || !asRequestError(err, &re) || re.Code != CodeUnavailable {
		t.Fatalf("test binary exec'd as monoagentcli: %v", err)
	}
}

// The bridge runs the CLI as the user for whoever holds the pairing token, so
// its verify takes no "full" (true, false or anything else), and always
// stops before the first page action. The side panel never sent "full".
func TestRecordVerifyOverTheBridgeRunsNoPageAction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	drafts, _ := recording.DraftsDir()
	if err := os.MkdirAll(filepath.Join(drafts, "rec-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, full := range []any{true, false, "true"} {
		if args, _, err := recordVerifyArgs(&Request{Params: map[string]any{"draftDir": "rec-1", "profile": "work", "full": full}}); err == nil {
			t.Errorf("full %v accepted: %q", full, args)
		}
	}
	args, _, err := recordVerifyArgs(&Request{Params: map[string]any{"draftDir": "rec-1", "profile": "work"}})
	if err != nil || !slices.Contains(args, "--stop-at-first-action") || slices.Contains(args, "--full") {
		t.Fatalf("args = %q, %v", args, err)
	}
}

// verifyArgsOnly adapts recordVerifyArgs to the args-only table shape.
func verifyArgsOnly(req *Request) ([]string, error) {
	args, _, err := recordVerifyArgs(req)
	return args, err
}

func TestRecordVerifyInputsValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	drafts, _ := recording.DraftsDir()
	if err := os.MkdirAll(filepath.Join(drafts, "rec-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	args, inputs, err := recordVerifyArgs(&Request{Params: map[string]any{"draftDir": "rec-1", "profile": "work",
		"inputs": map[string]any{"query": "a=b c", "password": "hunter2"}}})
	if err != nil || inputs["query"] != "a=b c" || inputs["password"] != "hunter2" {
		t.Fatalf("inputs = %v, %v", inputs, err)
	}
	for _, a := range args {
		if strings.Contains(a, "hunter2") || strings.Contains(a, "a=b c") || strings.HasPrefix(a, "--input") {
			t.Fatalf("argv carries an input: %q", args)
		}
	}
	if shown := redactArgs([]string{"--input=password=hunter2"}); strings.Contains(shown, "hunter2") {
		t.Fatalf("redacted argv = %q", shown)
	}
	for _, in := range []any{
		"x=y",
		map[string]any{"a=b": "v"},
		map[string]any{"-x": "v"},
		map[string]any{"a.b": "v"},
		map[string]any{"": "v"},
		map[string]any{"n": 3},
		map[string]any{"n": "a\x00b"},
		map[string]any{"n": strings.Repeat("x", maxVerifyInputBytes+1)},
	} {
		if _, _, err := recordVerifyArgs(&Request{Params: map[string]any{"draftDir": "rec-1", "inputs": in}}); err == nil {
			t.Errorf("inputs %v accepted", in)
		}
	}
}

// fileRunner records the argv and the inputs file's state at exec time,
// then answers with out or fails.
type fileRunner struct {
	mu   sync.Mutex
	args []string
	path string
	mode os.FileMode
	dir  os.FileMode
	body map[string]string
	fail bool
}

func (r *fileRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args = args
	for _, a := range args {
		if p, ok := strings.CutPrefix(a, "--inputs-file="); ok {
			r.path = p
			if fi, err := os.Stat(p); err == nil {
				r.mode = fi.Mode().Perm()
			}
			if fi, err := os.Stat(filepath.Dir(p)); err == nil {
				r.dir = fi.Mode().Perm()
			}
			blob, _ := os.ReadFile(p)
			_ = json.Unmarshal(blob, &r.body)
		}
	}
	if r.fail {
		return nil, errors.New("monoagentcli " + redactArgs(args) + ": exit status 1")
	}
	return []byte(`{"ok":true,"steps":[]}`), nil
}

func TestRecordVerifyPassesInputsByPrivateFile(t *testing.T) {
	for _, fail := range []bool{false, true} {
		srv, ext, _ := startCaptureServer(t)
		home, _ := os.UserHomeDir()
		drafts, _ := recording.DraftsDir()
		if err := os.MkdirAll(filepath.Join(drafts, "rec-1"), 0o700); err != nil {
			t.Fatal(err)
		}
		run := &fileRunner{fail: fail}
		srv.SetRecordRunner(run)
		ext.ask("v1", MethodRecordVerify, map[string]any{"draftDir": "rec-1", "profile": "work", "inputs": map[string]any{"pw": "hunter2", "q": "x"}})
		reply := ext.settled()
		if reply.OK == fail || strings.Contains(reply.Error, "hunter2") {
			t.Fatalf("fail=%v reply = %+v", fail, reply)
		}
		run.mu.Lock()
		for _, a := range run.args {
			if strings.Contains(a, "hunter2") {
				t.Fatalf("argv carries a value: %q", run.args)
			}
		}
		if run.path == "" || filepath.Dir(run.path) != filepath.Join(home, ".monoagent", "tmp") {
			t.Fatalf("inputs file path = %q (argv %q)", run.path, run.args)
		}
		if run.args[len(run.args)-1] != "--json" || run.mode != 0o600 || run.dir != 0o700 || run.body["pw"] != "hunter2" || run.body["q"] != "x" {
			t.Fatalf("fail=%v args %q mode %o dir %o body %v", fail, run.args, run.mode, run.dir, run.body)
		}
		path := run.path
		run.mu.Unlock()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("fail=%v: inputs file still there: %v", fail, err)
		}
	}
}

func TestRecordingWithoutEventsIsDiscarded(t *testing.T) {
	_, ext, inbox := startCaptureServer(t)
	ext.send(recFrame("c1", "start", map[string]any{"url": "https://example.com/"}))
	ext.nextAck("c1")
	ext.send(recFrame("c2", "stop", map[string]any{"reason": "error"}))
	ack := ext.nextAck("c2")
	data, _ := ack.Data.(map[string]any)
	if !ack.Success || data["discarded"] != "no events" {
		t.Fatalf("stop ack = %+v", ack)
	}
	if entries, _ := capture.List(inbox); len(entries) != 0 {
		t.Fatalf("empty recording landed: %+v", entries)
	}
}

func TestCloseWaitsForRecordingReaper(t *testing.T) {
	srv, _, _ := startCaptureServerWith(t, func(s *Server) { s.SetRecordingReapInterval(time.Millisecond) })
	done := make(chan struct{})
	go func() { _ = srv.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned")
	}
}

// record.verify's refusal says nothing about the filesystem: a missing
// path, an existing one and a symlink out of the drafts folder all get the
// same bad_params answer (security review V3).
func TestRecordVerifyDoesNotLeakPathExistence(t *testing.T) {
	srv, ext, _ := startCaptureServer(t)
	srv.SetRecordRunner(&argvRunner{out: `{}`})
	home, _ := os.UserHomeDir()
	drafts, _ := recording.DraftsDir()
	existing := filepath.Join(home, "secret-project")
	for _, d := range []string{drafts, existing} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(drafts, "link-out")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	var first string
	for i, ref := range []string{filepath.Join(home, "no-such-dir"), existing, link, filepath.Join(drafts, "..", "no-such-dir")} {
		id := "v" + strconv.Itoa(i)
		ext.ask(id, MethodRecordVerify, map[string]any{"draftDir": ref})
		reply := ext.settled()
		if reply.OK || reply.Code != CodeBadParams {
			t.Fatalf("%s: reply = %+v", ref, reply)
		}
		if strings.Contains(reply.Error, "no such file") || strings.Contains(reply.Error, home) {
			t.Errorf("%s: error %q mentions the filesystem", ref, reply.Error)
		}
		if first == "" {
			first = reply.Error
		} else if reply.Error != first {
			t.Errorf("%s: answer %q differs from %q", ref, reply.Error, first)
		}
	}
}

// exitRunner answers like a CLI that exited non-zero with out on stdout.
type exitRunner struct{ out string }

func (r exitRunner) Run(context.Context, ...string) ([]byte, error) {
	return []byte(r.out), errors.New("monoagentcli record verify: exit status 1")
}

// A failed replay exits 1 but prints its report; the side panel must get
// the report, not just "verify failed" (e2e R6-1).
func TestRecordVerifyFailureReturnsTheReport(t *testing.T) {
	srv, ext, _ := startCaptureServer(t)
	drafts, _ := recording.DraftsDir()
	if err := os.MkdirAll(filepath.Join(drafts, "rec-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	report := `{
  "steps": [
    {"id": "s1", "type": "click", "status": "pass", "message": "", "selector": "#a"},
    {"id": "s2", "type": "type", "status": "fail", "message": "element not found", "selector": "#b"}
  ],
  "stoppedAt": null,
  "ok": false
}`
	srv.SetRecordRunner(exitRunner{out: report})
	ext.ask("v1", MethodRecordVerify, map[string]any{"draftDir": "rec-1", "profile": "work"})
	reply := ext.settled()
	if !reply.OK {
		t.Fatalf("failed verify lost its report: %+v", reply)
	}
	data, _ := reply.Data.(map[string]any)
	steps, _ := data["steps"].([]any)
	if len(steps) != 2 || data["ok"] != false {
		t.Fatalf("report = %v", data)
	}
	if s2, _ := steps[1].(map[string]any); s2["status"] != "fail" || s2["message"] != "element not found" {
		t.Fatalf("step 2 = %v", steps[1])
	}
	if _, has := data["stoppedAt"]; !has {
		t.Fatalf("stoppedAt dropped: %v", data)
	}
}

func TestRunRecordJSONOnFailure(t *testing.T) {
	cases := []struct {
		out     string
		wantErr string // "" = the output is the result
	}{
		{`{"steps":[],"ok":false}`, ""},
		{`{"steps":[],"ok":false,"error":"step failed"}`, ""},
		{`{"ok":false,"error":"step failed"}`, ""},
		{`{"error":"draft not found or outside the drafts folder"}`, "draft not found or outside the drafts folder"},
		{`{"error":"lint has errors","code":"lint"}`, "lint has errors"},
		{`{"ok":"no","error":"not a report"}`, "not a report"},
		{`{"something":"else"}`, "exit status 1"},
		{``, "exit status 1"},
		{`panic: boom`, "exit status 1"},
		{`[1,2]`, "exit status 1"},
	}
	for _, c := range cases {
		data, err := runRecordJSON(context.Background(), exitRunner{out: c.out}, []string{"record", "verify", "x", "--json"})
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%q: err %v, want the report", c.out, err)
		case c.wantErr == "" && data == nil:
			t.Errorf("%q: no data", c.out)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%q: err %v, want %q", c.out, err, c.wantErr)
		}
	}
}

// verifyFailureReport is the real shape `record verify --json` prints for a
// failed replay (v0.75.0, e2e r7 broken-cli.json, trimmed): the report
// carries a top-level "error" summary next to its steps.
const verifyFailureReport = `{
  "steps": [
    {"id": "open", "type": "navigate", "status": "pass"},
    {"id": "name", "type": "type", "status": "fail",
     "message": "action execution aborted: step name: type step name: no element to type into: selector \"contact.name_input\": none of 2 selector candidates matched within 10s: extension error: Element not found within 300ms: #contact-name"},
    {"id": "email", "type": "type", "status": "skipped"},
    {"id": "save", "type": "click", "status": "skipped"}
  ],
  "stoppedAt": null,
  "ok": false,
  "error": "action execution aborted: step name: type step name: no element to type into: selector \"contact.name_input\": none of 2 selector candidates matched within 10s: extension error: Element not found within 300ms: #contact-name",
  "highlighted": false,
  "tabLeftOpen": false
}`

// The side panel gets the whole report — steps included — for a failed
// verify whose report also has "error" (R6-1 on v0.75.0).
func TestRecordVerifyFailureWithErrorKeyReturnsTheReport(t *testing.T) {
	srv, ext, _ := startCaptureServer(t)
	drafts, _ := recording.DraftsDir()
	if err := os.MkdirAll(filepath.Join(drafts, "rec-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv.SetRecordRunner(exitRunner{out: verifyFailureReport})
	ext.ask("v1", MethodRecordVerify, map[string]any{"draftDir": "rec-1", "profile": "work"})
	reply := ext.settled()
	if !reply.OK {
		t.Fatalf("failed verify lost its report: %+v", reply)
	}
	data, _ := reply.Data.(map[string]any)
	steps, _ := data["steps"].([]any)
	if len(steps) != 4 || data["ok"] != false || data["highlighted"] != false || data["tabLeftOpen"] != false {
		t.Fatalf("report = %v", data)
	}
	if msg, _ := data["error"].(string); !strings.Contains(msg, "no element to type into") {
		t.Fatalf("report error summary = %v", data["error"])
	}
	if s2, _ := steps[1].(map[string]any); s2["status"] != "fail" {
		t.Fatalf("step 2 = %v", steps[1])
	}
	if _, has := data["stoppedAt"]; !has {
		t.Fatalf("stoppedAt dropped: %v", data)
	}
}

// A selector conflict on save reaches the side panel as a code and the
// conflicting keys, not only as text to match.
func TestRecordSaveConflictPassesCodeAndKeys(t *testing.T) {
	srv, ext, _ := startCaptureServer(t)
	drafts, _ := recording.DraftsDir()
	if err := os.MkdirAll(filepath.Join(drafts, "rec-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv.SetRecordRunner(exitRunner{out: `{"error":"merge conflict: selectors.json#contact.name_input\nselector(s) contact.name_input differ …","code":"selector_conflict","keys":["contact.name_input","contact.email_input"]}`})
	ext.ask("s1", MethodRecordSave, map[string]any{"draftDir": "rec-1", "profile": "work", "automation": "acme-crm"})
	reply := ext.settled()
	if reply.OK || reply.Code != "selector_conflict" || !strings.Contains(reply.Error, "merge conflict") {
		t.Fatalf("reply = %+v", reply)
	}
	data, _ := reply.Data.(map[string]any)
	keys, _ := data["keys"].([]any)
	if len(keys) != 2 || keys[0] != "contact.name_input" || keys[1] != "contact.email_input" {
		t.Fatalf("keys = %v", data)
	}
	if _, leaked := data["error"]; leaked {
		t.Fatalf("data repeats the error: %v", data)
	}
}

func TestPlainFailureCodes(t *testing.T) {
	// A code the bridge would not pass on (not a plain identifier) leaves
	// a bare error; no code at all likewise.
	for _, out := range []string{`{"error":"x","code":"Bad Code!"}`, `{"error":"x"}`} {
		_, err := runRecordJSON(context.Background(), exitRunner{out: out}, []string{"record", "save", "d", "--json"})
		var re *RequestError
		if err == nil || asRequestError(err, &re) {
			t.Errorf("%s: err = %#v", out, err)
		}
	}
}
