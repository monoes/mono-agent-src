package openaiapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a clock the tests move by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// While Jev is down every question would wait out the whole budget, with the
// request's slot held, so a few failures in a row stop the questions for a while:
// the rule decides, and one question after the cooldown finds out whether Jev is back.
func TestAutoBreakerStopsAskingAfterConsecutiveFailures(t *testing.T) {
	clock := newFakeClock()
	b := newAutoBreaker(3, 30*time.Second, clock.now)

	for i := 1; i <= 3; i++ {
		if !b.allow() {
			t.Fatalf("failure %d: a closed breaker must allow the question", i)
		}
		if got := b.record(false); (got == breakerOpened) != (i == 3) {
			t.Fatalf("failure %d: transition %v, the third failure opens it", i, got)
		}
	}
	if b.allow() {
		t.Error("an open breaker must not allow a question")
	}
	clock.advance(29 * time.Second)
	if b.allow() {
		t.Error("still open before the cooldown has passed")
	}

	// Half-open: exactly one question goes out, the others use the rule meanwhile.
	clock.advance(2 * time.Second)
	if !b.allow() {
		t.Fatal("after the cooldown one question must go out")
	}
	if b.allow() {
		t.Error("only one question is the probe: a second one must wait for its answer")
	}
	if got := b.record(false); got != breakerReopened {
		t.Errorf("a failed probe opens it again, quietly, got %v", got)
	}
	if b.allow() {
		t.Error("open again for another cooldown")
	}

	clock.advance(31 * time.Second)
	if !b.allow() {
		t.Fatal("the next probe")
	}
	if got := b.record(true); got != breakerClosed {
		t.Errorf("a probe that Jev answers closes it, got %v", got)
	}
	for i := 0; i < 2; i++ {
		if !b.allow() {
			t.Errorf("closed again: every question goes out, question %d was held back", i+1)
		}
	}
}

// Failures must be consecutive, and a question the caller abandoned says nothing.
func TestAutoBreakerCountsConsecutiveFailuresOnly(t *testing.T) {
	b := newAutoBreaker(3, 30*time.Second, newFakeClock().now)
	for i := 0; i < 10; i++ {
		b.allow()
		b.record(false)
		b.allow()
		b.record(false)
		b.allow()
		if got := b.record(true); got != breakerNone { // an answer in between: it never reaches three
			t.Fatalf("round %d: %v", i, got)
		}
	}
	if !b.allow() {
		t.Error("two failures and an answer, ten times: still closed")
	}

	// An abandoned probe frees the probe for the next request, and counts as nothing.
	clock := newFakeClock()
	b = newAutoBreaker(1, time.Second, clock.now)
	b.allow()
	b.record(false)
	clock.advance(2 * time.Second)
	if !b.allow() {
		t.Fatal("the probe")
	}
	b.abandon()
	if !b.allow() {
		t.Error("an abandoned probe must free the next request to be the probe")
	}
}

// pickAuto through the gateway: three questions that fail stop the fourth.
func TestPickAutoStopsAskingWhileJevIsDown(t *testing.T) {
	clock := newFakeClock()
	f := &fakeAuto{err: errors.New("jev: 503")}
	h := autoGateway(t, f)
	h.g.now = clock.now
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900), cand("claude/haiku", ChatOnly, true, 0.002, true, 800)}

	for i := 0; i < 3; i++ {
		if pick := h.g.pickAuto(context.Background(), "alice", "a secret haiku prompt", cands); pick.By != "rule" {
			t.Fatalf("question %d: %+v", i, pick)
		}
	}
	if f.asked != 3 {
		t.Fatalf("asked %d times before the breaker opened, want 3", f.asked)
	}
	pick := h.g.pickAuto(context.Background(), "alice", "a secret haiku prompt", cands)
	if pick.By != "rule" || pick.Model.ID != "claude/default" || f.asked != 3 {
		t.Errorf("while open: %+v after %d questions, want the rule's pick and no new question", pick, f.asked)
	}
	lines := strings.Join(h.logged(), "\n")
	if strings.Count(lines, "stop asking") != 1 || strings.Contains(lines, "haiku") {
		t.Errorf("the log says once that questions stop, and never the prompt: %q", lines)
	}

	// The cooldown passes and Jev is back: one question, then all of them again.
	clock.advance(31 * time.Second)
	f.script(false, nil, "claude/haiku", 0.9)
	if pick := h.g.pickAuto(context.Background(), "alice", "a secret haiku prompt", cands); pick.By != "jev" || pick.Model.ID != "claude/haiku" || f.asked != 4 {
		t.Errorf("the probe: %+v after %d questions", pick, f.asked)
	}
	if pick := h.g.pickAuto(context.Background(), "alice", "a secret haiku prompt", cands); pick.By != "jev" || f.asked != 5 {
		t.Errorf("closed again: %+v after %d questions", pick, f.asked)
	}
	if !strings.Contains(strings.Join(h.logged(), "\n"), "answers again") {
		t.Errorf("the log says when Jev answers again: %q", h.logged())
	}
}

// Jev answering something the rule has to override is not Jev being down, a
// caller that leaves says nothing about it, and a question that times out is down.
func TestPickAutoOnlyFailedQuestionsOpenTheBreaker(t *testing.T) {
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900), cand("claude/haiku", ChatOnly, true, 0.002, true, 800)}

	// Answers that are not options, or not sure enough: Jev is up.
	f := &fakeAuto{id: "grok/default", p: 1}
	h := autoGateway(t, f)
	for i := 0; i < 6; i++ {
		h.g.pickAuto(context.Background(), "alice", "hi", cands)
	}
	f.id, f.p, f.thr = "claude/haiku", 0.2, 0.9
	for i := 0; i < 6; i++ {
		h.g.pickAuto(context.Background(), "alice", "hi", cands)
	}
	if f.asked != 12 {
		t.Errorf("answers are not failures: asked %d of 12", f.asked)
	}

	// A caller that leaves: nothing counted. (The questions finish in goroutines
	// of their own, so wait for them to have been asked.)
	f = &fakeAuto{block: true}
	h = autoGateway(t, f)
	for i := 0; i < 6; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h.g.pickAuto(ctx, "alice", "hi", cands)
	}
	waitUntil(t, func() bool { return f.calls.Load() == 6 }, "a caller that left is not a failure: all 6 questions are asked")

	// No answer within the budget: three of them open it.
	f = &fakeAuto{block: true}
	h = autoGateway(t, f, func(_ *Deps, c *Config) { c.AutoTimeout = 20 * time.Millisecond })
	for i := 0; i < 5; i++ {
		h.g.pickAuto(context.Background(), "alice", "hi", cands)
	}
	waitUntil(t, func() bool { return f.calls.Load() >= 3 }, "three questions are asked before it opens")
	if n := f.calls.Load(); n != 3 {
		t.Errorf("three timeouts open it: asked %d, want 3", n)
	}
}

// A provider with no probability has not decided, even at threshold 0: the
// rule picks, and it is an answer for the breaker (the provider is up),
// so it never opens.
func TestPickAutoNoProbabilityIsTheRulesPickNotAFailure(t *testing.T) {
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900), cand("claude/haiku", ChatOnly, true, 0.002, true, 800)}
	f := &fakeAuto{err: errNoProbability}
	h := autoGateway(t, f)
	for i := 0; i < 6; i++ {
		if pick := h.g.pickAuto(context.Background(), "alice", "hi", cands); pick.By != "rule" || pick.Model.ID != ruleChoice(cands).ID {
			t.Fatalf("pick = %+v", pick)
		}
	}
	if f.asked != 6 {
		t.Errorf("no probability opened the breaker: asked %d of 6", f.asked)
	}
	if !strings.Contains(strings.Join(h.logged(), "\n"), "no probability") {
		t.Errorf("log = %q", h.logged())
	}
}

// waitUntil polls cond for up to two seconds.
func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", what)
}

// A caller that leaves while the probe is out must not leave the breaker waiting for
// an answer that nobody will record: the next request is the probe.
func TestPickAutoFreesTheProbeWhenTheCallerLeaves(t *testing.T) {
	clock := newFakeClock()
	f := &fakeAuto{err: errors.New("jev: 503")}
	h := autoGateway(t, f)
	h.g.now = clock.now
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900), cand("claude/haiku", ChatOnly, true, 0.002, true, 800)}
	for i := 0; i < 3; i++ {
		h.g.pickAuto(context.Background(), "alice", "hi", cands)
	}
	clock.advance(31 * time.Second)

	f.script(true, nil, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the caller left: this request was the probe
	h.g.pickAuto(ctx, "alice", "hi", cands)
	waitUntil(t, func() bool { return f.calls.Load() == 4 }, "the probe is asked")

	f.script(false, nil, "claude/haiku", 0.9)
	if pick := h.g.pickAuto(context.Background(), "alice", "hi", cands); pick.By != "jev" {
		t.Errorf("the next request must be the probe, not wait for the one that left: %+v", pick)
	}
}

// Jev being down for one profile (its key was revoked, say) says nothing about
// another profile's.
func TestAutoBreakerIsPerProfile(t *testing.T) {
	f := &fakeAuto{err: errors.New("jev: 401")}
	h := autoGateway(t, f)
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900), cand("claude/haiku", ChatOnly, true, 0.002, true, 800)}
	for i := 0; i < 5; i++ {
		h.g.pickAuto(context.Background(), "alice", "hi", cands)
	}
	if f.asked != 3 {
		t.Fatalf("alice: asked %d, want 3", f.asked)
	}
	h.g.pickAuto(context.Background(), "bob", "hi", cands)
	if f.asked != 4 {
		t.Errorf("bob must still be asked: %d questions", f.asked)
	}
}
