//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

type pursuitRig struct {
	t       *testing.T
	a       *App
	log     string
	asked   []string
	answer  bool
	avail   bool
	now     time.Time
	think   time.Duration
	dialogs int
}

func newPursuitRig(t *testing.T) *pursuitRig {
	t.Helper()
	r := &pursuitRig{t: t, answer: true, avail: true, now: time.Unix(1_800_000_000, 0), think: 5 * time.Second}
	r.cli(pursuitPreviewJSON(t, nil), `echo '{"profile":{"id":"work"},"pursuit":{"session_id":5},"goal":{"state":"locked"},"via":"gui"}'`)
	r.a = newTaskTestApp(t)
	oldNow, oldConfirm := doomsdayNow, confirmNative
	doomsdayNow = func() time.Time { return r.now }
	confirmNative = func(_ *App, title, body string) (bool, error) {
		r.dialogs++
		r.asked = append(r.asked, title+"\n"+body)
		r.now = r.now.Add(r.think)
		if !r.avail {
			return false, errNoNativeDialog
		}
		return r.answer, nil
	}
	t.Cleanup(func() {
		doomsdayNow, confirmNative = oldNow, oldConfirm
		pursuitGrants.reset()
		pursuitPreviews.reset()
	})
	pursuitGrants.reset()
	pursuitPreviews.reset()
	dialogOpen.Store(false)
	dialogShown.Store(0)
	return r
}

// cli installs a CLI that logs its argv and answers a preview with prev and a begin with the shell body.
func (r *pursuitRig) cli(prev, begin string) {
	r.t.Helper()
	dir := r.t.TempDir()
	if r.log == "" {
		r.log = dir + "/args.log"
	}
	pf := dir + "/preview.json"
	if err := os.WriteFile(pf, []byte(prev), 0o600); err != nil {
		r.t.Fatal(err)
	}
	script := "for a in \"$@\"; do printf '%s|' \"$a\"; done >> '" + r.log + "'\necho >> '" + r.log + "'\n" +
		"case \"$*\" in *' preview '*) cat '" + pf + "'; if grep -q '^{\"error\"' '" + pf + "'; then exit 1; fi;; *)\n" + begin + "\n;; esac\n"
	r.t.Setenv("MONOAGENTCLI_BIN", fakeCLI(r.t, script))
}

func (r *pursuitRig) calls() int { return callCount(r.t, r.log) }

func (r *pursuitRig) preview(l PursuitLimits) (token string, doc string) {
	r.t.Helper()
	pursuitPreviews.reset() // the rate limit has its own test
	doc = r.a.PursuitStartPreview(5, l)
	var d struct {
		Token string `json:"start_token"`
	}
	if json.Unmarshal([]byte(doc), &d) != nil || d.Token == "" {
		r.t.Fatalf("no start token: %s", doc)
	}
	return d.Token, doc
}

func (r *pursuitRig) begins() int {
	n := 0
	for _, l := range loggedArgs(r.t, r.log) {
		if strings.Contains(l, "|begin|") {
			n++
		}
	}
	return n
}

func (r *pursuitRig) start(token string) startResult {
	r.t.Helper()
	return parseStart(r.t, r.a.PursuitStart(5, pLimits, pursuitPhrase(5), token))
}

type startResult = struct {
	OK      bool   `json:"ok"`
	Started bool   `json:"started"`
	Output  string `json:"output"`
	Detail  string `json:"detail"`
	Code    string `json:"code"`
	State   string `json:"state"`
}

func parseStart(t *testing.T, s string) (d startResult) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatalf("not a start document: %q", s)
	}
	return d
}

// A preview runs the fixed read argv, answers with the CLI's document plus a token; a start runs the fixed begin argv built from
// what the preview resolved, the phrase in the --flag=value form, and nothing the page sent is a flag.
func TestPursuitPreviewAndStartRunTheExactArgv(t *testing.T) {
	r := newPursuitRig(t)
	tok, doc := r.preview(PursuitLimits{Pct: 25})
	if !strings.Contains(doc, `"consent_digest"`) || len(tok) != 32 {
		t.Fatalf("the document: %s", doc)
	}
	d := r.start(tok)
	if !d.OK || !d.Started || d.State != "started" || !strings.Contains(d.Output, `"via":"gui"`) {
		t.Fatalf("%+v", d)
	}
	want := []string{
		"--profile|work|--json|task|session|pursuit|preview|5|--pct=25|",
		"--profile|work|--json|task|session|pursuit|begin|5|--goal-hash=" + pHash + "|--consent=" + pDigest + "|--chat=" + pChat +
			"|--parked-max=4|--pct=25|--wakes-max=7|--gui-phrase=" + pursuitPhrase(5) + "|",
	}
	if got := loggedArgs(t, r.log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv:\n%q\nwant:\n%q", got, want)
	}
}

// What the page cannot be trusted with never reaches the CLI: a refused input runs neither the CLI nor the dialog.
func TestPursuitStartRefusesBadInputBeforeAnything(t *testing.T) {
	r := newPursuitRig(t)
	tok, _ := r.preview(pLimits)
	before := r.calls()
	good := pursuitPhrase(5)
	for name, c := range map[string]struct {
		session int64
		lim     PursuitLimits
		phrase  string
	}{
		"no session":       {0, pLimits, good},
		"negative":         {-3, pLimits, good},
		"zero limit":       {5, PursuitLimits{Pct: 25, WakesMax: 7}, good},
		"negative limit":   {5, PursuitLimits{ParkedMax: -1, Pct: 25, WakesMax: 7}, good},
		"absurd limit":     {5, PursuitLimits{ParkedMax: 4, Pct: 25, WakesMax: pursuitSane + 1}, good},
		"empty phrase":     {5, pLimits, ""},
		"newline phrase":   {5, pLimits, good + "\nmore"},
		"long phrase":      {5, pLimits, strings.Repeat("a", doomsdayPhraseMax+1)},
		"dash flag phrase": {5, pLimits, "--yes"},
	} {
		d := parseStart(t, r.a.PursuitStart(c.session, c.lim, c.phrase, tok))
		if d.OK || (d.Code != "invalid_input" && d.Code != "phrase") {
			t.Errorf("%s: %+v", name, d)
		}
	}
	if r.calls() != before || r.dialogs != 0 {
		t.Fatal("the CLI or the dialog ran for a refused input")
	}
	if bad := r.a.PursuitStartPreview(5, PursuitLimits{Pct: -1}); !strings.Contains(bad, "invalid_input") || r.calls() != before {
		t.Fatalf("a negative preview limit: %s", bad)
	}
}

// Token: wrong, expired (the clock seam), reused, of another session, for other limits, replaced by a newer preview. A decline keeps
// the grant; a yes spends it.
func TestPursuitStartTokens(t *testing.T) {
	r := newPursuitRig(t)
	tok, _ := r.preview(pLimits)
	prior := r.calls()
	for name, c := range map[string]struct {
		token string
		lim   PursuitLimits
		sess  int64
	}{
		"no token":      {"", pLimits, 5},
		"wrong token":   {strings.Repeat("0", 32), pLimits, 5},
		"short token":   {"abcd", pLimits, 5},
		"not hex":       {strings.Repeat("z", 32), pLimits, 5},
		"other limits":  {tok, PursuitLimits{ParkedMax: 4, Pct: 26, WakesMax: 7}, 5},
		"other parked":  {tok, PursuitLimits{ParkedMax: 5, Pct: 25, WakesMax: 7}, 5},
		"other wakes":   {tok, PursuitLimits{ParkedMax: 4, Pct: 25, WakesMax: 8}, 5},
		"other session": {tok, pLimits, 6},
	} {
		d := parseStart(t, r.a.PursuitStart(c.sess, c.lim, pursuitPhrase(c.sess), c.token))
		if d.OK || d.Code != "token" {
			t.Errorf("%s: %+v", name, d)
		}
	}
	if r.calls() != prior || r.dialogs != 0 {
		t.Fatal("a refused token reached the CLI or the dialog")
	}
	// the refusals did not burn it; a decline does not either; a yes spends it
	r.answer = false
	if d := r.start(tok); d.Code != "declined" || r.dialogs != 1 {
		t.Fatalf("decline: %+v", d)
	}
	r.answer = true
	if d := r.start(tok); !d.OK {
		t.Fatalf("the grant did not survive a decline: %+v", d)
	}
	if d := r.start(tok); d.OK || d.Code != "token" {
		t.Fatalf("a spent token started again: %+v", d)
	}
	if r.begins() != 1 {
		t.Fatalf("begin ran %d times", r.begins())
	}
}

func TestPursuitTokenExpiresAndANewPreviewReplacesIt(t *testing.T) {
	r := newPursuitRig(t)
	tok, _ := r.preview(pLimits)
	r.now = r.now.Add(doomsdayGrantTTL + time.Second)
	if d := r.start(tok); d.Code != "token" || r.dialogs != 0 {
		t.Fatalf("an expired token: %+v", d)
	}
	old, _ := r.preview(pLimits)
	newer, _ := r.preview(pLimits)
	if old == newer {
		t.Fatal("two previews, one token")
	}
	if d := r.start(old); d.Code != "token" || r.dialogs != 0 {
		t.Fatalf("the replaced token started: %+v", d)
	}
	if d := r.start(newer); !d.OK {
		t.Fatalf("the newer token: %+v", d)
	}
}

// The token is bound to the exact preview: a preview of the same session whose consent digest changed is another grant, and a token
// of one profile is nothing for another.
func TestPursuitTokenIsPerProfile(t *testing.T) {
	r := newPursuitRig(t)
	tok, _ := r.preview(pLimits)
	r.a.selectForTest("other")
	if d := r.start(tok); d.Code != "token" || r.dialogs != 0 {
		t.Fatalf("another profile: %+v", d)
	}
}

// confirmNative false / error / busy / too fast: the CLI never begins. Mutating the answer to yes turns this red.
func TestStartNeedsTheNativeConfirmation(t *testing.T) {
	r := newPursuitRig(t)
	tok, _ := r.preview(pLimits)
	prior := r.calls()
	check := func(name, code string) {
		t.Helper()
		if d := r.start(tok); d.OK || d.Started || d.State != "not_started" || d.Code != code || r.calls() != prior {
			t.Fatalf("%s: %+v (calls %d, want %d)", name, d, r.calls(), prior)
		}
	}
	r.answer = false
	check("declined", "declined")
	r.answer, r.avail = true, false
	check("no dialog", "no_native_dialog")
	r.avail, r.think = true, pursuitMinThink/2
	check("answered under the minimum", "declined")
	r.think = pursuitMinThink   // exactly the minimum counts
	tok, _ = r.preview(pLimits) // two declines dropped the grant
	if d := r.start(tok); !d.OK || r.begins() != 1 {
		t.Fatalf("at the minimum: %+v", d)
	}
}

func TestPursuitStartWhileADialogIsOpenIsBusy(t *testing.T) {
	r := newPursuitRig(t)
	tok, _ := r.preview(pLimits)
	prior := r.calls()
	entered, release := make(chan struct{}), make(chan struct{})
	confirmNative = func(*App, string, string) (bool, error) { close(entered); <-release; return false, nil }
	done := make(chan string)
	go func() { done <- r.a.PursuitStart(5, pLimits, pursuitPhrase(5), tok) }()
	<-entered
	if d := parseStart(t, r.a.PursuitStart(5, pLimits, pursuitPhrase(5), tok)); d.Code != "busy" || d.OK {
		t.Fatalf("a second start with a dialog open: %+v", d)
	}
	close(release)
	<-done
	if r.calls() != prior {
		t.Fatal("the CLI ran")
	}
}

func TestOnlyOneStartAtATime(t *testing.T) {
	r := newPursuitRig(t)
	tok, _ := r.preview(pLimits)
	prior := r.calls()
	entered, release := make(chan struct{}, 6), make(chan struct{})
	confirmNative = func(*App, string, string) (bool, error) { entered <- struct{}{}; <-release; return false, nil }
	results := make(chan startResult, 6)
	for range 6 {
		go func() { results <- parseStart(t, r.a.PursuitStart(5, pLimits, pursuitPhrase(5), tok)) }()
	}
	// while the first one waits in its dialog, the other five are refused at once
	for range 5 {
		if d := <-results; d.Code != "busy" || d.OK {
			t.Fatalf("a concurrent start: %+v", d)
		}
	}
	close(release)
	if d := <-results; d.Code != "declined" || len(entered) != 1 || r.calls() != prior {
		t.Fatalf("the one that held the dialog: %+v (dialogs %d)", d, len(entered))
	}
}

// Where the app was started from an agent's shell (opgate) every binding refuses before a grant is read or the CLI runs.
func TestStartRefusesWhereAnAgentStartedTheApp(t *testing.T) {
	r := newPursuitRig(t)
	tok, _ := r.preview(pLimits)
	prior := r.calls()
	opgate.SetForTest(t, func(bool) string { return "an agent's shell" })
	if d := r.start(tok); d.OK || d.Code != "operator_only" {
		t.Fatalf("start: %+v", d)
	}
	if got := r.a.PursuitStartPreview(5, PursuitLimits{}); !strings.Contains(got, "operator_only") {
		t.Fatalf("preview: %q", got)
	}
	if r.calls() != prior || r.dialogs != 0 {
		t.Fatal("the CLI or the dialog ran for a refused caller")
	}
	opgate.SetForTest(t, func(bool) string { return "" })
	if d := r.start(tok); !d.OK { // the refusal read nothing and spent nothing
		t.Fatalf("the grant was disturbed by a refused caller: %+v", d)
	}
}
