//go:build !nosocial

package tiktok

// healthPage is an offline browser.PageInterface for the TikTok methods:
// Eval answers each page script with a canned value chosen by a distinctive
// substring of the script's own body (jsPrelude, prepended to every script,
// is ignored when matching). Unscripted scripts fail like a page script that
// threw, and are logged. No browser, no network.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/browser"
)

type healthCall struct {
	N    int // how often this rule matched before
	URL  string
	Args []interface{}
}

type healthRule struct {
	sub string
	fn  func(c healthCall) interface{}
}

type healthPage struct {
	browser.PageInterface

	mu       sync.Mutex
	url      string
	navs     []string
	rules    []healthRule
	hits     map[string]int
	unknown  []string
	clicks   []string
	keys     []rune
	navErr   error
	loadErr  error
	elemErr  error
	inputs   []string
	setURLOn map[string]string // navigation target -> URL the page lands on
	has      map[string]bool
	race     func(sels []string) (int, error) // nil: the first selector matches
	noElem   map[string]bool                  // selectors Element does not find
}

func newHealthPage(t *testing.T) *healthPage {
	t.Helper()
	p := &healthPage{hits: map[string]int{}, setURLOn: map[string]string{}, has: map[string]bool{}, noElem: map[string]bool{}}
	t.Cleanup(func() {
		for _, u := range p.unknown {
			t.Logf("unscripted page script: %s", u)
		}
	})
	return p
}

func (p *healthPage) on(sub string, fn func(c healthCall) interface{}) *healthPage {
	p.rules = append(p.rules, healthRule{sub, fn})
	return p
}

func (p *healthPage) onValue(sub string, v interface{}) *healthPage {
	return p.on(sub, func(healthCall) interface{} { return v })
}

// onSeq answers the i-th call with vs[i], repeating the last value.
func (p *healthPage) onSeq(sub string, vs ...interface{}) *healthPage {
	return p.on(sub, func(c healthCall) interface{} {
		if c.N < len(vs) {
			return vs[c.N]
		}
		return vs[len(vs)-1]
	})
}

func (p *healthPage) count(sub string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits[sub]
}

func (p *healthPage) navigations() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.navs...)
}

func (p *healthPage) clickCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.clicks)
}

func (p *healthPage) Navigate(u string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.navErr != nil {
		return p.navErr
	}
	p.navs = append(p.navs, u)
	p.url = u
	if to, ok := p.setURLOn[u]; ok {
		p.url = to
	}
	return nil
}

func (p *healthPage) WaitLoad() error { return p.loadErr }

func (p *healthPage) GetURL() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.url, nil
}

func (p *healthPage) Has(sel string) (bool, error) { return p.has[sel], nil }

func (p *healthPage) Race(sels []string, d time.Duration) (int, browser.ElementHandle, error) {
	if p.race == nil {
		return 0, &healthElem{p: p, sel: sels[0]}, nil
	}
	i, err := p.race(sels)
	if err != nil {
		return -1, nil, err
	}
	return i, &healthElem{p: p, sel: sels[i]}, nil
}

func (p *healthPage) KeyboardPress(k rune) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys = append(p.keys, k)
	return nil
}

func (p *healthPage) MouseScroll(x, y float64, steps int) error { return nil }

func (p *healthPage) Element(sel string, d time.Duration) (browser.ElementHandle, error) {
	if p.elemErr != nil {
		return nil, p.elemErr
	}
	if p.noElem[sel] {
		return nil, errors.New("no such element")
	}
	return &healthElem{p: p, sel: sel}, nil
}

func (p *healthPage) Eval(src string, args ...interface{}) (*browser.EvalResult, error) {
	body := src
	if i := strings.Index(src, jsPrelude); i >= 0 {
		body = src[i+len(jsPrelude):]
	}
	p.mu.Lock()
	var rule *healthRule
	for i := range p.rules {
		if strings.Contains(body, p.rules[i].sub) {
			rule = &p.rules[i]
			break
		}
	}
	if rule == nil {
		short := body
		if len(short) > 200 {
			short = short[:200]
		}
		p.unknown = append(p.unknown, short)
		p.mu.Unlock()
		return nil, errors.New("unscripted page script")
	}
	c := healthCall{N: p.hits[rule.sub], URL: p.url, Args: args}
	p.hits[rule.sub]++
	p.mu.Unlock()
	v := rule.fn(c)
	if err, ok := v.(error); ok {
		return nil, err
	}
	return browser.NewEvalResult(v), nil
}

// healthElem is the element a marked-element lookup returns: clicks and
// typed text are recorded on the page.
type healthElem struct {
	browser.ElementHandle
	p   *healthPage
	sel string
}

func (e *healthElem) Click() error {
	e.p.mu.Lock()
	defer e.p.mu.Unlock()
	e.p.clicks = append(e.p.clicks, e.sel)
	return nil
}

func (e *healthElem) Focus() error { return nil }

func (e *healthElem) Input(s string) error {
	e.p.mu.Lock()
	defer e.p.mu.Unlock()
	e.p.inputs = append(e.p.inputs, s)
	return nil
}

// healthFixture loads testdata/health_<name>.json; with key, the value under
// that top-level key (one file holds several scenarios).
func healthFixture(t *testing.T, name, key string) interface{} {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "health_"+name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	if key == "" {
		return v
	}
	m, _ := v.(map[string]interface{})
	sv, ok := m[key]
	if !ok {
		t.Fatalf("fixture %s has no scenario %q", name, key)
	}
	return sv
}

// healthFast makes every wait near-instant.
func healthFast(t *testing.T) {
	t.Helper()
	oldScale := settleScale
	old := []time.Duration{verifyTimeout, findTimeout, pollInterval, publishTimeout}
	settleScale = 0
	verifyTimeout, findTimeout, publishTimeout = 40*time.Millisecond, 40*time.Millisecond, 40*time.Millisecond
	pollInterval = time.Millisecond
	t.Cleanup(func() {
		settleScale = oldScale
		verifyTimeout, findTimeout, pollInterval, publishTimeout = old[0], old[1], old[2], old[3]
	})
}

// Distinctive substrings of the page scripts.
const (
	hsHold          = "window.__monoagentHold.apply(); return true;"
	hsRelease       = "window.__monoagentHold.stop(); return true;"
	hsUnmark        = "forEach(e => e.removeAttribute(MARK)); return true;"
	hsTabs          = `data-e2e="videos-tab"`
	hsUserVideos    = `data-e2e="user-post-item-list"`
	hsScrollWin     = "window.dispatchEvent(new Event('scroll')); return true;"
	hsScrollCmts    = "if (!scrollable(box))"
	hsScrollFollow  = "const box = [pop, ...pop.querySelectorAll('*')]"
	hsInspectCmts   = "if (open) return {open: true, icon: false};"
	hsPanelOpen     = "return commentItems().length > 0 || (!!l && visible(l));"
	hsCommentRead   = "commentItems().map(commentInfo)"
	hsSearch        = "search-card-video-caption"
	hsFollowList    = "const NOT_NAME"
	hsProfile       = `data-e2e="user-bio"`
	hsUniversal     = "__UNIVERSAL_DATA_FOR_REHYDRATION__"
	hsFindVideoLike = "const inView = c.filter"
	hsReadPressed   = "return {count: 1, pressed: pressedOf(e)};"
	hsFindComment   = "const hits = byID.length ? byID : matches;"
	hsCountComments = "return commentItems().length;"
	hsFindFollow    = "if (btns.length === 0 && unf.length === 1)"
	hsReadFollow    = "if (e && visible(e)) return {count: 1, state: classify(e)};"
	hsFindEditor    = "if (ed && visible(ed) && !eds.includes(ed)) eds.push(ed);"
	hsProgress      = "commentInfo(it).text.includes(snip)"
	hsTextOf        = "return e ? textOf(e) : '';"
	hsFindSubmit    = "for (let a = ed && ed.parentElement"
	hsFindOption    = "return {count: 1, state: disabledOf(c[0])"
	hsChatState     = `data-e2e="chat-uniqueid"`
	hsConversations = "chat-list-item-unread"
	hsFindConv      = "return hits.length;"
	hsSelectCaption = "r.selectNodeContents(e)"
)

// scriptOpen answers the housekeeping scripts: holding/releasing videos and
// unmarking succeed.
func (p *healthPage) scriptOpen() *healthPage {
	return p.onValue(hsRelease, true).onValue(hsHold, true).onValue(hsUnmark, true)
}

// echoInput makes textOf(marked) return what was typed last.
func (p *healthPage) echoInput() *healthPage {
	return p.on(hsTextOf, func(healthCall) interface{} {
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.inputs) == 0 {
			return ""
		}
		return p.inputs[len(p.inputs)-1]
	})
}
