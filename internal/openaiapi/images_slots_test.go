package openaiapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// The sandbox a model's class depends on is required for a sandboxed model, so that
// a sandbox that cannot be applied refuses the turn instead of running it
// unconfined; an unconfined model has none to require.
func TestImagesRequireTheSandboxOnlyOfASandboxedModel(t *testing.T) {
	log := &execLog{}
	h := newHarness(t, log.imageExec("a.png", onePNG("a.png")))
	secret := h.key(t, "default", "app", false)
	for _, model := range []string{"codex", "antigravity"} {
		if rec := postImages(h, anyPolicy, secret, `{"model":"`+model+`","prompt":"x"}`); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body)
		}
	}
	if log.count() != 2 || !log.opts[0].RequireSandbox || log.opts[1].RequireSandbox {
		t.Errorf("codex is sandboxed and antigravity is not: %v", []bool{log.opts[0].RequireSandbox, log.opts[1].RequireSandbox})
	}
}

// The class is checked at the start of the turn against the policy of the key: for a
// key created with --context that is the context maximum, not the listener's.
func TestImagesATurnIsHeldToTheContextCapWhenItStarts(t *testing.T) {
	h := newHarness(t, scriptedExec(evStart(false, "none"), evText("a.png"), evResult("a.png", monomind.StopEndTurn), evDone(0)))
	raised := Policy{Max: Unconfined, ContextMax: Sandboxed}
	// codex is classified sandboxed, and reports no confinement at all when it starts.
	if rec := postImages(h, raised, h.key(t, "default", "notes", true), `{"model":"codex","prompt":"x"}`); rec.Code != http.StatusForbidden || decodeErrorBody(t, rec)["code"] != "policy_denied" {
		t.Errorf("a context key: %d %s", rec.Code, rec.Body)
	}
	// The same turn under a plain key is the listener's to judge, and the listener serves everything.
	if rec := postImages(h, raised, h.key(t, "default", "app", false), `{"model":"codex","prompt":"x"}`); rec.Code == http.StatusForbidden {
		t.Errorf("a plain key was held to the context cap: %s", rec.Body)
	}
}

// Two requests that run together work in different folders, or each would empty the
// other's files.
func TestImagesRequestsAtTheSameTimeWorkInDifferentSlotFolders(t *testing.T) {
	var mu sync.Mutex
	var cwds []string
	var started atomic.Int32
	barrier := make(chan struct{})
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		mu.Lock()
		cwds = append(cwds, o.Cwd)
		mu.Unlock()
		if started.Add(1) == 2 {
			close(barrier)
		}
		select {
		case <-barrier:
		case <-time.After(5 * time.Second):
		}
		return imageTurn("a.png", onePNG("a.png"))(ctx, o, onEvent)
	})
	secret := h.key(t, "default", "app", false)

	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- postImages(h, anyPolicy, secret, `{"prompt":"x"}`).Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusOK {
			t.Errorf("status %d", code)
		}
	}
	if len(cwds) != 2 || cwds[0] == cwds[1] {
		t.Errorf("two turns at once must not share a folder: %v", cwds)
	}
}

// A slot is given back when the request ends, whatever way it ends: after each ending
// the next request is served by the one slot there is.
func TestImagesGiveTheSlotBack(t *testing.T) {
	var next execFunc
	var leave context.CancelFunc // the client that leaves while Jev is asked
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		return next(ctx, o, onEvent)
	}, func(d *Deps, c *Config) {
		c.MaxConcurrent = 1
		d.Auto = AutoFuncs{
			Status: func(context.Context, string) AutoStatus { return AutoStatus{Available: true} },
			Choose: func(cx context.Context, _, _ string, _ map[string]string) (string, float64, float64, error) {
				leave()
				<-cx.Done()
				return "", 0, 0, cx.Err()
			},
		}
	})
	secret := h.key(t, "default", "app", false)
	success := func(when string) {
		t.Helper()
		next = imageTurn("a.png", onePNG("a.png"))
		if rec := postImages(h, anyPolicy, secret, `{"prompt":"x"}`); rec.Code != http.StatusOK {
			t.Fatalf("%s: the next request got %d %s: the slot was not given back", when, rec.Code, rec.Body)
		}
	}
	success("at the start")

	for name, c := range map[string]struct {
		exec   execFunc
		status int
	}{
		"a quota":                   {scriptedExec(evStart(false, "workspace-write"), evError(monomind.ErrQuota, "usage limit reached"), evDone(1)), 429},
		"a turn that made no image": {imageTurn("nothing was made", nil), 502},
		"no image tool":             {imageTurn("NO_IMAGE_TOOL", nil), 400},
		"a turn that did not start": {func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
			return nil, monomind.ErrSandboxRequired
		}, 403},
	} {
		next = c.exec
		if rec := postImages(h, anyPolicy, secret, `{"prompt":"x"}`); rec.Code != c.status {
			t.Fatalf("%s: %d %s, want %d", name, rec.Code, rec.Body, c.status)
		}
		success("after " + name)
	}

	// A client that leaves while the turn runs.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	next = func(c context.Context, o monomind.ExecOptions, _ func(monomind.Event)) (*monomind.TurnResult, error) {
		cancel()
		<-c.Done()
		return &monomind.TurnResult{SawDone: true, Err: &monomind.ProtocolError{Code: monomind.ErrCancelled, Message: "cancelled"}}, nil
	}
	r := httptest.NewRequest(http.MethodPost, imagesURL, strings.NewReader(`{"prompt":"x"}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+secret)
	h.do(anyPolicy, r)
	success("after a client that left")

	// A client that leaves while Jev is asked, before any turn has started.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	leave = cancel
	r = httptest.NewRequest(http.MethodPost, imagesURL, strings.NewReader(autoImage)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+secret)
	h.do(autoAnyPolicy, r)
	success("after a client that left while Jev was asked")
}

// An image turn takes a minute: the write timeout a server sets (the legacy HTTP API's is
// 5 minutes) must not cut the answer to a turn that ran longer than it does, whether the
// answer is the images or a failure (the images get a write budget of their own, so it is
// the failure that needs the connection's deadline to have been moved). The read timeout is
// not tested: net/http stops it when the request body has been read, which is before the
// turn starts, and the gateway does nothing about it.
func TestImagesResponseOutlivesTheServersWriteTimeout(t *testing.T) {
	var turns atomic.Int32
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		time.Sleep(900 * time.Millisecond)
		if ctx.Err() != nil { // the request was cut while the turn ran
			return nil, ctx.Err()
		}
		if turns.Add(1) == 1 {
			return imageTurn("a.png", onePNG("a.png"))(ctx, o, onEvent)
		}
		return imageTurn("nothing was made", nil)(ctx, o, onEvent)
	})
	mux := http.NewServeMux()
	h.g.Mount(mux, anyPolicy)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.WriteTimeout = 400 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	secret := h.key(t, "default", "app", false)

	for _, want := range []struct { // in this order: the first turn makes the images
		name   string
		status int
		in     string
	}{{"the images", http.StatusOK, `"b64_json"`}, {"a failure", http.StatusBadGateway, "image_generation_failed"}} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+imagesURL, strings.NewReader(`{"prompt":"x"}`))
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: a timeout of the server cut the response: %v", want.name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want.status || !strings.Contains(string(body), want.in) {
			t.Fatalf("%s: status %d body %s", want.name, resp.StatusCode, body)
		}
	}
}

// The turn's folder is handed to Collect once, after a turn that ended cleanly and as
// the turn left it, and is emptied after that.
func TestRunTurnHandsTheFolderToCollectBeforeItIsEmptied(t *testing.T) {
	h := newHarness(t, imageTurn("a.png", onePNG("a.png")))
	var dir string
	var calls int
	var then []string
	_, err := h.g.runTurn(context.Background(), turn{Runtime: "codex", Model: "default", Prompt: "p", Policy: anyPolicy, ProfileID: "alice",
		Collect: func(_ context.Context, d string) { calls, dir, then = calls+1, d, besidesTmp(d) }})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(then) != 1 || then[0] != "a.png" {
		t.Fatalf("Collect ran %d times and saw %v: it must run once, with the file the turn saved", calls, then)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the folder must be emptied after Collect: %d entries", len(entries))
	}
}

// A turn that is given an output folder finds it in its working folder when it starts:
// empty, and for its owner alone. It goes with the rest when the turn is over. A turn
// that is given none finds none.
func TestRunTurnMakesTheOutputFolderBeforeTheTurnStarts(t *testing.T) {
	var cwd string
	var mode os.FileMode
	var inside, beside []string
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		cwd = o.Cwd
		mode, inside = 0, nil
		if fi, err := os.Lstat(filepath.Join(o.Cwd, "out-abc")); err == nil && fi.IsDir() {
			mode = fi.Mode().Perm()
		}
		entries, _ := os.ReadDir(filepath.Join(o.Cwd, "out-abc"))
		for _, e := range entries {
			inside = append(inside, e.Name())
		}
		beside = besidesTmp(o.Cwd)
		return okTurn("x")(ctx, o, onEvent)
	})
	if _, err := h.g.runTurn(context.Background(), turn{Runtime: "claude", Model: "default", Prompt: "p", Policy: anyPolicy, ProfileID: "alice", Subdir: "out-abc"}); err != nil {
		t.Fatal(err)
	}
	if mode != 0o700 || len(inside) != 0 || !slices.Equal(beside, []string{"out-abc"}) {
		t.Errorf("the turn found a folder of mode %v holding %v, and %v in its working folder: want an empty out-abc for its owner alone", mode, inside, beside)
	}
	if left := besidesTmp(cwd); len(left) != 0 {
		t.Errorf("the output folder must go with the rest of the turn's files: %v", left)
	}

	if _, err := h.g.runTurn(context.Background(), turn{Runtime: "claude", Model: "default", Prompt: "p", Policy: anyPolicy, ProfileID: "alice"}); err != nil {
		t.Fatal(err)
	}
	if len(beside) != 0 {
		t.Errorf("a turn that was given no output folder found %v", beside)
	}
}

// The name of the output folder is a name: one that leads out of the working folder, or
// onto the turn's temp folder, is refused before anything runs.
func TestRunTurnRefusesAnOutputFolderThatIsNotAPlainName(t *testing.T) {
	var ran atomic.Bool
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		ran.Store(true)
		return okTurn("x")(ctx, o, onEvent)
	})
	for _, name := range []string{"../out", "a/b", ".", "..", turnTmpName, "/tmp/out"} {
		_, err := h.g.runTurn(context.Background(), turn{Runtime: "claude", Model: "default", Prompt: "p", Policy: anyPolicy, ProfileID: "alice", Subdir: name})
		if err == nil || ran.Load() {
			t.Errorf("%q: error %v, the turn ran: %v: a name that is not a plain one is refused", name, err, ran.Load())
		}
	}
}

// What a turn that did not end cleanly left is nobody's to read.
func TestRunTurnDoesNotCollectAfterATurnThatDidNotEndCleanly(t *testing.T) {
	for name, c := range map[string]struct {
		exec   execFunc
		policy Policy
	}{
		"Exec failed to start": {func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
			return nil, errors.New("boom")
		}, anyPolicy},
		"the turn reported an error":              {scriptedExec(evStart(false, "workspace-write"), evError(monomind.ErrQuota, "limit"), evDone(1)), anyPolicy},
		"the turn never said it was done":         {scriptedExec(evStart(false, "workspace-write"), evText("x")), anyPolicy},
		"the turn started weaker than the policy": {scriptedExec(evStart(false, "none"), evText("x"), evResult("x", monomind.StopEndTurn), evDone(0)), Policy{Max: Sandboxed}},
	} {
		h := newHarness(t, c.exec)
		collected := false
		_, _ = h.g.runTurn(context.Background(), turn{Runtime: "codex", Model: "default", Prompt: "p", Policy: c.policy, Collect: func(context.Context, string) { collected = true }})
		if collected {
			t.Errorf("%s: the folder was handed to Collect", name)
		}
	}
	// A turn that no one asked to collect from is run as it always was.
	h := newHarness(t, okTurn("x"))
	if _, err := h.g.runTurn(context.Background(), turn{Runtime: "claude", Model: "default", Prompt: "p", Policy: anyPolicy}); err != nil {
		t.Fatal(err)
	}
}
