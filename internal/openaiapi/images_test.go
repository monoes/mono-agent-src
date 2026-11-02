package openaiapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

const imagesURL = "/v1/images/generations"

func postImages(h *harness, p Policy, secret, body string) *httpRecorder {
	return h.serve(p, http.MethodPost, imagesURL, secret, body)
}

// imageFolderRE finds the output folder a turn's system prompt names.
var imageFolderRE = regexp.MustCompile(`\./(out-[a-z2-7]{16})/`)

// givenFolder is the folder an image turn was told to save its images in: the one
// its system prompt names, inside its working folder. A turn that was told none (a
// chat turn) has its working folder.
func givenFolder(o monomind.ExecOptions) string {
	if m := imageFolderRE.FindStringSubmatch(o.SystemPrompt); m != nil {
		return filepath.Join(o.Cwd, m[1])
	}
	return o.Cwd
}

// imageTurn is a turn of an image runtime: it saves files in the folder it was given,
// as a runtime does after it made them, and replies.
func imageTurn(reply string, files map[string][]byte) execFunc {
	return imageTurnAt(givenFolder, reply, files)
}

// imageTurnTop is imageTurn for a runtime that ignores the folder it was given and
// saves at the top of its working folder.
func imageTurnTop(reply string, files map[string][]byte) execFunc {
	return imageTurnAt(func(o monomind.ExecOptions) string { return o.Cwd }, reply, files)
}

func imageTurnAt(where func(monomind.ExecOptions) string, reply string, files map[string][]byte) execFunc {
	return func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		for name, b := range files {
			if err := os.WriteFile(filepath.Join(where(opts), name), b, 0o600); err != nil {
				return nil, err
			}
		}
		res, err := scriptedExec(evStart(false, "workspace-write"), evText(reply), evUsage(40000, 500), evResult(reply, monomind.StopEndTurn), evDone(0))(ctx, opts, onEvent)
		if res != nil {
			res.SandboxStatus = monomind.SandboxStatusSandboxed
		}
		return res, err
	}
}

// imageExec is imageTurn that records the options of each turn it runs.
func (l *execLog) imageExec(reply string, files map[string][]byte) execFunc {
	inner := imageTurn(reply, files)
	return func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		l.mu.Lock()
		l.opts = append(l.opts, opts)
		l.mu.Unlock()
		return inner(ctx, opts, onEvent)
	}
}

func onePNG(name string) map[string][]byte { return map[string][]byte{name: pngBytes} }

// wantImageSystemPrompt fails the test unless the system prompt of a turn is the fixed
// one, naming one output folder of the random shape.
func wantImageSystemPrompt(t *testing.T, o monomind.ExecOptions) {
	t.Helper()
	m := imageFolderRE.FindStringSubmatch(o.SystemPrompt)
	if m == nil || o.SystemPrompt != imageSystemPrompt(m[1]) {
		t.Errorf("the system prompt of an image turn is the fixed one with its folder in it: %q", o.SystemPrompt)
	}
}

// waitStarted fails the test when the turn it waits for never starts, instead of
// blocking until the test binary's own timeout.
func waitStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never started")
	}
}

// decodeImages reads a response of POST /v1/images/generations the way an SDK does:
// the shape is exactly {"created":…,"data":[{"b64_json":…}]}.
func decodeImages(t *testing.T, rec *httptest.ResponseRecorder) (created int64, images [][]byte) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	delete(top, "usage") // checked by TestImagesReportUsage
	if len(top) != 2 || top["created"] == nil || top["data"] == nil {
		t.Fatalf("the response has keys %v, want exactly created and data", slices.Sorted(maps.Keys(top)))
	}
	if err := json.Unmarshal(top["created"], &created); err != nil {
		t.Fatal(err)
	}
	var data []map[string]string
	if err := json.Unmarshal(top["data"], &data); err != nil {
		t.Fatal(err)
	}
	for _, item := range data {
		if len(item) != 1 {
			t.Fatalf("an image has fields %v, want only b64_json", item)
		}
		b, err := base64.StdEncoding.DecodeString(item["b64_json"])
		if err != nil {
			t.Fatalf("b64_json is not base64: %v", err)
		}
		images = append(images, b)
	}
	return created, images
}

func TestImagesHappyPath(t *testing.T) {
	log := &execLog{}
	h := newHarness(t, log.imageExec("image-1.png", onePNG("image-1.png")))
	secret := h.key(t, "default", "app", false)

	before := time.Now().Unix()
	rec := postImages(h, anyPolicy, secret, `{"model":"codex","prompt":"a small red circle on a white background"}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, content type %q, body %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	created, images := decodeImages(t, rec)
	if created < before || created > time.Now().Unix() {
		t.Errorf("created = %d, want now", created)
	}
	if !equalImages(images, pngBytes) {
		t.Fatalf("got %d images, want the one the runtime saved", len(images))
	}
	if !strings.HasPrefix(rec.Header().Get("X-Request-Id"), "req_") || rec.Header().Get("X-Monoagent-Model") != "codex/default" ||
		rec.Header().Get("X-Monoagent-Sandbox") != "sandboxed" {
		t.Errorf("headers: %v", rec.Header())
	}

	if log.count() != 1 {
		t.Fatalf("%d turns ran, want 1", log.count())
	}
	o := log.opts[0]
	if o.Runtime != "codex" || o.Model != "" || o.Prompt != "a small red circle on a white background" {
		t.Errorf("runtime %q model %q prompt %q", o.Runtime, o.Model, o.Prompt)
	}
	// The same locked-down posture as a chat turn: a sandbox the sandboxed class depends on is required,
	// no caller tools, no settings, nothing of the client's but the prompt.
	if o.Sandbox != monomind.TurnSandboxMode || !o.RequireSandbox || o.WorkspacePurpose != "api" ||
		o.Access != "" || len(o.Tools) != 0 || o.OnToolCall != nil || len(o.Settings) != 0 || len(o.AllowBashPrefixes) != 0 {
		t.Errorf("the turn is not locked down: %+v", o)
	}
	if o.Cwd != filepath.Join(h.scratch, profileFolder("default"), "slot-0") || o.Timeout != time.Minute {
		t.Errorf("Cwd %q timeout %v", o.Cwd, o.Timeout)
	}
	for k := range o.Env {
		if k != "TMPDIR" && k != "TMP" && k != "TEMP" && k != "TYPESAFE_API_KEY" {
			t.Errorf("a turn's environment is its temp folder and nothing else, got %s", k)
		}
	}
	for _, want := range []string{"NO_IMAGE_TOOL", "current directory", "built-in image generation", "Do not draw", "copy", "Save nothing anywhere else"} {
		if !strings.Contains(o.SystemPrompt, want) {
			t.Errorf("the system prompt must say %q: %s", want, o.SystemPrompt)
		}
	}
	wantImageSystemPrompt(t, o)
	// What was collected was read before the folder was emptied, and the folder is empty now.
	if entries, _ := os.ReadDir(o.Cwd); len(entries) != 0 {
		t.Errorf("the slot folder must be left empty: %d entries", len(entries))
	}
}

// How many and how big go to the runtime in words; what it saved is what comes back,
// the first n by name, and a runtime that saved fewer still answers.
func TestImagesAskForNAndSizeAndReturnWhatWasSaved(t *testing.T) {
	four := map[string][]byte{"a.png": pngBytes, "b.jpg": jpegBytes, "c.webp": webpBytes, "d.gif": gifBytes}
	for name, c := range map[string]struct {
		body   string
		files  map[string][]byte
		prompt string
		want   [][]byte
	}{
		"n and size":           {`{"prompt":"cats","n":3,"size":"1024x1024"}`, four, "cats\n\nCreate 3 distinct images. Preferred size: 1024x1024.", [][]byte{pngBytes, jpegBytes, webpBytes}},
		"one by default":       {`{"prompt":"cats"}`, four, "cats", [][]byte{pngBytes}},
		"a size alone":         {`{"prompt":"cats","size":"1792X1024"}`, onePNG("a.png"), "cats\n\nPreferred size: 1792x1024.", [][]byte{pngBytes}},
		"auto is no size":      {`{"prompt":"cats","size":"auto"}`, onePNG("a.png"), "cats", [][]byte{pngBytes}},
		"fewer than asked":     {`{"prompt":"cats","n":4}`, map[string][]byte{"a.png": pngBytes, "b.jpg": jpegBytes}, "cats\n\nCreate 4 distinct images.", [][]byte{pngBytes, jpegBytes}},
		"other fields ignored": {`{"prompt":"cats","quality":"hd","style":"vivid","output_format":"webp","background":"transparent","user":"u-1","response_format":"b64_json"}`, onePNG("a.png"), "cats", [][]byte{pngBytes}},
	} {
		log := &execLog{}
		h := newHarness(t, log.imageExec("saved", c.files))
		rec := postImages(h, anyPolicy, h.key(t, "default", "app", false), c.body)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
			continue
		}
		if _, images := decodeImages(t, rec); !equalImages(images, c.want...) {
			t.Errorf("%s: got %d images, want %d", name, len(images), len(c.want))
		}
		if log.count() != 1 || log.opts[0].Prompt != c.prompt {
			t.Errorf("%s: the turn was told %q, want %q", name, log.opts[0].Prompt, c.prompt)
		}
	}
}

// A request that is refused starts nothing and takes no slot: with the only slot held by
// a turn that is running, each of them is still answered with its own refusal and not
// with a 429.
func TestImagesRejectBeforeSpawningAnything(t *testing.T) {
	var spawned atomic.Int32
	started, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		if spawned.Add(1) == 1 { // the turn that holds the slot
			started <- struct{}{}
			<-release
		}
		return imageTurn("x", onePNG("a.png"))(ctx, o, onEvent)
	}, func(_ *Deps, c *Config) { c.MaxConcurrent = 1 })
	secret := h.key(t, "default", "app", false)
	ctxKey := h.key(t, "default", "notes", true)
	chatOnly, sandboxed := Policy{Max: ChatOnly}, Policy{Max: Sandboxed}

	holder := make(chan int, 1)
	go func() { holder <- postImages(h, anyPolicy, secret, `{"prompt":"hold the slot"}`).Code }()
	waitStarted(t, started)

	for _, c := range []struct {
		name   string
		policy Policy
		secret string
		body   string
		status int
		code   string
		param  string
	}{
		{"no key", anyPolicy, "", `{"prompt":"x"}`, 401, "invalid_api_key", ""},
		{"malformed JSON", anyPolicy, secret, `{nope`, 400, "invalid_json", ""},
		{"an empty body", anyPolicy, secret, ``, 400, "invalid_json", ""},
		{"no prompt", anyPolicy, secret, `{"model":"codex"}`, 400, "missing_required_parameter", "prompt"},
		{"n over the cap", anyPolicy, secret, `{"prompt":"x","n":5}`, 400, "invalid_value", "n"},
		{"a malformed size", anyPolicy, secret, `{"prompt":"x","size":"1024x1024; rm -rf /"}`, 400, "invalid_value", "size"},
		{"a url", anyPolicy, secret, `{"prompt":"x","response_format":"url"}`, 400, "unsupported_parameter", "response_format"},
		{"a stream", anyPolicy, secret, `{"prompt":"x","stream":true}`, 400, "unsupported_parameter", "stream"},
		{"an unknown model", anyPolicy, secret, `{"model":"nope/x","prompt":"x"}`, 404, "model_not_found", "model"},
		{"a flag-shaped model", anyPolicy, secret, `{"model":"codex/--dangerously-bypass","prompt":"x"}`, 404, "model_not_found", "model"},
		{"a chat model", anyPolicy, secret, `{"model":"claude/default","prompt":"x"}`, 400, "invalid_value", "model"},
		{"a runtime that is not an image runtime", anyPolicy, secret, `{"model":"hermes","prompt":"x"}`, 400, "invalid_value", "model"},
		{"auto is not set up", anyPolicy, secret, `{"model":"auto","prompt":"x"}`, 404, "model_not_found", "model"},
		{"nothing allowed", chatOnly, secret, `{"prompt":"x"}`, 403, "policy_denied", ""},
		{"codex above a chat-only policy", chatOnly, secret, `{"model":"codex","prompt":"x"}`, 403, "policy_denied", ""},
		{"antigravity above a sandboxed policy", sandboxed, secret, `{"model":"antigravity","prompt":"x"}`, 403, "policy_denied", ""},
		{"a context key, held to chat-only", anyPolicy, ctxKey, `{"prompt":"x"}`, 403, "policy_denied", ""},
		{"a context key naming codex", anyPolicy, ctxKey, `{"model":"codex","prompt":"x"}`, 403, "policy_denied", ""},
	} {
		rec := postImages(h, c.policy, c.secret, c.body)
		e := decodeErrorBody(t, rec)
		param, _ := e["param"].(string)
		if rec.Code != c.status || e["code"] != c.code || param != c.param {
			t.Errorf("%s: status %d code %v param %q, want %d %s %q (%v)", c.name, rec.Code, e["code"], param, c.status, c.code, c.param, e["message"])
		}
		if rec.Header().Get("X-Request-Id") == "" {
			t.Errorf("%s: no X-Request-Id", c.name)
		}
	}
	if n := spawned.Load(); n != 1 {
		t.Fatalf("%d turns were spawned, want only the one that holds the slot: requests that had to be refused first start nothing", n)
	}
	close(release)
	if code := <-holder; code != http.StatusOK {
		t.Errorf("the request that held the slot: %d", code)
	}
}

// What the runtime saved decides first: with images in the folder it is a success, whatever
// the reply says. With none, a reply that is the marker on a line of its own is "no image
// tool" (400, nothing to be done by retrying), and anything else is a turn that made no image
// (502, with what the runtime said).
func TestImagesReplyHandling(t *testing.T) {
	long := strings.Repeat("a long and rambling reply ", 40)
	for name, c := range map[string]struct {
		reply  string
		files  map[string][]byte
		status int
		code   string
		in     []string // what the message holds
		out    []string // what it must not
	}{
		"no image tool":                      {"NO_IMAGE_TOOL", nil, 400, "image_generation_unsupported", nil, nil},
		"no image tool on a line of its own": {"I am sorry zqreply.\nNO_IMAGE_TOOL\n", nil, 400, "image_generation_unsupported", nil, nil},
		"no image tool, padded":              {"  NO_IMAGE_TOOL \t\n", nil, 400, "image_generation_unsupported", nil, nil},
		// Only the marker on a line of its own says it: a sentence that holds it, or a file name
		// that starts with it, is what the runtime said, and the client is shown it.
		"the marker at the end of a sentence": {"I am sorry zqreply. NO_IMAGE_TOOL.", nil, 502, "image_generation_failed", []string{"I am sorry zqreply. NO_IMAGE_TOOL."}, nil},
		"the marker in a file name":           {"NO_IMAGE_TOOL.png", nil, 502, "image_generation_failed", []string{"NO_IMAGE_TOOL.png"}, nil},
		// Images win: a runtime that made one did not lack the tool.
		"images win over the marker":            {"NO_IMAGE_TOOL", onePNG("drawn.png"), 200, "", nil, nil},
		"images win over a mention of it":       {"Saved a.png zqreply (no need to reply NO_IMAGE_TOOL)", onePNG("a.png"), 200, "", nil, nil},
		"images win, one of them named like it": {"NO_IMAGE_TOOL.png", onePNG("NO_IMAGE_TOOL.png"), 200, "", nil, nil},

		"nothing saved":                  {"I could not save the image zqreply, the tool failed.", nil, 502, "image_generation_failed", []string{"the tool failed"}, nil},
		"nothing saved and nothing said": {"", nil, 502, "image_generation_failed", nil, nil},
		"only files that are no image":   {"done: notes.txt zqreply", map[string][]byte{"notes.txt": []byte("not a picture")}, 502, "image_generation_failed", []string{"done: notes.txt zqreply"}, []string{"not a picture"}},
		"a reply of many lines":          {"line one\n\n  line\ttwo\x00\x1b[31m red\n", nil, 502, "image_generation_failed", []string{"line one line two[31m red"}, []string{"\n", "\x00", "\x1b"}},
		"a long reply":                   {long, nil, 502, "image_generation_failed", nil, []string{long}},
	} {
		h := newHarness(t, imageTurn(c.reply, c.files))
		rec := postImages(h, anyPolicy, h.key(t, "default", "app", false), `{"prompt":"a secret prompt"}`)
		// Neither the prompt nor what the runtime answered is for the log.
		defer func() {
			for _, line := range h.logged() {
				if strings.Contains(line, "secret prompt") || strings.Contains(line, "zqreply") || strings.Contains(line, "tool failed") || strings.Contains(line, "notes.txt") {
					t.Errorf("%s: the log holds the prompt or the reply: %q", name, line)
				}
			}
		}()
		if c.status == http.StatusOK {
			if rec.Code != http.StatusOK {
				t.Errorf("%s: %d %s, want the images", name, rec.Code, rec.Body)
				continue
			}
			if _, images := decodeImages(t, rec); len(images) != 1 {
				t.Errorf("%s: %d images, want 1", name, len(images))
			}
			continue
		}
		e := decodeErrorBody(t, rec)
		msg, _ := e["message"].(string)
		if rec.Code != c.status || e["code"] != c.code {
			t.Errorf("%s: %d %v, want %d %s", name, rec.Code, e, c.status, c.code)
			continue
		}
		wantType := "invalid_request_error"
		if c.status == 502 {
			wantType = "api_error"
		}
		if e["type"] != wantType {
			t.Errorf("%s: type %v, want %s", name, e["type"], wantType)
		}
		for _, s := range c.in {
			if !strings.Contains(msg, s) {
				t.Errorf("%s: %q must hold %q", name, msg, s)
			}
		}
		for _, s := range c.out {
			if strings.Contains(msg, s) {
				t.Errorf("%s: %q must not hold %q", name, msg, s)
			}
		}
		if c.status == 502 && len([]rune(msg)) > maxRuntimeWords+120 {
			t.Errorf("%s: the message is %d characters: the runtime's words are cut to %d", name, len([]rune(msg)), maxRuntimeWords)
		}
	}
}

// What was left out is told to the operator by reason, not by name.
func TestImagesFailureLogSaysWhyFilesWereLeftOut(t *testing.T) {
	h := newHarness(t, imageTurn("done", map[string][]byte{"notes.txt": []byte("x"), "more.txt": []byte("y")}))
	postImages(h, anyPolicy, h.key(t, "default", "app", false), `{"prompt":"p"}`)
	lines := strings.Join(h.logged(), "\n")
	if !strings.Contains(lines, "status=502") || !strings.Contains(lines, "not an image x2") || strings.Contains(lines, "notes.txt") {
		t.Errorf("the log line must say what was left out, by reason and count: %q", lines)
	}
}

// A refused request names the model it was refused for in its log line, as chat's does:
// the operator reading it sees what was asked for. Without a model of its own to name
// (an unknown one, or none given) there is none.
func TestImagesRefusalsLogTheModel(t *testing.T) {
	for name, c := range map[string]struct {
		policy Policy
		body   string
		status string
		model  string
	}{
		"a model above the policy":        {Policy{Max: ChatOnly}, `{"model":"codex","prompt":"x"}`, "status=403", "model=codex/default "},
		"a model that cannot make images": {anyPolicy, `{"model":"claude","prompt":"x"}`, "status=400", "model=claude/default "},
		"auto, which is not set up":       {anyPolicy, `{"model":"auto","prompt":"x"}`, "status=404", "model=auto "},
		"an unknown model":                {anyPolicy, `{"model":"nope/x","prompt":"x"}`, "status=404", "model= "},
		"no model, and none allowed":      {Policy{Max: ChatOnly}, `{"prompt":"x"}`, "status=403", "model= "},
	} {
		h := newHarness(t, imageTurn("a.png", onePNG("a.png")))
		postImages(h, c.policy, h.key(t, "default", "app", false), c.body)
		lines := h.logged()
		if len(lines) != 1 || !strings.Contains(lines[0], c.status) || !strings.Contains(lines[0], c.model) {
			t.Errorf("%s: the log is %q, want it to hold %q and %q", name, lines, c.status, c.model)
		}
	}
}

// What the collection left out is told to the operator whether or not an image was
// found: by reason and count, never by name. A runtime that makes an image and a mess
// is how an operator finds out about the mess.
func TestImagesSuccessLogSaysWhatWasLeftOut(t *testing.T) {
	// The collection looks at files by name and stops at the n it was asked for, so these come first.
	h := newHarness(t, imageTurn("a.png", map[string][]byte{"a.png": pngBytes, "0-notes.txt": []byte("x"), "1-more-notes.txt": []byte("y")}))
	rec := postImages(h, anyPolicy, h.key(t, "default", "app", false), `{"prompt":"p"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	lines := h.logged()
	if len(lines) != 1 || !strings.Contains(lines[0], "status=200") || !strings.Contains(lines[0], "not an image x2") || strings.Contains(lines[0], "notes") {
		t.Errorf("the log line of a success must say what was left out, by reason and count: %q", lines)
	}
	// Nothing left out, nothing said.
	h = newHarness(t, imageTurn("a.png", onePNG("a.png")))
	postImages(h, anyPolicy, h.key(t, "default", "app", false), `{"prompt":"p"}`)
	if lines := h.logged(); len(lines) != 1 || strings.Contains(lines[0], "detail=") {
		t.Errorf("a success that left nothing out has no detail: %q", lines)
	}
}

// The request leaves one line, as chat's does, with the key, the profile, the model
// and the outcome, and never the prompt or the image.
func TestImagesLogLine(t *testing.T) {
	h := newHarness(t, imageTurn("a.png", onePNG("a.png")))
	secret := h.key(t, "alice", "app", false)
	rec := postImages(h, anyPolicy, secret, `{"model":"agy","prompt":"a very private prompt"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	lines := h.logged()
	if len(lines) != 1 {
		t.Fatalf("%d log lines, want one: %q", len(lines), lines)
	}
	for _, want := range []string{"req=" + rec.Header().Get("X-Request-Id"), "profile=alice", "model=antigravity/default", "status=200", "ms="} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("%q must hold %q", lines[0], want)
		}
	}
	if strings.Contains(lines[0], "private") || strings.Contains(lines[0], base64.StdEncoding.EncodeToString(pngBytes)[:12]) {
		t.Errorf("the log holds the prompt or the image: %q", lines[0])
	}
}

// Nothing one request leaves can reach the next: not its image in the answer, and not its
// files in the folder the next runtime starts in. Both requests work in the same slot
// folder, which is emptied when a turn is over and, for what was written since, again
// before the next one starts.
func TestImagesOneRequestsFilesNeverReachTheNext(t *testing.T) {
	var turns atomic.Int32
	var found [][]string // what the runtime of each turn found in its working folder when it started
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		found = append(found, besidesTmp(o.Cwd))
		if turns.Add(1) == 1 {
			_ = os.WriteFile(filepath.Join(o.Cwd, "left-behind.png"), pngBytes, 0o600)
			return imageTurn("a.png", onePNG("a.png"))(ctx, o, onEvent)
		}
		return imageTurn("I made nothing", nil)(ctx, o, onEvent)
	}, func(_ *Deps, c *Config) { c.MaxConcurrent = 1 })
	secret := h.key(t, "default", "app", false)
	slot := filepath.Join(h.scratch, profileFolder("default"), "slot-0")

	if rec := postImages(h, anyPolicy, secret, `{"prompt":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("the first request: %d %s", rec.Code, rec.Body)
	}
	if entries, _ := os.ReadDir(slot); len(entries) != 0 {
		t.Fatalf("the folder must be empty when a turn is over: %d entries", len(entries))
	}
	// A process of the first turn that is still running writes again, after the turn is over.
	if err := os.WriteFile(filepath.Join(slot, "late.png"), pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(slot, "late-folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if rec := postImages(h, anyPolicy, secret, `{"prompt":"x"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("the second request: %d, want a 502: it must not be answered with the first one's image: %s", rec.Code, rec.Body)
	}
	for i, names := range found {
		if len(names) != 1 || !regexp.MustCompile(`^out-[a-z2-7]{16}$`).MatchString(names[0]) {
			t.Errorf("turn %d started among %v, want its own output folder and nothing else", i+1, names)
		}
	}
}

func TestImagesReportUsage(t *testing.T) {
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		res, err := imageTurn("image-1.png", onePNG("image-1.png"))(ctx, o, onEvent)
		if res != nil {
			res.CostUSD, res.HasCostUSD = 0.0123, true // as a runtime that reports a cost leaves it
		}
		return res, err
	})
	secret := h.key(t, "default", "app", false)
	rec := postImages(h, anyPolicy, secret, `{"model":"codex","prompt":"x"}`)
	var out struct{ Usage *usage }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Usage == nil || out.Usage.PromptTokens != 40000 || out.Usage.CompletionTokens != 500 ||
		out.Usage.TotalTokens != 40500 || out.Usage.CostUSD == nil || *out.Usage.CostUSD != 0.0123 {
		t.Fatalf("usage in %s (%v)", rec.Body, err)
	}
}
