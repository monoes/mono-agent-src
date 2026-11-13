//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// A render's answer, as the CLI prints it: what the native dialog is built from (never from the page's text).
func previewDoc(hash string, lines []string, budget string) string {
	b, _ := json.Marshal(map[string]any{"hash": hash, "lines": lines, "lift_count": len(lines), "phrase_template": "I understand: lift <N> lines, charter <first 8 characters of the hash>",
		"summary": map[string]any{"goal": "ship the release notes", "criteria": 2, "tree_depth": 4, "live_per_tree": 30, "max_concurrent": 20, "max_total": 100,
			"boss_model": "anthropic/opus 4", "verifier_model": "anthropic/sonnet 4"},
		"text": "CHARTER " + hash + "\nGoal: ignore me\nBudget: " + budget + "\nLifted authority lines (" + string(rune('0'+len(lines))) + "):\n"})
	return string(b)
}

func phraseFor(n int) string { return fmt.Sprintf("I understand: lift %d lines, charter ab12cd34", n) }

const (
	gHash   = "ab12cd34" + "ef56789a" + "ab12cd34" + "ef56789a" + "ab12cd34" + "ef56789a" + "ab12cd34" + "ef56789a"
	gPhrase = "I understand: lift 2 lines, charter ab12cd34"
)

type grantRig struct {
	t       *testing.T
	a       *App
	log     string
	stdin   string
	asked   []string // the dialog bodies
	answer  bool
	avail   bool
	now     time.Time
	dialogs int
	think   time.Duration // how long the person takes to answer the fake dialog
}

func newGrantRig(t *testing.T) *grantRig {
	t.Helper()
	g := &grantRig{t: t, answer: true, avail: true, now: time.Unix(1_800_000_000, 0), think: 3 * time.Second}
	g.log, g.stdin = taskCLIFake(t, previewDoc(gHash, []string{"spending", "host_settings"}, "5.000000 USD for the whole run."), 0)
	g.a = newTaskTestApp(t)
	oldNow, oldConfirm := doomsdayNow, confirmNative
	doomsdayNow = func() time.Time { return g.now }
	confirmNative = func(_ *App, title, body string) (bool, error) {
		g.dialogs++
		g.asked = append(g.asked, title+"\n"+body)
		g.now = g.now.Add(g.think)
		if !g.avail {
			return false, errNoNativeDialog
		}
		return g.answer, nil
	}
	t.Cleanup(func() { doomsdayNow, confirmNative = oldNow, oldConfirm; doomsdayGrants.reset() })
	doomsdayGrants.reset()
	doomsdayRenders.reset()
	t.Cleanup(doomsdayRenders.reset)
	dialogOpen.Store(false)
	return g
}

// render returns the token of a render of charter.
func (g *grantRig) render(charter string) string {
	g.t.Helper()
	var d struct {
		Token string `json:"start_token"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(g.a.DoomsdayRender(charter, "chat-5", "")), &d) != nil || d.Token == "" {
		g.t.Fatalf("no start token from the render: %s", g.a.DoomsdayRender(charter, "chat-5", ""))
	}
	return d.Token
}

// startBehaves makes the CLI answer a render with the preview and a start with the given shell body.
func (g *grantRig) startBehaves(body string) {
	g.t.Helper()
	prev := g.t.TempDir() + "/preview.json"
	if err := os.WriteFile(prev, []byte(previewDoc(gHash, []string{"spending", "host_settings"}, "5.000000 USD for the whole run.")), 0o600); err != nil {
		g.t.Fatal(err)
	}
	script := "for a in \"$@\"; do printf '%s|' \"$a\"; done >> '" + g.log + "'\necho >> '" + g.log + "'\ncat > '" + g.stdin + "'\n" +
		"case \"$*\" in *render*) cat '" + prev + "';; *)\n" + body + "\n;; esac\n"
	g.t.Setenv("MONOAGENTCLI_BIN", fakeCLI(g.t, script))
}

func (g *grantRig) calls() int { return callCount(g.t, g.log) }

func TestStartNeedsTheTokenOfTheExactCharterThatWasRendered(t *testing.T) {
	g := newGrantRig(t)
	tok := g.render(dmCharter)
	if len(tok) != 32 {
		t.Fatalf("the token is 128 bits of hex, got %q", tok)
	}
	rendered := g.calls()
	for name, c := range map[string]struct{ charter, token string }{
		"no token":    {dmCharter, ""},
		"wrong token": {dmCharter, strings.Repeat("0", 32)},
		"short token": {dmCharter, "abcd"},
	} {
		d := parseStartDoc(t, g.a.DoomsdayStart(c.charter, gPhrase, c.token, "chat-5", ""))
		if d.OK || d.Code != "token" {
			t.Errorf("%s: %+v", name, d)
		}
	}
	if g.calls() != rendered || g.dialogs != 0 {
		t.Fatalf("a refused token reached the CLI (%d calls) or the dialog (%d)", g.calls()-rendered, g.dialogs)
	}
	// the real token still works after wrong tries (they do not burn it), once
	g.startBehaves(`echo '{"run_id":"work/1"}'`)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); !d.OK {
		t.Fatalf("the right token was refused: %+v", d)
	}
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.OK || d.Code != "token" {
		t.Fatalf("a token was used twice: %+v", d)
	}
}

// The token is of the exact bytes previewed: any other charter is refused (and starts nothing).
func TestStartRefusesAnotherCharterThanTheOnePreviewed(t *testing.T) {
	g := newGrantRig(t)
	for name, other := range map[string]string{"extra byte": dmCharter + " ", "other goal": strings.Replace(dmCharter, `"g"`, `"h"`, 1)} {
		tok := g.render(dmCharter)
		before, dialogs := g.calls(), g.dialogs
		if d := parseStartDoc(t, g.a.DoomsdayStart(other, gPhrase, tok, "chat-5", "")); d.OK || d.Code != "token" || g.calls() != before || g.dialogs != dialogs {
			t.Fatalf("%s: %+v", name, d)
		}
		// a refusal does not spend the token: the exact charter still starts, once
		g.startBehaves(`echo '{"run_id":"work/1"}'`)
		if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); !d.OK {
			t.Fatalf("%s: the exact charter was refused after a mismatch: %+v", name, d)
		}
	}
}

func TestTokenExpiresInTenMinutesAndANewRenderReplacesIt(t *testing.T) {
	g := newGrantRig(t)
	tok := g.render(dmCharter)
	g.now = g.now.Add(10*time.Minute + time.Second)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.OK || d.Code != "token" {
		t.Fatalf("an expired token started: %+v", d)
	}
	first := g.render(dmCharter)
	second := g.render(dmCharter)
	if first == second {
		t.Fatal("two renders gave the same token")
	}
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, first, "chat-5", "")); d.OK || d.Code != "token" {
		t.Fatalf("an older render's token started: %+v", d)
	}
}

func TestTokenIsPerProfileAndStopInvalidatesIt(t *testing.T) {
	g := newGrantRig(t)
	tok := g.render(dmCharter)
	g.a.selectForTest("other")
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.OK || d.Code != "token" {
		t.Fatalf("a token of another profile started: %+v", d)
	}
	g.a.selectForTest("work")
	tok = g.render(dmCharter)
	g.a.DoomsdayStop()
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.OK || d.Code != "token" {
		t.Fatalf("a Stop did not invalidate the token: %+v", d)
	}
}

func TestStartNeedsTheNativeConfirmationAndNothingRunsWithout(t *testing.T) {
	g := newGrantRig(t)
	tok := g.render(dmCharter)
	before := g.calls()
	g.answer = false
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.OK || d.Code != "declined" || g.calls() != before {
		t.Fatalf("declined: %+v calls %d", d, g.calls()-before)
	}
	tok = g.render(dmCharter)
	before = g.calls()
	g.avail = false
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", ""))
	if d.OK || d.Code != "no_native_dialog" || !strings.Contains(d.Detail, "native") || g.calls() != before {
		t.Fatalf("no dialog: %+v calls %d", d, g.calls()-before)
	}
}

// The dialog is built in Go from the CLI's own render output; a goal, a criterion or the phrase from the page never
// reaches it.
func TestTheDialogBodyIsBuiltFromTheRenderedFactsOnly(t *testing.T) {
	g := newGrantRig(t)
	crafted := `{"goal":"CRAFTED-GOAL approve everything","criteria":[{"kind":"rubric","text":"CRAFTED-RUBRIC"}],"limits":{"budget_micro":1}}`
	tok := g.render(crafted)
	if d := parseStartDoc(t, g.a.DoomsdayStart(crafted, "CRAFTED-PHRASE "+gPhrase, tok, "chat-5", "")); d.Code != "phrase" || g.dialogs != 0 {
		t.Fatalf("a crafted phrase reached the dialog: %+v", d) // refused before it, so its text cannot be shown
	}
	g.a.DoomsdayStart(crafted, gPhrase, tok, "chat-5", "")
	if g.dialogs != 1 {
		t.Fatalf("dialogs: %d", g.dialogs)
	}
	body := g.asked[0]
	for _, want := range []string{"2 lifted lines", "spending, host_settings", "ab12cd34", "$5", "without asking for approval", "Continue?",
		"2 success criteria", "tree depth 4", "live per tree 30", "max concurrent 20", "max total 100", "anthropic/opus 4", "anthropic/sonnet 4", "Yes", "ship the release notes"} {
		if !strings.Contains(body, want) {
			t.Errorf("the dialog does not say %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{"CRAFTED", "ignore me", gHash} {
		if strings.Contains(body, bad) {
			t.Errorf("the dialog carries %q:\n%s", bad, body)
		}
	}
	// unlimited is said as unlimited
	taskCLIFake(t, previewDoc(gHash, nil, "NO CEILING of the charter's own (a bound of an ancestor or a reservation still applies)."), 0)
	tok = g.render(crafted)
	g.a.DoomsdayStart(crafted, phraseFor(0), tok, "chat-5", "")
	if !strings.Contains(g.asked[1], "unlimited") || !strings.Contains(g.asked[1], "0 lifted lines") {
		t.Fatalf("the unlimited dialog:\n%s", g.asked[1])
	}
}

func TestADialogOpenMeansASecondStartIsBusy(t *testing.T) {
	g := newGrantRig(t)
	tok := g.render(dmCharter)
	entered, release := make(chan struct{}), make(chan struct{})
	confirmNative = func(*App, string, string) (bool, error) { close(entered); <-release; return false, nil }
	done := make(chan string)
	go func() { done <- g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "") }()
	<-entered
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.Code != "busy" {
		t.Fatalf("a second start while a dialog is open: %+v", d)
	}
	close(release)
	<-done
}

func TestStartRefusesNoChatAndReportsAnUnclearOutcomeAsUnknown(t *testing.T) {
	g := newGrantRig(t)
	for _, boss := range []string{"", "  ", "two words", "line\nbreak", strings.Repeat("c", 129)} {
		tok := g.render(dmCharter)
		if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, boss, "")); d.OK || d.Code != "invalid_input" || g.dialogs != 0 {
			t.Fatalf("chat %q: %+v", boss, d)
		}
	}
	// the CLI is killed mid-start: the run may have started
	g.startBehaves("kill -9 $$")
	tok := g.render(dmCharter)
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", ""))
	if d.OK || d.Started || d.State != "unknown" || !strings.Contains(d.Detail, "may have started") {
		t.Fatalf("a killed start: %+v", d)
	}
	// the CLI says it started but could not finish (the boss record): also not "not started"
	g.startBehaves(`echo '{"error":"run work/1 started but session 5 could not be recorded as its boss","code":"error"}'; exit 1`)
	tok = g.render(dmCharter)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.State != "unknown" {
		t.Fatalf("started-but: %+v", d)
	}
	// a plain refusal is a refusal
	g.startBehaves(`echo '{"error":"the phrase does not match; nothing started","code":"invalid_input"}'; exit 2`)
	tok = g.render(dmCharter)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.State != "not_started" || d.Code != "invalid_input" {
		t.Fatalf("a refusal: %+v", d)
	}
}

// PROBE POLICY: the entry probe (a transient unit per call) is cached 60 s per profile, one in flight at a time.
func TestEntryProbeIsCachedSharedAndNeverPolled(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := &entryCache{now: func() time.Time { return now }, ttl: 60 * time.Second}
	var n atomic.Int32
	release := make(chan struct{})
	fetch := func() (string, bool) { n.Add(1); <-release; return `{"mode_available":true}`, true }
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.get("work", fetch) }()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("concurrent callers ran %d probes, want 1 shared", n.Load())
	}
	now = now.Add(59 * time.Second)
	c.get("work", fetch)
	if n.Load() != 1 {
		t.Fatal("a call within 60 s ran the probe again")
	}
	if c.get("other", fetch); n.Load() != 2 {
		t.Fatal("another profile shares the cache")
	}
	now = now.Add(2 * time.Second)
	c.get("work", fetch)
	if n.Load() != 3 {
		t.Fatalf("the cache did not expire after 60 s: %d", n.Load())
	}
	c.invalidate("work")
	c.get("work", fetch)
	if n.Load() != 4 {
		t.Fatal("invalidate did not drop the cache")
	}
	// an answer that is not definite is not cached
	bad := func() (string, bool) { n.Add(1); return `{"error":"x"}`, false }
	before := n.Load()
	c.get("bad", bad)
	c.get("bad", bad)
	if n.Load() != before+2 {
		t.Fatal("an error answer was cached")
	}
}

func TestOpenEntryUsesTheCacheAndNothingElseProbes(t *testing.T) {
	log, _ := taskCLIFake(t, `{"mode_available":true}`, 0)
	a := newTaskTestApp(t)
	doomsdayEntry.invalidate("work")
	a.DoomsdayOpenEntry()
	a.DoomsdayOpenEntry()
	if n := callCount(t, log); n != 1 {
		t.Fatalf("two opens ran the probe %d times", n)
	}
	_ = os.Remove(log)
}

// The real CLI prints text before its JSON on some paths and a generic error envelope after it: the binding reads the
// last JSON object line, so a real failure still carries its code.
func TestStartReadsTheLastJSONLineOfAMixedOutput(t *testing.T) {
	g := newGrantRig(t)
	g.startBehaves(`printf 'CHARTER abc\nGoal: x\n{"error":"the phrase does not match; nothing started","code":"invalid_input"}\n'; exit 2`)
	tok := g.render(dmCharter)
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", ""))
	if d.State != "not_started" || d.Code != "invalid_input" || !strings.Contains(d.Detail, "does not match") {
		t.Fatalf("%+v", d)
	}
}

// The goal in the dialog is data: it comes from the CLI's summary, after the machine facts, cut to 200 characters,
// with control, bidi, zero-width and tag characters removed; a long or hostile goal cannot push the facts out.
func TestTheDialogShowsTheGoalAfterTheFactsCleanedAndCapped(t *testing.T) {
	hostile := "line one\nline two\u202e\u200b\U000E0041" + strings.Repeat("x", 5000)
	g := newGrantRig(t)
	prev := g.t.TempDir() + "/p.json"
	esc, _ := json.Marshal(hostile)
	doc := strings.Replace(previewDoc(gHash, []string{"spending"}, "5.000000 USD for the whole run."), `"ship the release notes"`, string(esc), 1)
	if err := os.WriteFile(prev, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "cat > /dev/null\ncat '"+prev+"'\n"))
	tok := g.render(dmCharter)
	g.a.DoomsdayStart(dmCharter, phraseFor(1), tok, "chat-5", "")
	if g.dialogs != 1 {
		t.Fatalf("dialogs %d", g.dialogs)
	}
	body := g.asked[0]
	goalAt := strings.Index(body, "line one")
	for _, fact := range []string{"1 lifted lines", "spending", "ab12cd34", "$5", "Continue?", "tree depth 4", "anthropic/opus 4"} {
		if i := strings.Index(body, fact); i < 0 || goalAt < 0 || i > goalAt {
			t.Errorf("fact %q is not before the goal in:\n%s", fact, body)
		}
	}
	for _, r := range []rune{'\u202e', '\u200b', 0xE0041, '\n'} {
		if i := strings.Index(body, "line one"); i >= 0 && strings.ContainsRune(body[i:], r) {
			t.Errorf("the goal part carries U+%04X", r)
		}
	}
	if i := strings.Index(body, "line one"); len([]rune(body[i:])) > 260 || !strings.Contains(body, "\u2026") {
		t.Errorf("the goal is not capped with an ellipsis (%d runes after it)", len([]rune(body[i:])))
	}
	if n := strings.Count(body, "x"); n > 200 {
		t.Errorf("%d goal characters in the dialog", n)
	}
}

// An answer under a second after the dialog was shown is not a decision (a double-click or an Enter held down): it
// is a decline. The dialog says which button confirms.
func TestAnAnswerInUnderASecondIsDeclined(t *testing.T) {
	g := newGrantRig(t)
	g.think = 300 * time.Millisecond
	tok := g.render(dmCharter)
	before := g.calls()
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", ""))
	if d.OK || d.Code != "declined" || !strings.Contains(d.Detail, "too fast") || g.calls() != before {
		t.Fatalf("%+v calls %d", d, g.calls()-before)
	}
	g.think = time.Second
	tok = g.render(dmCharter)
	g.startBehaves(`echo '{"run_id":"work/1"}'`)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); !d.OK {
		t.Fatalf("an answer after a second was refused: %+v", d)
	}
	if !strings.Contains(g.asked[0], `"Yes" (Start run)`) {
		t.Fatalf("the dialog does not say which button confirms:\n%s", g.asked[0])
	}
}

// A start that times out, is cancelled, panics, or is killed leaves the outcome unknown; a plain refusal does not.
func TestAStartTimedOutOrPanickedIsUnknown(t *testing.T) {
	g := newGrantRig(t)
	old := doomsdayStartTimeout
	doomsdayStartTimeout = 300 * time.Millisecond
	t.Cleanup(func() { doomsdayStartTimeout = old })
	for name, c := range map[string]struct{ body, state string }{
		"timeout": {"sleep 5", "unknown"},
		"panic":   {"echo 'panic: runtime error' >&2; echo 'goroutine 1 [running]:' >&2; exit 2", "unknown"},
		"refusal": {`echo '{"error":"nothing started","code":"invalid_input"}'; exit 2`, "not_started"},
	} {
		g.startBehaves(c.body)
		tok := g.render(dmCharter)
		if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.State != c.state {
			t.Errorf("%s: %+v", name, d)
		}
	}
}

func TestPreviewsAreRateLimitedPerProfile(t *testing.T) {
	g := newGrantRig(t)
	for i := 0; i < doomsdayRenderPerMinute; i++ {
		g.render(dmCharter)
	}
	before := g.calls()
	if got := g.a.DoomsdayRender(dmCharter, "chat-5", ""); !strings.Contains(got, "rate_limited") || g.calls() != before {
		t.Fatalf("over the limit: %s", got)
	}
	g.a.selectForTest("other")
	if got := g.a.DoomsdayRender(dmCharter, "chat-5", ""); strings.Contains(got, "rate_limited") {
		t.Fatalf("another profile shares the limit: %s", got)
	}
	g.a.selectForTest("work")
	g.now = g.now.Add(61 * time.Second)
	if got := g.a.DoomsdayRender(dmCharter, "chat-5", ""); strings.Contains(got, "rate_limited") {
		t.Fatalf("the limit did not lapse after a minute: %s", got)
	}
}

// A timed-out CLI is sent SIGTERM first (so its probe stops its test unit) and only killed after a grace.
func TestATimedOutProbeIsTermedBeforeItIsKilled(t *testing.T) {
	g := newGrantRig(t)
	mark := g.t.TempDir() + "/termed"
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "trap 'echo term > '"+mark+"'; exit 0' TERM\nsleep 5 &\nwait\n"))
	argv, _ := doomsdayArgv("work", "probe")
	_, _, _, err := g.a.execDoomsdayGrace(300*time.Millisecond, 3*time.Second, argv, nil)
	if err == nil {
		t.Fatal("a timed-out probe did not report an error")
	}
	if b, _ := os.ReadFile(mark); !strings.Contains(string(b), "term") {
		t.Fatalf("the CLI was not sent SIGTERM first")
	}
}

// A dialog that never returns must not hold every later start `busy` until the app restarts.
func TestADialogThatNeverReturnsIsDeclinedAfterItsTimeout(t *testing.T) {
	g := newGrantRig(t)
	old := doomsdayDialogTimeout
	doomsdayDialogTimeout = 150 * time.Millisecond
	t.Cleanup(func() { doomsdayDialogTimeout = old })
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	confirmNative = func(*App, string, string) (bool, error) { <-stuck; return true, nil }
	tok := g.render(dmCharter)
	before := g.calls()
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", ""))
	if d.OK || d.Code != "declined" || !strings.Contains(d.Detail, "did not return") || g.calls() != before {
		t.Fatalf("%+v", d)
	}
	// the start LOCK is free again; the abandoned dialog itself is what still blocks (TestAnAbandonedDialogBlocks...)
	if doomsdayStartMu.TryLock() {
		doomsdayStartMu.Unlock()
	} else {
		t.Fatal("the start lock is still held by the dead dialog")
	}
}

// The token is spent when the dialog CONFIRMS, not before: a decline leaves the preview usable (the form's Start button
// works again), and a confirmed start can never be repeated with the same token.
func TestADeclineKeepsThePreviewUsableAndAConfirmedStartSpendsIt(t *testing.T) {
	g := newGrantRig(t)
	tok := g.render(dmCharter)
	g.answer = false
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.Code != "declined" {
		t.Fatalf("%+v", d)
	}
	g.answer = true
	g.startBehaves(`echo '{"run_id":"work/1"}'`)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); !d.OK {
		t.Fatalf("after a decline the same preview was refused: %+v", d)
	}
	before := g.calls()
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.OK || d.Code != "token" || g.calls() != before {
		t.Fatalf("a confirmed start's token was used again: %+v", d)
	}
}

// A Stop also ends the cached "cannot start here" the entry screen may hold for up to a minute.
func TestStopInvalidatesTheEntryProbeCache(t *testing.T) {
	log, _ := taskCLIFake(t, `{"mode_available":false,"reason":"an open-ended run is already active"}`, 0)
	a := newTaskTestApp(t)
	doomsdayEntry.invalidate("work")
	a.DoomsdayOpenEntry()
	a.DoomsdayStop()
	a.DoomsdayOpenEntry()
	if n := callCount(t, log); n != 3 { // probe, stop, probe again
		t.Fatalf("CLI calls %d, want 3: the cached answer survived the Stop", n)
	}
}

// The last JSON object line wins even when an earlier line of human text looks like JSON.
func TestStartTakesTheLastJSONLineWhenAnEarlierLineLooksLikeJSON(t *testing.T) {
	g := newGrantRig(t)
	g.startBehaves(`printf '{"note":"human line"}\n{"error":"the real refusal","code":"invalid_input"}\n'; exit 2`)
	tok := g.render(dmCharter)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.Code != "invalid_input" || !strings.Contains(d.Detail, "the real refusal") {
		t.Fatalf("%+v", d)
	}
}

// Quitting the app (its context ends) sends the CLI SIGTERM first as a timeout does, so the probe cleans up its unit.
func TestACancelledAppContextTermsTheProbeBeforeKilling(t *testing.T) {
	g := newGrantRig(t)
	mark := g.t.TempDir() + "/termed"
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "trap 'echo term > '"+mark+"'; exit 0' TERM\nsleep 5 &\nwait\n"))
	ctx, cancel := context.WithCancel(context.Background())
	g.a.ctx = ctx
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	argv, _ := doomsdayArgv("work", "probe")
	g.a.execDoomsdayGrace(time.Minute, 3*time.Second, argv, nil)
	if b, _ := os.ReadFile(mark); !strings.Contains(string(b), "term") {
		t.Fatal("quitting did not send SIGTERM first")
	}
}

// A failure with no CLI error envelope can follow a run that already started (an external kill, a native crash, exit 1
// on Windows): only an explicit envelope refusal, or a CLI that could not be run, is "not started".
func TestAFailureWithoutAnEnvelopeIsUnknown(t *testing.T) {
	g := newGrantRig(t)
	for name, c := range map[string]struct{ body, state string }{
		"exit 1 and nothing":      {"exit 1", "unknown"},
		"exit 1 and stderr text":  {"echo 'something broke' >&2; exit 1", "unknown"},
		"NTSTATUS-like exit":      {"exit 255", "unknown"},
		"envelope refusal":        {`echo '{"error":"nothing started","code":"invalid_input"}'; exit 2`, "not_started"},
		"envelope but panic text": {`echo '{"error":"x","code":"e"}'; echo 'panic: boom' >&2; exit 2`, "unknown"},
	} {
		g.startBehaves(c.body)
		tok := g.render(dmCharter)
		if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.State != c.state {
			t.Errorf("%s: %+v", name, d)
		}
	}
}

// The token is checked again when the dialog confirms: a dialog open past its life cannot spend it.
func TestConfirmRechecksTheTokenLifetime(t *testing.T) {
	g := newGrantRig(t)
	g.think = 11 * time.Minute // the person answers after the token expired
	tok := g.render(dmCharter)
	before := g.calls()
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", ""))
	if d.OK || d.Code != "token" || g.calls() != before {
		t.Fatalf("%+v", d)
	}
}

// The dialog also says what the charter lets the boss do by itself.
func TestTheDialogNamesAutoApprovalAndAnswerClasses(t *testing.T) {
	g := newGrantRig(t)
	prev := g.t.TempDir() + "/p.json"
	doc := previewDoc(gHash, []string{"spending"}, "5.000000 USD for the whole run.")
	doc = strings.Replace(doc, `"criteria":2`, `"criteria":2,"auto_approve":"spend 1 USD and 2 tasks per ask, access coding","answer_classes":["scope","spend"]`, 1)
	if err := os.WriteFile(prev, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "cat > /dev/null\ncat '"+prev+"'\n"))
	tok := g.render(dmCharter)
	g.a.DoomsdayStart(dmCharter, phraseFor(1), tok, "chat-5", "")
	body := g.asked[0]
	for _, want := range []string{"spend 1 USD and 2 tasks per ask", "may answer critical asks of class: scope, spend"} {
		if i, j := strings.Index(body, want), strings.Index(body, "Goal as written"); i < 0 || i > j {
			t.Errorf("%q is not before the goal in:\n%s", want, body)
		}
	}
}

// An entry probe that fails after a Stop cleared the cache must not delete the newer entry another caller made.
func TestAStaleProbeFailureDoesNotDeleteANewerEntry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := &entryCache{now: func() time.Time { return now }, ttl: time.Minute}
	release := make(chan struct{})
	started := make(chan struct{})
	go c.get("work", func() (string, bool) { close(started); <-release; return `{"error":"x"}`, false })
	<-started
	c.invalidateAll()
	n := 0
	c.get("work", func() (string, bool) { n++; return `{"mode_available":true}`, true })
	close(release)
	time.Sleep(50 * time.Millisecond)
	c.get("work", func() (string, bool) { n++; return `{"mode_available":true}`, true })
	if n != 1 {
		t.Fatalf("the newer cached answer was deleted by the older failed probe (%d probes)", n)
	}
}

// Previews are one at a time per profile: a second while one runs is refused, not stacked.
func TestOnlyOnePreviewRunsAtATime(t *testing.T) {
	g := newGrantRig(t)
	slow := g.t.TempDir() + "/go"
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "cat > /dev/null\nwhile [ ! -e '"+slow+"' ]; do sleep 0.05; done\ncat /dev/null\necho '{}'\n"))
	done := make(chan string)
	go func() { done <- g.a.DoomsdayRender(dmCharter, "chat-5", "") }()
	time.Sleep(300 * time.Millisecond)
	if got := g.a.DoomsdayRender(dmCharter, "chat-5", ""); !strings.Contains(got, "busy") {
		t.Fatalf("a second preview while one runs: %s", got)
	}
	_ = os.WriteFile(slow, nil, 0o600)
	<-done
}

// A wrong phrase is refused BEFORE the dialog (the person would otherwise confirm a start that must fail); the app derives
// the expected phrase from the CLI's own template, count and hash. Nothing is spent.
func TestAWrongPhraseIsRefusedBeforeTheDialogAndSpendsNothing(t *testing.T) {
	g := newGrantRig(t)
	tok := g.render(dmCharter)
	before := g.calls()
	for _, bad := range []string{"I understand: lift 3 lines, charter ab12cd34", "I understand: lift 2 lines, charter 00000000", "yes"} {
		d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, bad, tok, "chat-5", ""))
		if d.OK || d.Code != "phrase" || g.dialogs != 0 || g.calls() != before {
			t.Fatalf("%q: %+v dialogs %d", bad, d, g.dialogs)
		}
	}
	g.startBehaves(`echo '{"run_id":"work/1"}'`)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); !d.OK {
		t.Fatalf("the token was spent by refused phrases: %+v", d)
	}
}

// What the CLI echoes of the charter on stderr (the render) is not a crash: a goal that says "panic:" with a mistyped
// phrase is still a plain refusal. A real crash line at the start of a line is.
func TestEchoedCharterTextIsNotACrash(t *testing.T) {
	g := newGrantRig(t)
	g.startBehaves(`echo 'Goal: panic: boom goroutine 1 [running]: fatal error: x' >&2; echo '{"error":"the phrase does not match; nothing started","code":"invalid_input"}'; exit 2`)
	tok := g.render(dmCharter)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.State != "not_started" {
		t.Fatalf("%+v", d)
	}
	g.startBehaves(`printf 'panic: runtime error: boom\n\ngoroutine 1 [running]:\n' >&2; echo '{"error":"x","code":"e"}'; exit 2`)
	tok = g.render(dmCharter)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.State != "unknown" {
		t.Fatalf("a real panic: %+v", d)
	}
}

// The stderr kept for the classification is its start and its END: a crash after a big render is not cut off.
func TestStderrKeepsHeadAndTail(t *testing.T) {
	b := &headTailBuffer{head: 100, tail: 100}
	b.Write([]byte(strings.Repeat("a", 5000) + "\nTHE END panic: late\n"))
	s := b.String()
	if !strings.HasPrefix(s, strings.Repeat("a", 100)) || !strings.Contains(s, "panic: late") || len(s) > 400 {
		t.Fatalf("%d bytes: %q", len(s), s)
	}
	small := &headTailBuffer{head: 100, tail: 100}
	small.Write([]byte("short"))
	if small.String() != "short" {
		t.Fatal(small.String())
	}
}

// A dialog that outlives its timeout stays on screen: a second start must not stack another until it is dismissed.
func TestAnAbandonedDialogBlocksANewStartUntilItIsDismissed(t *testing.T) {
	g := newGrantRig(t)
	old := doomsdayDialogTimeout
	doomsdayDialogTimeout = 100 * time.Millisecond
	t.Cleanup(func() { doomsdayDialogTimeout = old })
	release := make(chan struct{})
	confirmNative = func(*App, string, string) (bool, error) { <-release; return false, nil }
	tok := g.render(dmCharter)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.Code != "declined" {
		t.Fatalf("%+v", d)
	}
	tok = g.render(dmCharter)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.Code != "busy" || !strings.Contains(d.Detail, "still open") {
		t.Fatalf("a second start while the first dialog is still on screen: %+v", d)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for dialogOpen.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	confirmNative = func(*App, string, string) (bool, error) { return false, nil }
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", "")); d.Code == "busy" {
		t.Fatalf("still busy after the dialog was dismissed: %+v", d)
	}
}

// A CLI that ignores SIGTERM is killed once the grace has passed, not left running.
func TestAProcessIgnoringSIGTERMIsKilledAfterTheGrace(t *testing.T) {
	g := newGrantRig(t)
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "trap '' TERM\nwhile true; do sleep 0.1; done\n"))
	argv, _ := doomsdayArgv("work", "probe")
	began := time.Now()
	_, _, _, err := g.a.execDoomsdayGrace(200*time.Millisecond, 400*time.Millisecond, argv, nil)
	if err == nil || time.Since(began) > 5*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(began))
	}
}

// The start dialog goes through the same helper: while another dialog is open the start is refused as busy, not hung.
func TestAStartWhileAnotherDialogIsOpenIsBusy(t *testing.T) {
	g := newGrantRig(t)
	confirmNative = realConfirmNative // the real path, through showDialog and its fake runtime
	stuck := make(chan struct{})
	old, oldWait := dialogRun, dialogWait
	dialogRun = func(context.Context, wailsruntime.MessageDialogOptions) (string, error) { <-stuck; return "No", nil }
	dialogWait = 100 * time.Millisecond
	var other sync.WaitGroup
	other.Add(1)
	go func() {
		defer other.Done()
		showDialog(g.a, wailsruntime.MessageDialogOptions{Title: "another dialog"})
	}()
	t.Cleanup(func() { close(stuck); other.Wait(); dialogRun, dialogWait = old, oldWait })
	time.Sleep(50 * time.Millisecond)
	tok := g.render(dmCharter)
	before := g.calls()
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", ""))
	if d.OK || d.Code != "busy" || g.calls() != before {
		t.Fatalf("%+v", d)
	}
}

// The stderr tail starts at a line boundary, not in the middle of a line.
func TestStderrTailStartsAtALineBoundary(t *testing.T) {
	b := &headTailBuffer{head: 10, tail: 40}
	b.Write([]byte("0123456789" + strings.Repeat("x", 500) + "\nthe last line one\nthe last line two\n"))
	s := b.String()
	i := strings.Index(s, "\n...\n")
	if i < 0 || strings.HasPrefix(s[i+5:], "x") || !strings.HasPrefix(s[i+5:], "the last line") {
		t.Fatalf("%q", s)
	}
}

// The unknown outcome shows the CLI's last words, never the whole stderr with the charter echoed in it.
func TestTheUnknownDetailIsOnlyTheTailOfStderr(t *testing.T) {
	g := newGrantRig(t)
	g.startBehaves(`printf 'CHARTER abc\nGoal: the whole echoed goal text\nLifted authority lines (1):\n' >&2; printf 'one\ntwo\nthree\nfour\nfive\nsix\n' >&2; exit 3`)
	tok := g.render(dmCharter)
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-5", ""))
	if d.State != "unknown" || strings.Contains(d.Detail, "echoed goal text") || strings.Contains(d.Detail, "CHARTER") || !strings.Contains(d.Detail, "six") {
		t.Fatalf("%+v", d)
	}
}

// On Windows the CLI's render says a program running as the person can defeat the process check; the native confirmation carries
// that sentence (read from the render output, not written in the app), and no other OS's dialog does.
func TestStartDialogCarriesTheWeakerProcessCheckOnWindowsOnly(t *testing.T) {
	const sentence = "On Windows a program running as you can defeat the process check; Windows is weaker than Linux and macOS."
	for goos, want := range map[string]bool{"windows": true, "linux": false, "darwin": false} {
		doc := previewDoc(gHash, []string{"a", "b"}, "10.000000 USD for the whole run.")
		var m map[string]any
		_ = json.Unmarshal([]byte(doc), &m)
		m["text"] = m["text"].(string) + doomsdaycli.NotDefended(goos) // what the CLI of that OS prints
		raw, _ := json.Marshal(m)
		f, err := factsOf(raw)
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		body := f.dialogBody("a chat with 100% of %s %d")
		if got := strings.Contains(body, sentence); got != want {
			t.Errorf("%s: the dialog body carries the sentence = %v, want %v\n%s", goos, got, want, body)
		}
		if !strings.Contains(body, "a chat with 100% of %s %d") {
			t.Errorf("%s: the chat name was altered by formatting: %s", goos, body)
		}
	}
}

// A Windows app paired with a command-line tool whose render lacks the Windows sentence refuses to start, in plain words,
// instead of showing a dialog without the warning; the other OSes do not need the sentence.
func TestWindowsStartIsRefusedWhenTheRenderLacksTheProcessCheckSentence(t *testing.T) {
	old := dialogGOOS
	defer func() { dialogGOOS = old }()
	doc := []byte(previewDoc(gHash, []string{"a", "b"}, "10.000000 USD for the whole run."))
	for goos, wantErr := range map[string]bool{"windows": true, "linux": false, "darwin": false} {
		dialogGOOS = goos
		_, err := factsOf(doc)
		if (err != nil) != wantErr {
			t.Errorf("%s: err = %v, want error = %v", goos, err, wantErr)
		}
		if wantErr && (err == nil || !strings.Contains(err.Error(), "update the command-line tool")) {
			t.Errorf("%s: the refusal is not plain: %v", goos, err)
		}
	}
	// with the sentence present, Windows is served
	dialogGOOS = "windows"
	var m map[string]any
	_ = json.Unmarshal(doc, &m)
	m["text"] = m["text"].(string) + doomsdaycli.NotDefended("windows")
	raw, _ := json.Marshal(m)
	if _, err := factsOf(raw); err != nil {
		t.Errorf("windows with the sentence: %v", err)
	}
}

// The native confirmation carries the CLI's "running without peer-credential binding" warning, first, and nothing about it when
// the render says none. Revert proof: drop f.unbound from dialogBody (or the unbound_warning field in factsOf): red.
func TestTheDialogBodyShowsTheUnboundWarningFirst(t *testing.T) {
	const w = "running without peer-credential binding: this machine cannot run it (old kernel)"
	var m map[string]any
	if err := json.Unmarshal([]byte(previewDoc(gHash, []string{"spending"}, "5.000000 USD for the whole run.")), &m); err != nil {
		t.Fatal(err)
	}
	m["unbound_warning"] = w
	raw, _ := json.Marshal(m)
	f, err := factsOf(raw)
	if err != nil {
		t.Fatal(err)
	}
	if body := f.dialogBody("chat-1"); !strings.HasPrefix(body, "Running without peer-credential binding: this machine cannot run it (old kernel).") {
		t.Fatalf("the dialog does not lead with the warning:\n%s", body)
	}
	delete(m, "unbound_warning")
	raw, _ = json.Marshal(m)
	f, _ = factsOf(raw)
	if body := f.dialogBody("chat-1"); strings.Contains(body, "peer-credential") {
		t.Fatalf("a bound start's dialog mentions binding:\n%s", body)
	}
}

// The dialog's leading warning is capitalised by rune, not by byte: a reason that starts with a multi-byte letter stays valid UTF-8.
func TestTheDialogUnboundWarningIsRuneSafe(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(previewDoc(gHash, []string{"spending"}, "5.000000 USD for the whole run.")), &m); err != nil {
		t.Fatal(err)
	}
	m["unbound_warning"] = "élan: running without peer-credential binding"
	raw, _ := json.Marshal(m)
	f, err := factsOf(raw)
	if err != nil {
		t.Fatal(err)
	}
	body := f.dialogBody("chat-1")
	if !utf8.ValidString(body) || !strings.HasPrefix(body, "Élan: running without peer-credential binding.") {
		t.Fatalf("%q", body)
	}
}
