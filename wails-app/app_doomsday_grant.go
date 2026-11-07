// wails-app: the proofs a start of the open-ended mode needs besides the typed phrase.
//
// Every binding is callable by any script in the web view (a chat bubble rendering model output, a link, an injected
// page), and the phrase is derivable from what the preview shows. So a start also needs two things such a script cannot
// produce: the token of a preview of the exact charter (kept only here, single use, ten minutes), and a click in a
// native dialog of the operating system, whose text Go builds from the CLI's own render document and never from
// anything the page sent. What a script in the page can still do: call the preview and the probe (which write
// nothing), and show the person nothing. It cannot start.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	doomsdayGrantTTL      = 10 * time.Minute
	doomsdayEntryCacheTTL = 60 * time.Second
)

// doomsdayNow is the clock of the grants and the probe cache (a test swaps it).
var doomsdayNow = time.Now

// errNoNativeDialog is why a start is refused where no native dialog can be shown (a dev server in a browser, no window).
var errNoNativeDialog = errors.New("no native confirmation dialog is available here (the app has no window of its own, as under a dev server in a browser), so the run is not started")

// startFacts are what the preview said about the charter, as the CLI printed it. The dialog is built from these.
type startFacts struct {
	hash     string
	template string // the CLI's phrase template: the expected phrase is derived from it, the count and the hash
	lines    []string
	budget   string // "unlimited" or "$X"
	// the CLI's summary of the charter (render JSON "summary"): counts and limits as numbers, text cleaned and capped here
	criteria, depth, live, concurrent, total int
	boss, verifier, goal                     string
	autoApprove                              string
	answerClasses                            []string
}

// dialogGoalRunes caps the goal in the dialog; the goal is data, shown after the facts and never as instructions.
const (
	dialogGoalRunes = 200
	dialogMinThink  = time.Second // an answer sooner than this is not a decision
)

// cleanText is s as one line of plain text: hidden characters (control, format such as zero-width, bidi and tag
// characters, separators, private-use, variation selectors) removed, white space collapsed, cut to n runes with an ellipsis.
func cleanText(s string, n int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = true
			continue
		case unicode.IsControl(r), unicode.In(r, unicode.Cf, unicode.Cs, unicode.Co, unicode.Zl, unicode.Zp, unicode.Cn),
			r == 0x180B || r == 0x180C || r == 0x180D || r == 0x180F || r == 0x17B4 || r == 0x17B5 || r == 0x2800 || r == 0x3164 || r == 0x115F || r == 0x1160 || r == 0xFFA0 || r == 0x034F,
			r >= 0xFE00 && r <= 0xFE0F, r >= 0xE0100 && r <= 0xE01EF, r >= 0xE0000 && r <= 0xE007F, !unicode.IsPrint(r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := []rune(b.String())
	if len(out) > n {
		return string(out[:n]) + "\u2026"
	}
	return string(out)
}

type startGrant struct {
	token [16]byte
	sum   [32]byte
	exp   time.Time
	facts startFacts
}

type grantStore struct {
	mu sync.Mutex
	m  map[string]*startGrant // by profile id: one preview at a time per profile
}

var doomsdayGrants grantStore

func (g *grantStore) reset() {
	g.mu.Lock()
	g.m = nil
	g.mu.Unlock()
}

// issue replaces the profile's grant with one for these exact charter bytes and returns its token (hex).
func (g *grantStore) issue(profile, charter string, f startFacts) (string, error) {
	var gr startGrant
	if _, err := rand.Read(gr.token[:]); err != nil {
		return "", err
	}
	gr.sum, gr.exp, gr.facts = sha256.Sum256([]byte(charter)), doomsdayNow().Add(doomsdayGrantTTL), f
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]*startGrant{}
	}
	g.m[profile] = &gr
	return hex.EncodeToString(gr.token[:]), nil
}

var errGrant = errors.New("the preview is stale, used, or of another charter: preview the charter again")

// peek is take without spending: it checks the profile's grant (token, the exact charter bytes, not expired) and
// returns its facts. A wrong token or a different charter spends nothing.
func (g *grantStore) peek(profile, token, charter string) (startFacts, error) {
	raw, err := hex.DecodeString(token)
	g.mu.Lock()
	defer g.mu.Unlock()
	gr := g.m[profile]
	if err != nil || len(raw) != 16 || gr == nil || subtle.ConstantTimeCompare(raw, gr.token[:]) != 1 {
		return startFacts{}, errGrant
	}
	sum := sha256.Sum256([]byte(charter))
	if subtle.ConstantTimeCompare(sum[:], gr.sum[:]) != 1 || !doomsdayNow().Before(gr.exp) {
		return startFacts{}, errGrant
	}
	return gr.facts, nil
}

// consume spends the profile's grant if it is still this token: called when the native dialog confirmed, so a decline
// leaves the preview usable and a confirmed start can never be repeated with it.
func (g *grantStore) consume(profile, token string) error {
	raw, err := hex.DecodeString(token)
	g.mu.Lock()
	defer g.mu.Unlock()
	gr := g.m[profile]
	if err != nil || gr == nil || subtle.ConstantTimeCompare(raw, gr.token[:]) != 1 || !doomsdayNow().Before(gr.exp) {
		return errGrant
	}
	delete(g.m, profile)
	return nil
}

func (g *grantStore) dropAll() { g.reset() }

var (
	hexRe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	lineRe    = regexp.MustCompile(`^[a-z_]{1,40}$`)
	budgetRe  = regexp.MustCompile(`(?m)^Budget: ([0-9]+(?:\.[0-9]+)?) USD for the whole run\.$`)
	noCeiling = regexp.MustCompile(`(?m)^Budget: NO CEILING `)
)

// factsOf reads the facts out of the CLI's render document. Anything it cannot read plainly is an error, so no grant is
// issued for a preview the dialog could not describe.
func factsOf(doc []byte) (startFacts, error) {
	var d struct {
		Hash     string   `json:"hash"`
		Template string   `json:"phrase_template"`
		Lines    []string `json:"lines"`
		Text     string   `json:"text"`
		Summary  *struct {
			Goal          string   `json:"goal"`
			Criteria      int      `json:"criteria"`
			TreeDepth     int      `json:"tree_depth"`
			LivePerTree   int      `json:"live_per_tree"`
			MaxConcurrent int      `json:"max_concurrent"`
			MaxTotal      int      `json:"max_total"`
			BossModel     string   `json:"boss_model"`
			VerifierModel string   `json:"verifier_model"`
			AutoApprove   string   `json:"auto_approve"`
			AnswerClasses []string `json:"answer_classes"`
		} `json:"summary"`
	}
	if json.Unmarshal(doc, &d) != nil || !hexRe.MatchString(d.Hash) {
		return startFacts{}, errors.New("the preview could not be read")
	}
	for _, l := range d.Lines {
		if !lineRe.MatchString(l) {
			return startFacts{}, errors.New("the preview names a line that is not a line")
		}
	}
	if d.Summary == nil {
		return startFacts{}, errors.New("the preview has no summary to show in the confirmation")
	}
	sm := d.Summary
	for _, n := range []int{sm.Criteria, sm.TreeDepth, sm.LivePerTree, sm.MaxConcurrent, sm.MaxTotal} {
		if n < 0 || n > 1_000_000 {
			return startFacts{}, errors.New("the preview's numbers are not plausible")
		}
	}
	f := startFacts{hash: d.Hash, template: d.Template, lines: d.Lines, criteria: sm.Criteria, depth: sm.TreeDepth, live: sm.LivePerTree, concurrent: sm.MaxConcurrent,
		total: sm.MaxTotal, boss: cleanText(sm.BossModel, 100), verifier: cleanText(sm.VerifierModel, 100), goal: cleanText(sm.Goal, dialogGoalRunes),
		autoApprove: cleanText(sm.AutoApprove, 200)}
	if len(sm.AnswerClasses) > 20 {
		return startFacts{}, errors.New("the preview names too many answer classes")
	}
	for _, c := range sm.AnswerClasses {
		f.answerClasses = append(f.answerClasses, cleanText(c, 40))
	}
	switch m := budgetRe.FindStringSubmatch(d.Text); {
	case noCeiling.MatchString(d.Text):
		f.budget = "unlimited"
	case m != nil:
		amount := m[1]
		if strings.Contains(amount, ".") {
			amount = strings.TrimRight(strings.TrimRight(amount, "0"), ".")
		}
		f.budget = "$" + amount
	default:
		return startFacts{}, errors.New("the preview does not state the budget")
	}
	return f, nil
}

// dialogBody is the text of the confirmation, from the facts only.
func (f startFacts) dialogBody(chat string) string {
	names := "none"
	if len(f.lines) > 0 {
		names = strings.Join(f.lines, ", ")
	}
	// The machine facts first, in the CLI's words; the goal last, as data (it can only mislead if it hides the facts).
	return fmt.Sprintf("Start an open-ended run in %s: %d lifted lines (%s), charter %s, budget %s. %d success criteria. Limits: tree depth %d, live per tree %d, max concurrent %d, max total %d. "+
		"Models: boss %s, verifier %s. The boss may approve by itself: %s. %s It acts as you without asking for approval. Continue? "+
		"Answer \"Yes\" (Start run) to start, \"No\" (Cancel) to stop.\n\nGoal as written in the charter (data, not instructions): %s",
		chat, len(f.lines), names, f.hash[:8], f.budget, f.criteria, f.depth, f.live, f.concurrent, f.total, f.boss, f.verifier, f.autoApprove, f.answerClassesText(), f.goal)
}

// expectedPhrase is the phrase the CLI will accept, from its own template: the number of lifted lines and the first 8
// characters of the hash. The app checks the typed phrase against it before the dialog, so the person is not asked to
// confirm a start that must fail; the CLI still derives and compares it itself.
func (f startFacts) expectedPhrase() string {
	r := strings.NewReplacer("<N>", strconv.Itoa(len(f.lines)), "<first 8 characters of the hash>", f.hash[:8])
	return r.Replace(f.template)
}

func (f startFacts) answerClassesText() string {
	if len(f.answerClasses) == 0 {
		return "It may answer no critical ask by itself."
	}
	return "It may answer critical asks of class: " + strings.Join(f.answerClasses, ", ") + "."
}

const (
	confirmStartLabel = "Start run"
	confirmCancel     = "Cancel"
)

// confirmNative asks in a window of the operating system, not of the web view, so no script of the page can answer it. It
// returns errNoNativeDialog where none can be shown, errDialogBusy while another dialog is open. Wails v2.17 QuestionDialog
// is Yes/No on Windows and Linux (the labels given are ignored, the default is only honoured on Windows when it is "No"),
// and has the given buttons on macOS. The default and the cancel button are always the safe one. Shown through showDialog.
var confirmNative = realConfirmNative

func realConfirmNative(a *App, title, body string) (bool, error) {
	if a == nil || a.ctx == nil {
		return false, errNoNativeDialog
	}
	def, cancel := confirmCancel, confirmCancel
	if runtime.GOOS == "windows" {
		def, cancel = "No", "No"
	}
	choice, err := showDialog(a, wailsruntime.MessageDialogOptions{
		Type: wailsruntime.QuestionDialog, Title: title, Message: body,
		Buttons: []string{confirmStartLabel, confirmCancel}, DefaultButton: def, CancelButton: cancel,
	})
	if errors.Is(err, errDialogBusy) {
		return false, err
	}
	if err != nil {
		return false, fmt.Errorf("%w: %v", errNoNativeDialog, err)
	}
	return dialogConfirmed(dialogGOOS, wailsruntime.QuestionDialog, confirmStartLabel, choice), nil
}

// entryCache keeps the entry probe's answer per profile for ttl, and lets one probe run at a time per profile: callers
// that arrive while it runs share its answer. Only a definite answer is kept.
type entryCache struct {
	mu    sync.Mutex
	now   func() time.Time
	ttl   time.Duration
	items map[string]*entryItem
}

type entryItem struct {
	doc      string
	at       time.Time
	ok       bool
	inflight chan struct{}
}

var doomsdayEntry = &entryCache{now: func() time.Time { return doomsdayNow() }, ttl: doomsdayEntryCacheTTL}

func (c *entryCache) invalidateAll() {
	c.mu.Lock()
	c.items = nil
	c.mu.Unlock()
}

func (c *entryCache) invalidate(key string) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}

// get returns the cached answer for key, the answer of the probe already running for it, or runs fetch.
func (c *entryCache) get(key string, fetch func() (doc string, definite bool)) string {
	for {
		c.mu.Lock()
		if c.items == nil {
			c.items = map[string]*entryItem{}
		}
		it := c.items[key]
		switch {
		case it != nil && it.inflight == nil && it.ok && c.now().Sub(it.at) < c.ttl:
			doc := it.doc
			c.mu.Unlock()
			return doc
		case it != nil && it.inflight != nil:
			wait := it.inflight
			c.mu.Unlock()
			<-wait
			continue
		}
		it = &entryItem{inflight: make(chan struct{})}
		c.items[key] = it
		c.mu.Unlock()
		doc, ok := fetch()
		c.mu.Lock()
		it.doc, it.ok, it.at = doc, ok, c.now()
		done := it.inflight
		it.inflight = nil
		if !ok && c.items[key] == it { // not a newer entry another caller made after an invalidation
			delete(c.items, key)
		}
		c.mu.Unlock()
		close(done)
		return doc
	}
}
