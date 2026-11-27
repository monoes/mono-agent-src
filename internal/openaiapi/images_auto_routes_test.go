package openaiapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

const autoImage = `{"model":"auto","prompt":"a red circle on a white background","n":2,"size":"1024x1024"}`

// Until the operator lets auto pick among models that can write files, an image
// request for auto has no candidates: the 404 says what to raise, and nothing runs
// and Jev is not asked.
func TestImagesAutoHasNoCandidatesUntilTheOperatorGrantsMore(t *testing.T) {
	log, f := &execLog{}, &fakeAuto{id: "codex/default", p: 1}
	h := newHarness(t, log.imageExec("a.png", onePNG("a.png")), func(d *Deps, _ *Config) { d.Auto = f.funcs() })
	secret := h.key(t, "default", "app", false)

	rec := postImages(h, anyPolicy, secret, autoImage)
	e := decodeErrorBody(t, rec)
	msg, _ := e["message"].(string)
	if rec.Code != http.StatusNotFound || e["code"] != "model_not_found" || e["param"] != "model" ||
		!strings.Contains(msg, "--auto-confinement") || !strings.Contains(msg, "sandboxed") {
		t.Fatalf("%d %v: want a 404 model_not_found that says to raise --auto-confinement to sandboxed", rec.Code, e)
	}
	if log.count() != 0 || f.asked != 0 {
		t.Errorf("%d turns ran and Jev was asked %d times for a request that had no candidates", log.count(), f.asked)
	}

	// A model named by the client is not auto's to hold back: the operator's permission is for auto.
	if rec := postImages(h, anyPolicy, secret, `{"model":"codex","prompt":"x"}`); rec.Code != http.StatusOK {
		t.Errorf("a named model: %d %s", rec.Code, rec.Body)
	}
}

// Where the operator allows it, Jev picks among the image models, and only those: the
// pick runs, the headers and the log say who chose, and Jev saw the prompt, not the
// instructions the gateway adds.
func TestImagesAutoRunsTheImageModelJevPicks(t *testing.T) {
	log, f := &execLog{}, &fakeAuto{id: "antigravity/gemini-3.8-flash-high", p: 0.9}
	h := newHarness(t, log.imageExec("a.png", onePNG("a.png")), func(d *Deps, _ *Config) { d.Auto = f.funcs() })
	secret := h.key(t, "alice", "app", false)

	rec := postImages(h, autoAnyPolicy, secret, autoImage)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, images := decodeImages(t, rec); !equalImages(images, pngBytes) {
		t.Errorf("got %d images", len(images))
	}
	if rec.Header().Get("X-Monoagent-Model") != "antigravity/gemini-3.8-flash-high" || rec.Header().Get("X-Monoagent-Auto") != "jev" {
		t.Errorf("headers: model %q auto %q", rec.Header().Get("X-Monoagent-Model"), rec.Header().Get("X-Monoagent-Auto"))
	}
	if log.count() != 1 || log.opts[0].Runtime != "antigravity" || log.opts[0].Model != "gemini-3.8-flash-high" || log.opts[0].RequireSandbox {
		t.Errorf("the runner ran %+v, want one unconfined turn of antigravity/gemini-3.8-flash-high", log.opts)
	}
	if log.opts[0].Prompt != "a red circle on a white background\n\nCreate 2 distinct images. Preferred size: 1024x1024." {
		t.Errorf("the turn is told %q", log.opts[0].Prompt)
	}
	wantImageSystemPrompt(t, log.opts[0])
	offered := make([]string, 0, len(f.options))
	for id := range f.options {
		offered = append(offered, id)
	}
	slices.Sort(offered)
	if want := []string{"antigravity/default", "antigravity/gemini-3.8-flash-high", "codex/default", "codex/gpt-6-astra"}; !slices.Equal(offered, want) {
		t.Errorf("Jev was offered %v, want the image models and nothing else: %v", offered, want)
	}
	if f.prompt != "a red circle on a white background" || f.chooseProfile != "alice" {
		t.Errorf("Jev was sent %q for profile %q: the client's prompt, for the key's profile", f.prompt, f.chooseProfile)
	}
	lines := strings.Join(h.logged(), "\n")
	if !strings.Contains(lines, "model=antigravity/gemini-3.8-flash-high") || !strings.Contains(lines, "auto=jev") || strings.Contains(lines, "red circle") {
		t.Errorf("the log line must say what ran and who chose, and never the prompt: %q", lines)
	}
}

// With sandboxed granted to auto, codex is the only image model it may pick, and the
// turn requires the sandbox.
func TestImagesAutoWithSandboxedGrantedPicksOnlyCodex(t *testing.T) {
	log, f := &execLog{}, &fakeAuto{id: "antigravity/default", p: 1} // never offered
	h := newHarness(t, log.imageExec("a.png", onePNG("a.png")), func(d *Deps, _ *Config) { d.Auto = f.funcs() })
	rec := postImages(h, Policy{Max: Unconfined, AutoMax: Sandboxed}, h.key(t, "default", "app", false), autoImage)
	if rec.Code != http.StatusOK || log.opts[0].Runtime != "codex" || !log.opts[0].RequireSandbox || rec.Header().Get("X-Monoagent-Auto") != "rule" {
		t.Fatalf("%d: ran %+v auto=%q: a pick that was not offered must not run", rec.Code, log.opts, rec.Header().Get("X-Monoagent-Auto"))
	}
}

// When Jev gives no answer the rule picks, as for chat: the most confined model first.
func TestImagesAutoFallsBackToTheRule(t *testing.T) {
	log, f := &execLog{}, &fakeAuto{err: errors.New("jev is down")}
	h := newHarness(t, log.imageExec("a.png", onePNG("a.png")), func(d *Deps, _ *Config) { d.Auto = f.funcs() })
	rec := postImages(h, autoAnyPolicy, h.key(t, "default", "app", false), autoImage)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Monoagent-Auto") != "rule" || rec.Header().Get("X-Monoagent-Model") != "codex/default" {
		t.Fatalf("%d auto=%q model=%q, want the rule's pick codex/default", rec.Code, rec.Header().Get("X-Monoagent-Auto"), rec.Header().Get("X-Monoagent-Model"))
	}
}

// A key created with --context is held to the context cap through auto as well.
func TestImagesAutoForAContextKeyFollowsTheContextCap(t *testing.T) {
	log, f := &execLog{}, &fakeAuto{id: "codex/default", p: 1}
	h := newHarness(t, log.imageExec("a.png", onePNG("a.png")), func(d *Deps, _ *Config) { d.Auto = f.funcs() })
	ctxKey := h.key(t, "default", "notes", true)

	rec := postImages(h, autoAnyPolicy, ctxKey, autoImage)
	if msg, _ := decodeErrorBody(t, rec)["message"].(string); rec.Code != http.StatusNotFound || !strings.Contains(msg, "--context-confinement") {
		t.Errorf("a context key held to chat-only: %d %s", rec.Code, rec.Body)
	}
	raised := Policy{Max: Unconfined, ContextMax: Sandboxed, AutoMax: Unconfined}
	if rec := postImages(h, raised, ctxKey, autoImage); rec.Code != http.StatusOK || log.opts[0].Runtime != "codex" {
		t.Errorf("raised to sandboxed: %d, ran %+v", rec.Code, log.opts)
	}
}

// A request that would be refused for lack of a slot never costs a Jev call.
func TestImagesAutoDoesNotAskJevWhenTheServerIsBusy(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	f := &fakeAuto{id: "codex/default", p: 1}
	var spawned atomic.Int32
	h := newHarness(t, func(ctx context.Context, o monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		if spawned.Add(1) == 1 {
			started <- struct{}{}
			<-release
		}
		return imageTurn("a.png", onePNG("a.png"))(ctx, o, onEvent)
	}, func(d *Deps, c *Config) { d.Auto = f.funcs(); c.MaxConcurrent = 1 })
	secret := h.key(t, "default", "app", false)

	done := make(chan int, 1)
	go func() {
		done <- postImages(h, autoAnyPolicy, secret, `{"model":"codex","prompt":"hold the slot"}`).Code
	}()
	waitStarted(t, started)
	rec := postImages(h, autoAnyPolicy, secret, autoImage)
	if rec.Code != http.StatusTooManyRequests || f.asked != 0 {
		t.Errorf("a busy server: status %d, Jev asked %d times: want 429 and none", rec.Code, f.asked)
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Errorf("the request that held the slot: %d", code)
	}
}

// A client that leaves while Jev is asked is not a Jev failure: nothing runs, and the
// log neither blames Jev nor says who chose.
func TestImagesAutoClientLeavingDuringTheQuestionRunsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &execLog{}
	h := newHarness(t, log.imageExec("a.png", onePNG("a.png")), func(d *Deps, _ *Config) {
		d.Auto = AutoFuncs{
			Status: func(context.Context, string) AutoStatus { return AutoStatus{Available: true} },
			Choose: func(c context.Context, _, _ string, _ map[string]string) (string, float64, float64, error) {
				cancel() // the client hangs up
				<-c.Done()
				return "", 0, 0, c.Err()
			},
		}
	})
	secret := h.key(t, "default", "app", false)

	r := httptest.NewRequest(http.MethodPost, imagesURL, strings.NewReader(autoImage)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+secret)
	h.do(autoAnyPolicy, r)

	lines := strings.Join(h.logged(), "\n")
	if log.count() != 0 {
		t.Errorf("%d turns ran for a client that had left", log.count())
	}
	if !strings.Contains(lines, "status=499") || strings.Contains(lines, "did not decide") || strings.Contains(lines, "auto=") {
		t.Errorf("want a 499 that blames nobody and names no chooser: %q", lines)
	}
}
