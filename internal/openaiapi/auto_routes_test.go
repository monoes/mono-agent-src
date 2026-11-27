package openaiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

const (
	autoChat    = `{"model":"auto","messages":[{"role":"user","content":"write a haiku"}]}`
	autoChatURL = "/v1/chat/completions"
)

// autoAnyPolicy serves every class and lets auto pick among all of them: what an
// operator has when they raised --auto-confinement on purpose.
var autoAnyPolicy = Policy{Max: Unconfined, AutoMax: Unconfined}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("not the JSON that was expected: %v\n%s", err, rec.Body)
	}
}

// execLog records the options of every turn the fake runner is asked to run.
type execLog struct {
	mu   sync.Mutex
	opts []monomind.ExecOptions
}

func (l *execLog) exec(reply string) execFunc {
	return func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		l.mu.Lock()
		l.opts = append(l.opts, opts)
		l.mu.Unlock()
		return okTurn(reply)(ctx, opts, onEvent)
	}
}

func (l *execLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.opts)
}

// Without a Jev key, or with the surface off, or on a server built without Jev,
// auto is not a model: not listed, not retrievable, not accepted, and the 404 says
// what is missing. Nothing runs and nothing is asked.
func TestAutoIsNotOfferedWithoutJev(t *testing.T) {
	for name, st := range map[string]struct {
		status  func(context.Context, string) AutoStatus
		mention string
	}{
		"no key": {func(context.Context, string) AutoStatus {
			return AutoStatus{Missing: "a Jev key for the profile (TYPESAFE_API_KEY)"}
		}, "a Jev key"},
		"surface off": {func(context.Context, string) AutoStatus {
			return AutoStatus{Missing: "the api_auto surface (monoagentcli jev enable api_auto)"}
		}, "jev enable api_auto"},
		"not wired": {nil, "not set up with"},
	} {
		log, f := &execLog{}, &fakeAuto{id: "claude/default", p: 1}
		h := newHarness(t, log.exec("x"), func(d *Deps, _ *Config) {
			if st.status != nil {
				d.Auto = f.funcs()
				d.Auto.Status = st.status
			}
		})
		secret := h.key(t, "default", "app", false)

		for _, m := range decodeModelList(t, h.serve(anyPolicy, http.MethodGet, "/v1/models", secret, "")).Data {
			if m.ID == "auto" {
				t.Errorf("%s: auto must not be listed", name)
			}
		}
		for what, rec := range map[string]*httptest.ResponseRecorder{
			"retrieve": h.serve(anyPolicy, http.MethodGet, "/v1/models/auto", secret, ""),
			"chat":     h.serve(anyPolicy, http.MethodPost, autoChatURL, secret, autoChat),
		} {
			e := decodeErrorBody(t, rec)
			msg, _ := e["message"].(string)
			if rec.Code != http.StatusNotFound || e["code"] != "model_not_found" || e["param"] != "model" || !strings.Contains(msg, st.mention) {
				t.Errorf("%s, %s: %d %v: want a 404 model_not_found that says %q", name, what, rec.Code, e, st.mention)
			}
		}
		if log.count() != 0 || f.asked != 0 {
			t.Errorf("%s: %d turns ran and Jev was asked %d times for a model that is not available", name, log.count(), f.asked)
		}
	}
}

// Where it works, auto is the last model of the list, so that a client that takes
// the first one is not moved to it (and its prompts to TypeSafe) by an operator
// switching the surface on. It is retrievable, and its confinement is the
// strongest class it picks within: the policy's, capped by what the operator let
// auto pick (chat-only unless they raised it).
func TestAutoIsListedLastWhenAvailable(t *testing.T) {
	h := autoGateway(t, &fakeAuto{id: "claude/default", p: 1})
	secret := h.key(t, "default", "app", false)

	for _, c := range []struct {
		policy Policy
		want   string
	}{
		{anyPolicy, "chat-only"}, // nothing granted to auto
		{Policy{Max: Unconfined, AutoMax: Sandboxed}, "sandboxed"},
		{autoAnyPolicy, "unconfined"},
		{Policy{Max: Sandboxed, AutoMax: Unconfined}, "sandboxed"}, // never above the listener
		{Policy{Max: ChatOnly, AutoMax: Unconfined}, "chat-only"},
	} {
		list := decodeModelList(t, h.serve(c.policy, http.MethodGet, "/v1/models", secret, ""))
		if len(list.Data) < 2 || list.Data[len(list.Data)-1].ID != "auto" || list.Data[0].ID == "auto" {
			t.Fatalf("policy %v: auto must come after the models: %+v", c.policy, list.Data)
		}
		a := list.Data[len(list.Data)-1]
		if a.Object != "model" || a.OwnedBy != "jev" || a.Monoagent.Runtime != "auto" || a.Monoagent.Confinement != c.want {
			t.Errorf("policy %v: %+v, want owned by jev, runtime auto, confinement %s", c.policy, a, c.want)
		}
	}
	rec := h.serve(anyPolicy, http.MethodGet, "/v1/models/auto", secret, "")
	var got modelObject
	decodeInto(t, rec, &got)
	if rec.Code != http.StatusOK || got.ID != "auto" {
		t.Errorf("retrieve: %d %s", rec.Code, rec.Body)
	}
}

// The answer is the pick's: the runner runs the runtime and model Jev chose,
// the completion and the headers name it, and Jev saw only the last user message.
func TestAutoRunsTheModelJevPicks(t *testing.T) {
	log, f := &execLog{}, &fakeAuto{id: "codex/gpt-6-astra", p: 0.9}
	h := newHarness(t, log.exec("hello"), func(d *Deps, _ *Config) { d.Auto = f.funcs() })
	secret := h.key(t, "alice", "app", false)

	body := `{"model":"auto","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"first question"},{"role":"assistant","content":"first answer"},{"role":"user","content":"write a haiku"}]}`
	rec := h.serve(autoAnyPolicy, http.MethodPost, autoChatURL, secret, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if log.count() != 1 || log.opts[0].Runtime != "codex" || log.opts[0].Model != "gpt-6-astra" {
		t.Errorf("the runner ran %+v, want one turn of codex/gpt-6-astra", log.opts)
	}
	if rec.Header().Get("X-Monoagent-Model") != "codex/gpt-6-astra" || rec.Header().Get("X-Monoagent-Auto") != "jev" {
		t.Errorf("headers: model %q auto %q", rec.Header().Get("X-Monoagent-Model"), rec.Header().Get("X-Monoagent-Auto"))
	}
	var got completion
	decodeInto(t, rec, &got)
	if got.Model != "codex/gpt-6-astra" || got.Choices[0].Message.Content != "hello" {
		t.Errorf("completion = %+v", got)
	}
	if f.prompt != "write a haiku" {
		t.Errorf("Jev was sent %q: only the last user message", f.prompt)
	}
	if f.chooseProfile != "alice" || f.thresholdProfile != "alice" {
		t.Errorf("the key's profile is alice, but the question was asked for %q and the threshold read for %q", f.chooseProfile, f.thresholdProfile)
	}
}

// Jev only picks among what the policy allows, and within what the operator let
// auto pick: under chat-only it is offered claude's models and nothing else, and a
// model it names that was not offered is not run. A key created with --context is
// held to the context maximum.
func TestAutoOffersOnlyWhatThePolicyAllows(t *testing.T) {
	for name, c := range map[string]struct {
		policy  Policy
		context bool
	}{
		"a chat-only server":  {Policy{Max: ChatOnly, AutoMax: Unconfined}, false},
		"a context key":       {Policy{Max: Unconfined, ContextMax: ChatOnly, AutoMax: Unconfined}, true},
		"a sandboxed ceiling": {Policy{Max: Sandboxed, AutoMax: Unconfined}, false},
		"nothing granted":     {anyPolicy, false},
	} {
		log, f := &execLog{}, &fakeAuto{id: "antigravity/gemini-3.8-flash-high", p: 1} // never offered
		h := newHarness(t, log.exec("x"), func(d *Deps, _ *Config) { d.Auto = f.funcs() })
		secret := h.key(t, "default", "app", c.context)

		rec := h.serve(c.policy, http.MethodPost, autoChatURL, secret, autoChat)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if f.asked == 0 {
			t.Fatalf("%s: Jev was not asked, so there are no options to check", name)
		}
		eff := c.policy
		if c.context {
			eff = c.policy.ForContextKey()
		}
		eff = eff.ForAuto()
		for id := range f.options {
			m, err := h.g.catalog.Resolve(context.Background(), id)
			if err != nil || !eff.Allows(m.Class) {
				t.Errorf("%s: Jev was offered %s, which the policy does not allow", name, id)
			}
		}
		if rec.Header().Get("X-Monoagent-Auto") != "rule" || log.opts[0].Runtime == "antigravity" {
			t.Errorf("%s: a model that was not offered must not run (auto=%q, ran %s)", name, rec.Header().Get("X-Monoagent-Auto"), log.opts[0].Runtime)
		}
	}
}

// Until the operator grants it more, auto picks among chat-only models, because a
// prompt can steer the pick and the author of a prompt need not hold the key: on a
// listener that serves every class, a runtime above chat-only is not an option, and
// a named model is not subject to this (the client chose it). --auto-confinement
// (AutoMax) is the permission, and never above the listener's own policy.
func TestAutoIsChatOnlyUntilTheOperatorGrantsMore(t *testing.T) {
	for _, c := range []struct {
		name    string
		policy  Policy
		offered []string // the runtimes Jev is offered, sorted
	}{
		{"nothing granted", anyPolicy, []string{"claude"}},
		{"sandboxed granted", Policy{Max: Unconfined, AutoMax: Sandboxed}, []string{"claude", "codex"}},
		{"everything granted", autoAnyPolicy, []string{"antigravity", "claude", "codex", "hermes"}},
		{"granted above a chat-only listener", Policy{Max: ChatOnly, AutoMax: Unconfined}, []string{"claude"}},
	} {
		log, f := &execLog{}, &fakeAuto{id: "claude/default", p: 1}
		h := newHarness(t, log.exec("x"), func(d *Deps, _ *Config) { d.Auto = f.funcs() })
		secret := h.key(t, "default", "app", false)

		if rec := h.serve(c.policy, http.MethodPost, autoChatURL, secret, autoChat); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		runtimes := map[string]bool{}
		for id := range f.options {
			runtimes[strings.SplitN(id, "/", 2)[0]] = true
		}
		var got []string
		for rt := range runtimes {
			got = append(got, rt)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(c.offered, ",") {
			t.Errorf("%s: Jev was offered %v, want models of %v", c.name, got, c.offered)
		}
	}

	// A model the client names itself is not auto's to refuse.
	log := &execLog{}
	h := newHarness(t, log.exec("x"), func(d *Deps, _ *Config) { d.Auto = (&fakeAuto{id: "claude/default", p: 1}).funcs() })
	secret := h.key(t, "default", "app", false)
	rec := h.serve(anyPolicy, http.MethodPost, autoChatURL, secret, `{"model":"antigravity","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK || log.count() != 1 || log.opts[0].Runtime != "antigravity" {
		t.Errorf("a named model: %d, ran %+v", rec.Code, log.opts)
	}
}

// A request that would be refused for lack of a slot never costs a Jev call.
func TestAutoDoesNotAskJevWhenTheServerIsBusy(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	f := &fakeAuto{id: "claude/default", p: 1}
	h := newHarness(t, func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error) {
		started <- struct{}{}
		<-release
		return okTurn("x")(ctx, opts, onEvent)
	}, func(d *Deps, c *Config) { d.Auto = f.funcs(); c.MaxConcurrent = 1 })
	secret := h.key(t, "default", "app", false)

	done := make(chan int, 1)
	go func() {
		done <- h.serve(anyPolicy, http.MethodPost, autoChatURL, secret, `{"model":"claude","messages":[{"role":"user","content":"hold the slot"}]}`).Code
	}()
	<-started
	rec := h.serve(anyPolicy, http.MethodPost, autoChatURL, secret, autoChat)
	if rec.Code != http.StatusTooManyRequests || f.asked != 0 {
		t.Errorf("a busy server: status %d, Jev asked %d times: want 429 and none", rec.Code, f.asked)
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Errorf("the request that held the slot: %d", code)
	}
}

// A streamed request names the pick the same way, and the log line says how it was
// chosen without ever carrying the prompt.
func TestAutoStreamsAndLogsWithoutThePrompt(t *testing.T) {
	log, f := &execLog{}, &fakeAuto{id: "codex/gpt-6-astra", p: 0.9}
	h := newHarness(t, log.exec("streamed"), func(d *Deps, _ *Config) { d.Auto = f.funcs() })
	secret := h.key(t, "default", "app", false)

	rec := h.serve(autoAnyPolicy, http.MethodPost, autoChatURL, secret, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"write a haiku"}]}`)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Monoagent-Auto") != "jev" || rec.Header().Get("X-Monoagent-Model") != "codex/gpt-6-astra" {
		t.Fatalf("%d, auto %q, model %q", rec.Code, rec.Header().Get("X-Monoagent-Auto"), rec.Header().Get("X-Monoagent-Model"))
	}
	if !strings.Contains(rec.Body.String(), `"model":"codex/gpt-6-astra"`) {
		t.Errorf("the chunks must name the model that answered: %s", rec.Body)
	}
	lines := strings.Join(h.logged(), "\n")
	if !strings.Contains(lines, "model=codex/gpt-6-astra") || !strings.Contains(lines, "auto=jev") {
		t.Errorf("the log line must say what was run and how it was chosen: %q", lines)
	}
	if strings.Contains(lines, "haiku") {
		t.Errorf("the log must never carry the prompt: %q", lines)
	}
}

// Availability is the profile of the key, not the server's.
func TestAutoAvailabilityIsPerProfile(t *testing.T) {
	var asked atomic.Value
	f := &fakeAuto{id: "claude/default", p: 1}
	h := newHarness(t, okTurn("x"), func(d *Deps, _ *Config) {
		d.Auto = f.funcs()
		d.Auto.Status = func(_ context.Context, profile string) AutoStatus {
			asked.Store(profile)
			return AutoStatus{Available: profile == "alice", Missing: "the api_auto surface for this profile"}
		}
	})
	alice, bob := h.key(t, "alice", "app", false), h.key(t, "bob", "app", false)

	if rec := h.serve(anyPolicy, http.MethodPost, autoChatURL, alice, autoChat); rec.Code != http.StatusOK {
		t.Errorf("alice has auto: %d %s", rec.Code, rec.Body)
	}
	if rec := h.serve(anyPolicy, http.MethodPost, autoChatURL, bob, autoChat); rec.Code != http.StatusNotFound || asked.Load() != "bob" {
		t.Errorf("bob does not: %d (Status asked about %v)", rec.Code, asked.Load())
	}
}

// A client that leaves while Jev is being asked is not a Jev failure: nothing
// runs, and the log neither blames Jev nor says who chose.
func TestAutoClientLeavingDuringTheQuestionRunsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &execLog{}
	h := newHarness(t, log.exec("x"), func(d *Deps, _ *Config) {
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

	r := httptest.NewRequest(http.MethodPost, autoChatURL, strings.NewReader(autoChat)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+secret)
	h.do(anyPolicy, r)

	lines := strings.Join(h.logged(), "\n")
	if log.count() != 0 {
		t.Errorf("%d turns ran for a client that had left", log.count())
	}
	if !strings.Contains(lines, "status=499") || strings.Contains(lines, "did not decide") || strings.Contains(lines, "auto=") {
		t.Errorf("want a 499 that blames nobody and names no chooser: %q", lines)
	}
}
