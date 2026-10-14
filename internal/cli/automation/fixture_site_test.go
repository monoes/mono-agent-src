package automation

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestAutomationFixtureSiteRouting(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"tests/fixtures/post.html":   {Data: []byte("FALLBACK")},
		"tests/fixtures/done.html":   {Data: []byte("DONE")},
		"tests/pages/form.html":      {Data: []byte("FORM")},
		"tests/post.expect.json":     {Data: []byte(`{"requests":[{"method":"POST","urlMatches":"/create$","bodyContains":"title=hi"}]}`)},
		"tests/post.routes.json":     {Data: []byte(`[{"match":"/new$","fixture":"pages/form.html"},{"match":"/create$","fixture":"done.html","status":201}]`)},
		"tests/fixtures/helper.html": {Data: []byte("not a test: no such action")},
	}
	fx := listFixtures(fsys, []string{"post"})
	if len(fx) != 1 || fx[0].name != "post" || fx[0].expectPath == "" {
		t.Fatalf("fixtures = %+v", fx)
	}
	site, err := loadFixtureSite(fsys, fx[0])
	if err != nil {
		t.Fatal(err)
	}
	for url, want := range map[string]string{
		"https://w.test/new": "FORM", "https://w.test/create": "DONE", "https://w.test/other": "FALLBACK",
	} {
		if pg, ok := site.page(url, nil); !ok || pg.body != want {
			t.Errorf("%s → %q", url, pg.body)
		}
	}
	if pg, _ := site.page("https://w.test/create", nil); pg.status != 201 {
		t.Errorf("status = %d", pg.status)
	}

	// "after": a page that changes once the form was posted.
	stateful := fstest.MapFS{
		"tests/fixtures/before.html": {Data: []byte("BEFORE")},
		"tests/fixtures/after.html":  {Data: []byte("AFTER")},
		"tests/s.routes.json": {Data: []byte(`[{"match":"/list$","after":"/create$","fixture":"after.html"},
			{"match":"/list$","fixture":"before.html"}]`)},
	}
	ss, err := loadFixtureSite(stateful, fixtureFile{name: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if pg, _ := ss.page("https://w.test/list", nil); pg.body != "BEFORE" {
		t.Errorf("before post: %q", pg.body)
	}
	posted := []recordedRequest{{Method: "POST", URL: "https://w.test/create"}}
	if pg, _ := ss.page("https://w.test/list", posted); pg.body != "AFTER" {
		t.Errorf("after post: %q", pg.body)
	}

	bad := fstest.MapFS{"tests/x.routes.json": {Data: []byte(`[{"match":".","fixture":"../../automation.json"}]`)},
		"automation.json": {Data: []byte("{}")}}
	if _, err := loadFixtureSite(bad, fixtureFile{name: "x"}); err == nil || !strings.Contains(err.Error(), "outside tests/") {
		t.Fatalf("escape not refused: %v", err)
	}
}

func TestAutomationFixtureExpectRequests(t *testing.T) {
	t.Parallel()
	e, err := parseFixtureExpect([]byte(`{"records":[{"a":1}],"requests":[{"method":"post","urlMatches":"/c$","bodyContains":"x=1"}]}`))
	if err != nil || !e.hasRecords || len(e.requests) != 1 {
		t.Fatalf("expect = %+v, %v", e, err)
	}
	plain, _ := parseFixtureExpect([]byte(`{"items":[]}`))
	if !plain.hasRecords || plain.requests != nil {
		t.Fatalf("plain = %+v", plain)
	}
	got := []recordedRequest{{Method: "POST", URL: "https://w.test/c", Body: "x=1&y=2"}}
	if unmet, _ := unmetRequests(e.requests, got); len(unmet) != 0 {
		t.Fatalf("unmet = %v", unmet)
	}
	if unmet, _ := unmetRequests(e.requests, nil); len(unmet) != 1 {
		t.Fatalf("unmet with no requests = %v", unmet)
	}
}
