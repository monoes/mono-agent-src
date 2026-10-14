package jevcli

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Fake credentials are assembled at run time so secret scanners do not flag
// this file.
var (
	fakeOpenAIKey = "sk" + "-proj-AbCdEf0123456789xyz"
	fakeLiveKey   = "sk" + "-live0123456789abcdefSECRET"
	fakeGitHubPAT = "gh" + "p_0123456789abcdefghijABCDEFGHIJ012345"
)

func retryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	return db.DB
}

func retryCtx(db *sql.DB, pid string) context.Context {
	return vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), pid)
}

func TestRetryJevInstalled(t *testing.T) {
	if workflow.RetryClassifier == nil {
		t.Fatal("retry_jev.go init must install workflow.RetryClassifier")
	}
}

func TestRetryJevDisabledMakesNoCalls(t *testing.T) {
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{retryQuestionID: "permanent"}))
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	db := retryTestDB(t)

	for _, ctx := range []context.Context{context.Background(), retryCtx(db, "p1")} {
		retry, delay := jevRetryClassifier(ctx, "http.request", errors.New("boom"), 1)
		if !retry || delay != 0 {
			t.Fatalf("disabled: retry=%v delay=%v, want retry as today", retry, delay)
		}
	}
	if srv.Calls() != 0 {
		t.Fatalf("disabled surface made %d Jev calls", srv.Calls())
	}
}

func TestRetryJevDecisions(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	db := retryTestDB(t)
	if err := jevconf.SetEnabled(db, "p1", jevconf.Retry, true); err != nil {
		t.Fatal(err)
	}
	ctx := retryCtx(db, "p1")
	cases := []struct {
		class string
		retry bool
		delay time.Duration
	}{
		{"transient", true, 0},
		{"rate_limited", true, 60 * time.Second}, // 30s × attempt 2
		{"auth", false, 0},
		{"permanent", false, 0},
	}
	for _, c := range cases {
		jevtest.NewServer(t, jevtest.Fixed(map[string]string{retryQuestionID: c.class}))
		retry, delay := jevRetryClassifier(ctx, "http.request", errors.New("request failed"), 2)
		if retry != c.retry || delay != c.delay {
			t.Fatalf("%s: retry=%v delay=%v, want %v %v", c.class, retry, delay, c.retry, c.delay)
		}
	}
	// Another profile on the same daemon has it off: no call.
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{retryQuestionID: "permanent"}))
	if retry, _ := jevRetryClassifier(retryCtx(db, "p2"), "x", errors.New("e"), 1); !retry || srv.Calls() != 0 {
		t.Fatalf("p2: retry=%v calls=%d", retry, srv.Calls())
	}
}

func TestRetryJevBelowThresholdOrFailureRetries(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	db := retryTestDB(t)
	_ = jevconf.SetEnabled(db, "p1", jevconf.Retry, true)
	ctx := retryCtx(db, "p1")

	// jevtest puts 0.94 on the pick; a 0.95 threshold means "not sure".
	if err := jevconf.SetThreshold(db, "p1", jevconf.Retry, 0.95); err != nil {
		t.Fatal(err)
	}
	jevtest.NewServer(t, jevtest.Fixed(map[string]string{retryQuestionID: "permanent"}))
	if retry, delay := jevRetryClassifier(ctx, "x", errors.New("e"), 1); !retry || delay != 0 {
		t.Fatalf("below threshold: retry=%v delay=%v", retry, delay)
	}
	_ = jevconf.SetThreshold(db, "p1", jevconf.Retry, 0.7)

	srv := jevtest.NewServer(t, nil)
	srv.SetStatus(500)
	if retry, delay := jevRetryClassifier(ctx, "x", errors.New("e"), 1); !retry || delay != 0 {
		t.Fatalf("server error: retry=%v delay=%v", retry, delay)
	}

	t.Setenv("TYPESAFE_API_KEY", "")
	srv = jevtest.NewServer(t, nil)
	if retry, _ := jevRetryClassifier(ctx, "x", errors.New("e"), 1); !retry || srv.Calls() != 0 {
		t.Fatalf("no key: retry=%v calls=%d", retry, srv.Calls())
	}
}

func TestRetryJevSendsRedactedState(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	db := retryTestDB(t)
	_ = jevconf.SetEnabled(db, "p1", jevconf.Retry, true)
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{retryQuestionID: "rate_limited"}))

	err := errors.New("POST https://api.example.com/v1/send?access_token=QTOKEN123 failed: status 429 " +
		"Too Many Requests (Authorization: Bearer " + fakeLiveKey + ")")
	_, _ = jevRetryClassifier(retryCtx(db, "p1"), "http.request", err, 3)

	raw := srv.RequestJSON()
	for _, leaked := range []string{fakeLiveKey, "QTOKEN123", "access_token="} {
		if strings.Contains(raw, leaked) {
			t.Fatalf("request leaked %q: %s", leaked, raw)
		}
	}
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(reqs))
	}
	state, _ := reqs[0].State.(map[string]any)
	if state["node_type"] != "http.request" || state["attempt"] != float64(3) || state["http_status"] != float64(429) {
		t.Fatalf("state = %#v", state)
	}
	if ue, _ := state["untrusted_error"].(string); !strings.Contains(ue, "https://api.example.com/v1/send") {
		t.Fatalf("untrusted_error lost the useful part: %q", ue)
	}
}

func TestRedactRetryError(t *testing.T) {
	cases := []struct{ in, mustKeep, mustDrop string }{
		{"GET https://x.io/a/b?key=abc123&x=1 failed", "https://x.io/a/b", "abc123"},
		{"dial https://user:pa55word@db.host:5432 refused", "db.host:5432", "pa55word"},
		{"header Authorization: Bearer eyJhbGciOi.payload.sig rejected", "rejected", "eyJhbGciOi"},
		{"authorization=Basic dXNlcjpwYXNz; retry", "retry", "dXNlcjpwYXNz"},
		{"using bearer abc.def-ghi now", "now", "abc.def-ghi"},
		{"openai: invalid key " + fakeOpenAIKey, "openai: invalid", fakeOpenAIKey},
		{"github token " + fakeGitHubPAT + " revoked", "revoked", fakeGitHubPAT[:14]},
		{`{"api_key": "supersecretvalue", "error": "denied"}`, "denied", "supersecretvalue"},
		{"password=hunter2 is wrong", "is wrong", "hunter2"},
		{"x-api-token: tok_9f8e7d wrong", "wrong", "tok_9f8e7d"},
		{"sig 0123456789abcdef0123456789abcdef01 bad", "bad", "0123456789abcdef0123456789abcdef01"},
		{"blob QmFzZTY0RW5jb2RlZFNlY3JldFZhbHVlMTIzNDU2Nzg5MA== end", "end", "QmFzZTY0RW5jb2RlZFNlY3JldFZhbHVl"},
		{"connection reset by peer", "connection reset by peer", ""},
		{"no_such_host_for_this_very_long_identifier_name", "no_such_host_for_this_very_long_identifier_name", ""},
	}
	for _, c := range cases {
		got := RedactRetryError(c.in)
		if !strings.Contains(got, c.mustKeep) || (c.mustDrop != "" && strings.Contains(got, c.mustDrop)) {
			t.Errorf("RedactRetryError(%q) = %q (keep %q, drop %q)", c.in, got, c.mustKeep, c.mustDrop)
		}
	}
	long := strings.Repeat("é", 3000)
	if got := RedactRetryError(long); len([]rune(got)) > maxRetryErrorChars {
		t.Fatalf("not capped: %d runes", len([]rune(got)))
	}
}

func TestParseHTTPStatus(t *testing.T) {
	cases := map[string]int{
		"status 429":                      429,
		"HTTP 503 Service Unavailable":    503,
		"status code: 401":                401,
		"got 404 Not Found from upstream": 404,
		"server returned HTTP/1.1 500":    500,
		"processed 200 items then failed": 0,
		"port 8080 refused":               0,
	}
	for in, want := range cases {
		if got := parseHTTPStatus(in); got != want {
			t.Errorf("parseHTTPStatus(%q) = %d, want %d", in, got, want)
		}
	}
}
