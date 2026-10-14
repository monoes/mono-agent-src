//go:build !nosocial

package instagram

// healthPage is an offline browser.PageInterface for the Instagram methods:
// Eval answers each page script with a canned value chosen by a distinctive
// substring of the script's own body (jsLib, prepended to every script, is
// ignored when matching). Unscripted scripts fail like a page script that
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
}

func newHealthPage(t *testing.T) *healthPage {
	t.Helper()
	p := &healthPage{hits: map[string]int{}, setURLOn: map[string]string{}}
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

func (p *healthPage) Has(sel string) (bool, error) { return false, nil }

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
	return &healthElem{p: p, sel: sel}, nil
}

func (p *healthPage) Eval(src string, args ...interface{}) (*browser.EvalResult, error) {
	body := src
	if i := strings.LastIndex(src, "\nreturn ("); i >= 0 {
		body = src[i:]
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
	old := []time.Duration{pageSettle, uiPause, findTimeout, verifyTimeout, pollEvery, storyDwell, scrollSettle, sendVerificationTimeout}
	pageSettle, uiPause, storyDwell, scrollSettle = 0, 0, 0, 0
	findTimeout, verifyTimeout, sendVerificationTimeout = 40*time.Millisecond, 40*time.Millisecond, 40*time.Millisecond
	pollEvery = time.Millisecond
	t.Cleanup(func() {
		pageSettle, uiPause, findTimeout, verifyTimeout = old[0], old[1], old[2], old[3]
		pollEvery, storyDwell, scrollSettle, sendVerificationTimeout = old[4], old[5], old[6], old[7]
	})
}

// Distinctive substrings of the page scripts.
const (
	hsLogin     = "location.pathname.startsWith('/accounts/login')"
	hsNotNow    = "/^not now$/i"
	hsUnmark    = "forEach((e) => e.removeAttribute(MARK)); return true;"
	hsGone      = "post isn.t available/i.test(VT())"
	hsStore     = "getRecordIDs"
	hsFetchAPI  = "web_profile_info/?username="
	hsDOM       = "og_description: meta('og:description')"
	hsHeader    = "() => !!hdr()"
	hsPrivate   = "() => /this account is private/i.test(VT())"
	hsDialogN   = "d.querySelectorAll('a[href]').length : 0"
	hsCountLink = "headerStats(h)[kind]"
	hsDialogRow = "u.toLowerCase() === owner.toLowerCase()"
	hsDialogScr = "el.scrollTop = el.scrollHeight"
	hsGridState = "posts: Array.from((document.querySelector('main')"
	hsGrid      = "thumbnail_src: img ?"
	hsGridScr   = "window.scrollBy(0, Math.max(800, innerHeight))"
	hsSearchN   = "querySelectorAll('main a[href], a[href]')"
	hsPostData  = "d.post_type = (em && mediaKind(em)) || domKind"
	hsComments  = "c.like }))"
	hsCommentLs = "is_reply: depth(c.el) > min"
	hsBar       = "!!commentBox() || postBars().length > 0"
	hsLoadMore  = "Load more comments"
	hsFollowSt  = "() => followState()"
	hsUnavail   = "/sorry, this page isn.t available/i.test(VT())"
	hsMeta      = "alternateName"
)

// scriptOpen answers the scripts every open() runs: not a login page, no
// interstitial dialog, and unmarking succeeds.
func (p *healthPage) scriptOpen() *healthPage {
	return p.onValue(hsLogin, false).onValue(hsNotNow, false).onValue(hsUnmark, true)
}
