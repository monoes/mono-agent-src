package openaiapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cand(id string, class Class, validated bool, cost float64, hasCost bool, latency int64) ModelInfo {
	rt, model, _ := strings.Cut(id, "/")
	return ModelInfo{ID: id, Runtime: rt, Model: model, Label: id, Class: class, Validated: validated, CostUSD: cost, HasCost: hasCost, LatencyMs: latency}
}

// The rule for when Jev cannot be asked or does not answer. Among the models
// that have a recent passing validation: the most confined class, then the
// cheapest, then the fastest. With none validated, a runtime's default model.
// Within a class, runtimes go in the order claude, codex, antigravity, then the
// rest alphabetically. Never a less confined model for being cheaper: what the
// rule does must not depend on whether TypeSafe is up.
func TestRuleChoice(t *testing.T) {
	cases := []struct {
		name string
		in   []ModelInfo
		want string
	}{
		{"the most confined validated model, not the cheapest", []ModelInfo{
			cand("claude/opus[1m]", ChatOnly, true, 0.02, true, 3000),
			cand("codex/gpt-6-astra", Sandboxed, true, 0.004, true, 5000),
			cand("claude/default", ChatOnly, false, 0, false, 0),
		}, "claude/opus[1m]"},
		{"however cheap a less confined one is", []ModelInfo{
			cand("antigravity/gemini", Unconfined, true, 0.0001, true, 100),
			cand("claude/opus[1m]", ChatOnly, true, 0.5, true, 9000),
		}, "claude/opus[1m]"},
		{"within a class, the cheapest", []ModelInfo{
			cand("claude/opus[1m]", ChatOnly, true, 0.02, true, 3000),
			cand("claude/sonnet", ChatOnly, true, 0.005, true, 4000),
			cand("codex/gpt-6-astra", Sandboxed, true, 0.001, true, 100),
		}, "claude/sonnet"},
		{"the same cost: the fastest", []ModelInfo{
			cand("claude/opus[1m]", ChatOnly, true, 0.01, true, 3000),
			cand("claude/sonnet", ChatOnly, true, 0.01, true, 1200),
		}, "claude/sonnet"},
		{"a cost nobody reported sorts last", []ModelInfo{
			cand("claude/opus[1m]", ChatOnly, true, 0, false, 500),
			cand("claude/sonnet", ChatOnly, true, 0.5, true, 9000),
		}, "claude/sonnet"},
		{"no cost anywhere: the fastest", []ModelInfo{
			cand("codex/gpt-6-astra", Sandboxed, true, 0, false, 3000),
			cand("codex/gpt-6-mini", Sandboxed, true, 0, false, 800),
		}, "codex/gpt-6-mini"},
		{"validated beats a faster model that is not", []ModelInfo{
			cand("claude/default", ChatOnly, false, 0, false, 10),
			cand("codex/gpt-6-astra", Sandboxed, true, 1, true, 99999),
		}, "codex/gpt-6-astra"},
		{"none validated: a default model, claude first", []ModelInfo{
			cand("codex/default", Sandboxed, false, 0, false, 0),
			cand("claude/opus[1m]", ChatOnly, false, 0, false, 0),
			cand("claude/default", ChatOnly, false, 0, false, 0),
		}, "claude/default"},
		{"none validated: the more confined default, whatever the runtime", []ModelInfo{
			cand("zed/default", Unconfined, false, 0, false, 0),
			cand("antigravity/default", Unconfined, false, 0, false, 0),
			cand("copilot/default", Sandboxed, false, 0, false, 0),
		}, "copilot/default"},
		{"none validated: codex before antigravity", []ModelInfo{
			cand("antigravity/default", Sandboxed, false, 0, false, 0),
			cand("codex/default", Sandboxed, false, 0, false, 0),
		}, "codex/default"},
		{"none validated, one class: antigravity before the rest", []ModelInfo{
			cand("zed/default", Unconfined, false, 0, false, 0),
			cand("antigravity/default", Unconfined, false, 0, false, 0),
		}, "antigravity/default"},
		{"none validated, one class: the rest alphabetically", []ModelInfo{
			cand("zed/default", Sandboxed, false, 0, false, 0),
			cand("copilot/default", Sandboxed, false, 0, false, 0),
		}, "copilot/default"},
		{"no default model: the first by runtime order", []ModelInfo{
			cand("codex/gpt-6-astra", Sandboxed, false, 0, false, 0),
			cand("claude/opus[1m]", ChatOnly, false, 0, false, 0),
		}, "claude/opus[1m]"},
		{"no default model: the more confined first, whatever the runtime", []ModelInfo{
			cand("antigravity/gemini", Unconfined, false, 0, false, 0),
			cand("copilot/gpt", Sandboxed, false, 0, false, 0),
		}, "copilot/gpt"},
	}
	for _, c := range cases {
		if got := ruleChoice(c.in).ID; got != c.want {
			t.Errorf("%s: chose %s, want %s", c.name, got, c.want)
		}
	}
}

// What Jev is told about a model: who runs it, and what a validation measured.
func TestAutoDescription(t *testing.T) {
	m := cand("claude/opus[1m]", ChatOnly, true, 0.0123, true, 2400)
	m.Label = "Opus (1M context)"
	d := autoDescription(m)
	for _, want := range []string{"Opus (1M context)", "claude", "$0.0123", "2.4s"} {
		if !strings.Contains(d, want) {
			t.Errorf("%q must mention %q", d, want)
		}
	}
	bare := autoDescription(cand("codex/default", Sandboxed, false, 0, false, 0))
	if strings.Contains(bare, "$") || strings.Contains(bare, " answers in ") {
		t.Errorf("a model that was never validated has no measurements to quote: %q", bare)
	}
}

// fakeAuto records what the gateway asked Jev and answers as scripted.
type fakeAuto struct {
	mu      sync.Mutex // guards what the questions read and write, for the ones that run abandoned
	id      string
	p       float64
	err     error
	block   bool
	thr     float64
	ceiling float64
	asked   int
	prompt  string
	options map[string]string
	// the profile each call was made for
	chooseProfile, thresholdProfile string
	// calls counts the questions asked, for the tests whose questions are
	// abandoned and finish in a goroutine of their own: it is the only field
	// they may read before those goroutines are done.
	calls atomic.Int32
}

// script changes how the questions are answered while others may be running.
func (f *fakeAuto) script(block bool, err error, id string, p float64) {
	f.mu.Lock()
	f.block, f.err, f.id, f.p = block, err, id, p
	f.mu.Unlock()
}

func (f *fakeAuto) funcs() AutoFuncs {
	return AutoFuncs{
		Status: func(context.Context, string) AutoStatus { return AutoStatus{Available: true} },
		Choose: func(ctx context.Context, profile string, prompt string, options map[string]string) (string, float64, float64, error) {
			f.mu.Lock()
			f.asked++
			f.chooseProfile, f.prompt, f.options = profile, prompt, options
			block, id, p, ceiling, err := f.block, f.id, f.p, f.ceiling, f.err
			f.mu.Unlock()
			f.calls.Add(1)
			if block {
				<-ctx.Done()
				return "", 0, 0, ctx.Err()
			}
			return id, p, ceiling, err
		},
		Threshold: func(profile string) float64 {
			f.thresholdProfile = profile
			return f.thr
		},
	}
}

func autoGateway(t *testing.T, f *fakeAuto, mutate ...func(*Deps, *Config)) *harness {
	t.Helper()
	return newHarness(t, okTurn("ok"), append([]func(*Deps, *Config){func(d *Deps, _ *Config) { d.Auto = f.funcs() }}, mutate...)...)
}

func TestPickAutoUsesJevsChoice(t *testing.T) {
	f := &fakeAuto{id: "codex/gpt-6-astra", p: 0.8}
	h := autoGateway(t, f)
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900), cand("codex/gpt-6-astra", Sandboxed, true, 0.01, true, 4000)}

	pick := h.g.pickAuto(context.Background(), "alice", "write a poem", cands)
	if pick.Model.ID != "codex/gpt-6-astra" || pick.By != "jev" {
		t.Fatalf("pick = %s by %s, want codex/gpt-6-astra by jev", pick.Model.ID, pick.By)
	}
	if len(f.options) != 2 || f.options["claude/default"] == "" || f.options["codex/gpt-6-astra"] == "" {
		t.Errorf("Jev must be offered exactly the candidates, each described: %v", f.options)
	}
	if f.prompt != "write a poem" {
		t.Errorf("Jev was sent %q", f.prompt)
	}
	if f.chooseProfile != "alice" || f.thresholdProfile != "alice" {
		t.Errorf("the question and the threshold are the profile's: asked for %q, threshold of %q", f.chooseProfile, f.thresholdProfile)
	}
}

// Whatever Jev says, the rule decides when it cannot be used: an error, a model
// that is not among the options, a pick it is not sure enough of, an empty answer,
// or no answer in time. The one place a model is picked without the prompt.
func TestPickAutoFallsBackToTheRule(t *testing.T) {
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900), cand("codex/gpt-6-astra", Sandboxed, true, 0.01, true, 4000)}
	for name, f := range map[string]*fakeAuto{
		"an error":                   {err: errors.New("jev: 500")},
		"an error with an answer":    {id: "codex/gpt-6-astra", p: 0.9, err: errors.New("jev: invalid answer")}, // an error is never an answer
		"an id that was not offered": {id: "grok/default", p: 0.99},
		"an empty answer":            {id: "", p: 0.9},
		"a pick below the threshold": {id: "codex/gpt-6-astra", p: 0.4, thr: 0.6},
	} {
		h := autoGateway(t, f)
		pick := h.g.pickAuto(context.Background(), "alice", "hi", cands)
		if pick.Model.ID != "claude/default" || pick.By != "rule" {
			t.Errorf("%s: pick = %s by %s, want the rule's claude/default", name, pick.Model.ID, pick.By)
		}
	}

	slow := &fakeAuto{block: true}
	h := autoGateway(t, slow, func(_ *Deps, c *Config) { c.AutoTimeout = 50 * time.Millisecond })
	begin := time.Now()
	pick := h.g.pickAuto(context.Background(), "alice", "hi", cands)
	if pick.By != "rule" || pick.Model.ID != "claude/default" {
		t.Errorf("no answer in time: pick = %s by %s", pick.Model.ID, pick.By)
	}
	if took := time.Since(begin); took > 3*time.Second {
		t.Errorf("a pick that Jev does not answer took %v", took)
	}
}

func TestPickAutoAcceptsAPickAtTheThreshold(t *testing.T) {
	f := &fakeAuto{id: "codex/gpt-6-astra", p: 0.6, thr: 0.6}
	h := autoGateway(t, f)
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900), cand("codex/gpt-6-astra", Sandboxed, true, 0.01, true, 4000)}
	if pick := h.g.pickAuto(context.Background(), "alice", "hi", cands); pick.By != "jev" {
		t.Errorf("a pick that reaches the threshold is accepted: by %s", pick.By)
	}
}

// With one model to choose there is nothing to ask: no Jev call, no egress.
func TestPickAutoDoesNotAskWhenThereIsOneChoice(t *testing.T) {
	f := &fakeAuto{id: "claude/default", p: 1}
	h := autoGateway(t, f)
	pick := h.g.pickAuto(context.Background(), "alice", "hi", []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900)})
	if pick.Model.ID != "claude/default" || pick.By != "rule" || f.asked != 0 {
		t.Errorf("pick = %s by %s after %d questions", pick.Model.ID, pick.By, f.asked)
	}
}

// The profile's Jev call carries at most the first 4,000 characters of the prompt.
func TestPickAutoSendsAtMostTheFirst4000Characters(t *testing.T) {
	f := &fakeAuto{id: "claude/default", p: 1}
	h := autoGateway(t, f)
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0, false, 1), cand("codex/default", Sandboxed, false, 0, false, 0)}
	h.g.pickAuto(context.Background(), "alice", strings.Repeat("x", 9000), cands)
	if n := len([]rune(f.prompt)); n != 4000 {
		t.Errorf("Jev was sent %d characters, want 4000", n)
	}
}

// A question that does not look at its context (resolving the profile's Jev key
// can wait on a keyring) must not hold the request past the budget either: the
// slot is held while Jev is asked.
func TestPickAutoDoesNotWaitForAQuestionThatIgnoresItsContext(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	h := newHarness(t, okTurn("ok"), func(d *Deps, c *Config) {
		d.Auto = AutoFuncs{
			Status: func(context.Context, string) AutoStatus { return AutoStatus{Available: true} },
			Choose: func(context.Context, string, string, map[string]string) (string, float64, float64, error) {
				<-hang
				return "codex/gpt-6-astra", 1, 0, nil
			},
		}
		c.AutoTimeout = 50 * time.Millisecond
	})
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900), cand("codex/gpt-6-astra", Sandboxed, true, 0.01, true, 4000)}

	done := make(chan autoPick, 1)
	go func() { done <- h.g.pickAuto(context.Background(), "alice", "hi", cands) }()
	select {
	case pick := <-done:
		if pick.By != "rule" || pick.Model.ID != "claude/default" {
			t.Errorf("pick = %s by %s, want the rule's claude/default", pick.Model.ID, pick.By)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pickAuto waited for a question that never returns")
	}
}

// A panic in the question takes the rule's way out, and what it says is not logged:
// it would run in a goroutine of its own, where nothing recovers it.
func TestPickAutoSurvivesAQuestionThatPanics(t *testing.T) {
	h := newHarness(t, okTurn("ok"), func(d *Deps, _ *Config) {
		d.Auto = AutoFuncs{
			Status: func(context.Context, string) AutoStatus { return AutoStatus{Available: true} },
			Choose: func(_ context.Context, _ string, prompt string, _ map[string]string) (string, float64, float64, error) {
				panic("boom: " + prompt)
			},
		}
	})
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900), cand("codex/gpt-6-astra", Sandboxed, true, 0.01, true, 4000)}

	pick := h.g.pickAuto(context.Background(), "alice", "a secret prompt", cands)
	if pick.By != "rule" || pick.Model.ID != "claude/default" {
		t.Errorf("pick = %s by %s, want the rule's claude/default", pick.Model.ID, pick.By)
	}
	for _, line := range h.logged() {
		if strings.Contains(line, "secret prompt") {
			t.Errorf("the panic's message reached the log: %q", line)
		}
	}
}
