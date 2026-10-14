package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/browser"
	"github.com/rs/zerolog"
)

// Fixture "sites" for `automation test` (data/automation-templates/README.md):
//
//	tests/<name>.routes.json   [{"match":"<regexp on URL>","fixture":"<file>","status":200,"after":"<regexp>"}]
//	tests/fixtures/<name>.html fallback page for any unmatched document URL
//
// First matching route wins. "fixture" is a file in tests/fixtures, or a
// path under tests/ when it contains a slash (e.g. "pages/submit.html").
// A route with "after" applies only once a recorded non-GET request's URL
// matches it (the page after a form post: list it before the plain route).
// Non-document requests get an empty body. Requests other than GET are
// recorded (method, URL, body) for the expect file's "requests" assertions.

type fixtureRoute struct {
	re, after *regexp.Regexp
	body      string
	status    int
}

type fixtureSite struct {
	routes   []fixtureRoute
	fallback *fixtureRoute
}

// page returns what to serve for a document URL, given the non-GET
// requests recorded so far.
func (s fixtureSite) page(url string, sent []recordedRequest) (fixtureRoute, bool) {
	for _, r := range s.routes {
		if r.re.MatchString(url) && (r.after == nil || anyURLMatches(r.after, sent)) {
			return r, true
		}
	}
	if s.fallback != nil {
		return *s.fallback, true
	}
	return fixtureRoute{}, false
}

func loadFixtureSite(fsys fs.FS, fx fixtureFile) (fixtureSite, error) {
	var site fixtureSite
	if fx.htmlPath != "" {
		b, err := fs.ReadFile(fsys, fx.htmlPath)
		if err != nil {
			return site, err
		}
		site.fallback = &fixtureRoute{body: string(b), status: 200}
	}
	routesPath := "tests/" + fx.name + ".routes.json"
	b, err := fs.ReadFile(fsys, routesPath)
	if err != nil {
		if site.fallback == nil {
			return site, fmt.Errorf("no tests/fixtures/%s.html and no %s", fx.name, routesPath)
		}
		return site, nil
	}
	var raw []struct {
		Match   string `json:"match"`
		Fixture string `json:"fixture"`
		Status  int    `json:"status"`
		After   string `json:"after"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return site, fmt.Errorf("%s: %w", routesPath, err)
	}
	for i, r := range raw {
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return site, fmt.Errorf("%s[%d]: match: %w", routesPath, i, err)
		}
		file := "tests/fixtures/" + r.Fixture
		if strings.Contains(r.Fixture, "/") {
			file = "tests/" + r.Fixture
		}
		if file = path.Clean(file); !strings.HasPrefix(file, "tests/") || !fs.ValidPath(file) {
			return site, fmt.Errorf("%s[%d]: fixture %q is outside tests/", routesPath, i, r.Fixture)
		}
		body, err := fs.ReadFile(fsys, file)
		if err != nil {
			return site, fmt.Errorf("%s[%d]: %w", routesPath, i, err)
		}
		status := r.Status
		if status == 0 {
			status = 200
		}
		rt := fixtureRoute{re: re, body: string(body), status: status}
		if r.After != "" {
			if rt.after, err = regexp.Compile(r.After); err != nil {
				return site, fmt.Errorf("%s[%d]: after: %w", routesPath, i, err)
			}
		}
		site.routes = append(site.routes, rt)
	}
	return site, nil
}

func anyURLMatches(re *regexp.Regexp, reqs []recordedRequest) bool {
	for _, r := range reqs {
		if re.MatchString(r.URL) {
			return true
		}
	}
	return false
}

// recordedRequest is a non-GET request the page made during a fixture run.
type recordedRequest struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Body   string `json:"body"`
}

// requestAssert is one entry of an expect file's "requests".
type requestAssert struct {
	Method       string `json:"method"`
	URLMatches   string `json:"urlMatches"`
	BodyContains string `json:"bodyContains"`
}

// fixtureExpect is a parsed tests/<name>.expect.json: either the expected
// records, or {"records": …?, "requests": [...]}.
type fixtureExpect struct {
	records    interface{}
	hasRecords bool
	requests   []requestAssert
}

func parseFixtureExpect(b []byte) (fixtureExpect, error) {
	var e fixtureExpect
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return e, err
	}
	m, ok := v.(map[string]interface{})
	if _, hasReq := m["requests"]; !ok || !hasReq {
		e.records, e.hasRecords = v, true
		return e, nil
	}
	var obj struct {
		Records  json.RawMessage `json:"records"`
		Requests []requestAssert `json:"requests"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return e, err
	}
	e.requests = obj.Requests
	if len(obj.Records) > 0 {
		e.hasRecords = true
		_ = json.Unmarshal(obj.Records, &e.records)
	}
	return e, nil
}

// unmetRequests returns the assertions no recorded request satisfies.
func unmetRequests(asserts []requestAssert, got []recordedRequest) ([]string, error) {
	var unmet []string
	for _, a := range asserts {
		var re *regexp.Regexp
		if a.URLMatches != "" {
			var err error
			if re, err = regexp.Compile(a.URLMatches); err != nil {
				return nil, fmt.Errorf("urlMatches %q: %w", a.URLMatches, err)
			}
		}
		found := false
		for _, r := range got {
			if (a.Method == "" || strings.EqualFold(a.Method, r.Method)) &&
				(re == nil || re.MatchString(r.URL)) &&
				strings.Contains(r.Body, a.BodyContains) {
				found = true
				break
			}
		}
		if !found {
			unmet = append(unmet, strings.TrimSpace(a.Method+" "+a.URLMatches+" "+a.BodyContains))
		}
	}
	return unmet, nil
}

// fixtureRun is what running one action against one fixture site produced.
type fixtureRun struct {
	items    []map[string]interface{}
	requests []recordedRequest
	safeStop *action.SafeStop
	err      error
}

// fixtureRunner runs def (inputs as params) against site. nil means no
// browser is available.
type fixtureRunner func(pkg *automation.Package, def *action.ActionDef, site fixtureSite, inputs map[string]interface{}) fixtureRun

func runFixture(pkg *automation.Package, fx fixtureFile, run fixtureRunner) fixtureResult {
	r := fixtureResult{Action: fx.action, Fixture: fx.name, OK: true, Status: "skipped"}
	fail := func(msg string) fixtureResult { r.OK, r.Status, r.Message = false, "fail", msg; return r }
	def, err := pkg.Action(fx.action)
	if err != nil {
		return fail("no such action: " + err.Error())
	}
	if fx.expectPath == "" {
		r.Message = "skipped: no tests/" + fx.name + ".expect.json"
		return r
	}
	b, err := fs.ReadFile(pkg.FS, fx.expectPath)
	if err != nil {
		return fail(err.Error())
	}
	want, err := parseFixtureExpect(b)
	if err != nil {
		return fail(fx.expectPath + ": " + err.Error())
	}
	inputs, missing := fixtureInputs(pkg.FS, fx.name, def, want.records)
	if len(missing) > 0 {
		r.Message = "skipped: no value for required input(s) " + strings.Join(missing, ", ") +
			" (add tests/" + fx.name + ".inputs.json)"
		return r
	}
	site, err := loadFixtureSite(pkg.FS, fx)
	if err != nil {
		return fail(err.Error())
	}
	if run == nil {
		r.Message = "skipped: no browser"
		return r
	}
	got := run(pkg, def, site, inputs)
	if got.safeStop != nil {
		r.Message = "skipped: stopped before side-effect step " + got.safeStop.StepID + " (run with --full)"
		return r
	}
	if got.err != nil {
		return fail(got.err.Error())
	}
	if want.hasRecords && !outputMatches(got.items, want.records) {
		g, _ := json.Marshal(got.items)
		return fail("output differs from " + fx.expectPath + ": got " + truncateStr(string(g), 300))
	}
	unmet, err := unmetRequests(want.requests, got.requests)
	if err != nil {
		return fail(fx.expectPath + ": " + err.Error())
	}
	if len(unmet) > 0 {
		g, _ := json.Marshal(got.requests)
		return fail("no request matched: " + strings.Join(unmet, "; ") + "; recorded " + truncateStr(string(g), 300))
	}
	r.Status = "pass"
	r.Message = fmt.Sprintf("%d record(s), %d request assertion(s) match %s", len(got.items), len(want.requests), fx.expectPath)
	return r
}

// newBrowserFixtureRunner starts a headless browser only if one is already
// installed (launcher.LookPath; rod's own download is never triggered).
// Returns a nil runner when none is found. full runs past side effects.
func newBrowserFixtureRunner(full bool) (fixtureRunner, func()) {
	noop := func() {}
	if os.Getenv("MONOAGENT_TEST_NO_BROWSER") != "" {
		return nil, noop
	}
	bin, found := launcher.LookPath()
	if !found {
		return nil, noop
	}
	l := launcher.New().Bin(bin).Headless(true)
	u, err := l.Launch()
	if err != nil {
		return nil, noop
	}
	b := rod.New().ControlURL(u)
	if err := b.Connect(); err != nil {
		l.Kill()
		return nil, noop
	}
	closeFn := func() { b.Close(); l.Kill() }
	return func(pkg *automation.Package, def *action.ActionDef, site fixtureSite, inputs map[string]interface{}) fixtureRun {
		page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
		if err != nil {
			return fixtureRun{err: err}
		}
		defer page.Close()
		var mu sync.Mutex
		var reqs []recordedRequest
		// Every request is answered here; nothing leaves the machine.
		router := page.HijackRequests()
		router.MustAdd("*", func(h *rod.Hijack) {
			mu.Lock()
			if m := h.Request.Method(); m != "GET" && m != "HEAD" && m != "OPTIONS" {
				reqs = append(reqs, recordedRequest{Method: m, URL: h.Request.URL().String(), Body: h.Request.Body()})
			}
			sent := append([]recordedRequest(nil), reqs...)
			mu.Unlock()
			if h.Request.Type() == proto.NetworkResourceTypeDocument {
				if pg, ok := site.page(h.Request.URL().String(), sent); ok {
					h.Response.Payload().ResponseCode = pg.status
					h.Response.SetHeader("Content-Type", "text/html; charset=utf-8")
					h.Response.SetBody(pg.body)
					return
				}
				// A body keeps Chrome on the URL (an empty 404 is a network
				// error page, which then fails the domain check).
				h.Response.Payload().ResponseCode = 404
				h.Response.SetHeader("Content-Type", "text/html; charset=utf-8")
				h.Response.SetBody("<!doctype html><title>404</title><p>no fixture for this URL</p>")
				return
			}
			h.Response.SetBody("")
		})
		go router.Run()
		defer router.Stop()

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		// Start where a real run starts: on the package's start URL (served
		// from the fixtures), not about:blank, which fails the domain check.
		if start := pkg.Manifest.Site.StartURL; start != "" {
			if err := page.Context(ctx).Navigate(start); err != nil {
				return fixtureRun{err: fmt.Errorf("open start URL %s: %w", start, err)}
			}
			_ = page.Context(ctx).WaitLoad()
		}
		ae := action.NewActionExecutor(ctx, browser.NewRodPage(page.Context(ctx)), nil, nil, nil, nil, zerolog.Nop())
		ae.SetPackage(pkg.Context())
		ae.SetSafeMode(!full)
		res, err := ae.ExecuteDef(&action.StorageAction{ID: "fixture-test", Type: def.ActionType,
			TargetPlatform: pkg.Manifest.ID, Params: inputs}, def)
		out := fixtureRun{err: err, safeStop: ae.SafeStopped()}
		if res != nil {
			out.items = res.ExtractedItems
		}
		mu.Lock()
		out.requests = append(out.requests, reqs...)
		mu.Unlock()
		return out
	}, closeFn
}
