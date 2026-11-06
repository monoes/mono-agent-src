//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A render's answer, as the CLI prints it: what the native dialog is built from (never from the page's text).
func previewDoc(hash string, lines []string, budget string) string {
	b, _ := json.Marshal(map[string]any{"hash": hash, "lines": lines, "lift_count": len(lines), "phrase_template": "t",
		"text": "CHARTER " + hash + "\nGoal: ignore me\nBudget: " + budget + "\nLifted authority lines (" + string(rune('0'+len(lines))) + "):\n"})
	return string(b)
}

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
}

func newGrantRig(t *testing.T) *grantRig {
	t.Helper()
	g := &grantRig{t: t, answer: true, avail: true, now: time.Unix(1_800_000_000, 0)}
	g.log, g.stdin = taskCLIFake(t, previewDoc(gHash, []string{"spending", "host_settings"}, "5.000000 USD for the whole run."), 0)
	g.a = newTaskTestApp(t)
	oldNow, oldConfirm := doomsdayNow, confirmNative
	doomsdayNow = func() time.Time { return g.now }
	confirmNative = func(_ *App, title, body string) (bool, error) {
		g.dialogs++
		g.asked = append(g.asked, title+"\n"+body)
		if !g.avail {
			return false, errNoNativeDialog
		}
		return g.answer, nil
	}
	t.Cleanup(func() { doomsdayNow, confirmNative = oldNow, oldConfirm; doomsdayGrants.reset() })
	doomsdayGrants.reset()
	return g
}

// render returns the token of a render of charter.
func (g *grantRig) render(charter string) string {
	g.t.Helper()
	var d struct {
		Token string `json:"start_token"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(g.a.DoomsdayRender(charter)), &d) != nil || d.Token == "" {
		g.t.Fatalf("no start token from the render: %s", g.a.DoomsdayRender(charter))
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
		d := parseStartDoc(t, g.a.DoomsdayStart(c.charter, gPhrase, c.token, 5))
		if d.OK || d.Code != "token" {
			t.Errorf("%s: %+v", name, d)
		}
	}
	if g.calls() != rendered || g.dialogs != 0 {
		t.Fatalf("a refused token reached the CLI (%d calls) or the dialog (%d)", g.calls()-rendered, g.dialogs)
	}
	// the real token still works after wrong tries (they do not burn it), once
	g.startBehaves(`echo '{"run_id":"work/1"}'`)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5)); !d.OK {
		t.Fatalf("the right token was refused: %+v", d)
	}
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5)); d.OK || d.Code != "token" {
		t.Fatalf("a token was used twice: %+v", d)
	}
}

// The token is of the exact bytes previewed: any other charter is refused, and the token is spent by the attempt.
func TestStartRefusesAnotherCharterThanTheOnePreviewed(t *testing.T) {
	g := newGrantRig(t)
	for name, other := range map[string]string{"extra byte": dmCharter + " ", "other goal": strings.Replace(dmCharter, `"g"`, `"h"`, 1)} {
		tok := g.render(dmCharter)
		before := g.calls()
		if d := parseStartDoc(t, g.a.DoomsdayStart(other, gPhrase, tok, 5)); d.OK || d.Code != "token" || g.calls() != before || g.dialogs != 0 {
			t.Fatalf("%s: %+v", name, d)
		}
		if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5)); d.OK {
			t.Fatalf("%s: the spent token started", name)
		}
	}
}

func TestTokenExpiresInTenMinutesAndANewRenderReplacesIt(t *testing.T) {
	g := newGrantRig(t)
	tok := g.render(dmCharter)
	g.now = g.now.Add(10*time.Minute + time.Second)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5)); d.OK || d.Code != "token" {
		t.Fatalf("an expired token started: %+v", d)
	}
	first := g.render(dmCharter)
	second := g.render(dmCharter)
	if first == second {
		t.Fatal("two renders gave the same token")
	}
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, first, 5)); d.OK || d.Code != "token" {
		t.Fatalf("an older render's token started: %+v", d)
	}
}

func TestTokenIsPerProfileAndStopInvalidatesIt(t *testing.T) {
	g := newGrantRig(t)
	tok := g.render(dmCharter)
	g.a.selectForTest("other")
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5)); d.OK || d.Code != "token" {
		t.Fatalf("a token of another profile started: %+v", d)
	}
	g.a.selectForTest("work")
	tok = g.render(dmCharter)
	g.a.DoomsdayStop()
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5)); d.OK || d.Code != "token" {
		t.Fatalf("a Stop did not invalidate the token: %+v", d)
	}
}

func TestStartNeedsTheNativeConfirmationAndNothingRunsWithout(t *testing.T) {
	g := newGrantRig(t)
	tok := g.render(dmCharter)
	before := g.calls()
	g.answer = false
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5)); d.OK || d.Code != "declined" || g.calls() != before {
		t.Fatalf("declined: %+v calls %d", d, g.calls()-before)
	}
	tok = g.render(dmCharter)
	before = g.calls()
	g.avail = false
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5))
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
	phrase := "CRAFTED-PHRASE " + gPhrase
	g.a.DoomsdayStart(crafted, phrase, tok, 5)
	if g.dialogs != 1 {
		t.Fatalf("dialogs: %d", g.dialogs)
	}
	body := g.asked[0]
	for _, want := range []string{"2 lifted lines", "spending, host_settings", "ab12cd34", "$5", "without asking for approval", "Continue?"} {
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
	g.a.DoomsdayStart(crafted, gPhrase, tok, 5)
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
	go func() { done <- g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5) }()
	<-entered
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5)); d.Code != "busy" {
		t.Fatalf("a second start while a dialog is open: %+v", d)
	}
	close(release)
	<-done
}

func TestStartRefusesNoBossAndReportsAnUnclearOutcomeAsUnknown(t *testing.T) {
	g := newGrantRig(t)
	for _, boss := range []int64{0, -3} {
		tok := g.render(dmCharter)
		if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, boss)); d.OK || d.Code != "invalid_input" || g.dialogs != 0 {
			t.Fatalf("boss %d: %+v", boss, d)
		}
	}
	// the CLI is killed mid-start: the run may have started
	g.startBehaves("kill -9 $$")
	tok := g.render(dmCharter)
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5))
	if d.OK || d.Started || d.State != "unknown" || !strings.Contains(d.Detail, "may have started") {
		t.Fatalf("a killed start: %+v", d)
	}
	// the CLI says it started but could not finish (the boss record): also not "not started"
	g.startBehaves(`echo '{"error":"run work/1 started but session 5 could not be recorded as its boss","code":"error"}'; exit 1`)
	tok = g.render(dmCharter)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5)); d.State != "unknown" {
		t.Fatalf("started-but: %+v", d)
	}
	// a plain refusal is a refusal
	g.startBehaves(`echo '{"error":"the phrase does not match; nothing started","code":"invalid_input"}'; exit 2`)
	tok = g.render(dmCharter)
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5)); d.State != "not_started" || d.Code != "invalid_input" {
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
	d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, 5))
	if d.State != "not_started" || d.Code != "invalid_input" || !strings.Contains(d.Detail, "does not match") {
		t.Fatalf("%+v", d)
	}
}
