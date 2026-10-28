package monomind

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type stubRunSource struct {
	wfs      map[string]*RunWorkflow
	runs     map[string][]RunRecord
	finished []FinishedRun
	since    []time.Time
}

func (s *stubRunSource) Workflow(_ context.Context, id string) (*RunWorkflow, error) {
	return s.wfs[id], nil
}

func (s *stubRunSource) RecentRuns(_ context.Context, id string, n int) ([]RunRecord, error) {
	r := s.runs[id]
	if len(r) > n {
		r = r[:n]
	}
	return r, nil
}

func (s *stubRunSource) FinishedSince(_ context.Context, since time.Time) ([]FinishedRun, error) {
	s.since = append(s.since, since)
	var out []FinishedRun
	for _, f := range s.finished {
		if !f.FinishedAt.Before(since) {
			out = append(out, f)
		}
	}
	return out, nil
}

type syncCall struct {
	profile string
	nodes   []KGNode
	edges   []KGEdge
	ref     string
}

type recordingSync struct {
	calls []syncCall
	err   error
}

func (r *recordingSync) fn(_ context.Context, _ *sql.DB, pid string, n []KGNode, e []KGEdge, ref string) error {
	r.calls = append(r.calls, syncCall{pid, n, e, ref})
	return r.err
}

func memTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "mem.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE configs (name TEXT PRIMARY KEY, config_data TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

var memT0 = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

func runs(statuses ...string) []RunRecord {
	out := make([]RunRecord, len(statuses))
	for i, s := range statuses {
		out[i] = RunRecord{ExecutionID: "e", Status: s, FinishedAt: memT0.Add(-time.Duration(i) * time.Minute)}
	}
	return out
}

func newMem(t *testing.T, src *stubRunSource, rs *recordingSync, now *time.Time) *RunMemory {
	return &RunMemory{DB: memTestDB(t), Source: src, Sync: rs.fn, Now: func() time.Time { return *now }, MinInterval: 30 * time.Minute}
}

func findNode(nodes []KGNode, name string) *KGNode {
	for i := range nodes {
		if nodes[i].Name == name {
			return &nodes[i]
		}
	}
	return nil
}

func hasEdge(edges []KGEdge, src, rel, dst string) bool {
	for _, e := range edges {
		if e.Source == src && e.Relation == rel && e.Target == dst {
			return true
		}
	}
	return false
}

func TestRunMemoryAggregateDescribe(t *testing.T) {
	rr := runs("FAILED", "SUCCESS", "FAILED", "SUCCESS", "FAILED", "SUCCESS_WITH_ERRORS", "CANCELLED")
	rr[0].Error = "browser not connected: extension bridge unavailable"
	rr[0].FailedNode = "Open page"
	rr[2].Error = "browser not connected"
	rr[4].Error = "HTTP 502 Bad Gateway"
	rr[5].ErrorClass = ClassSelectorMissing // engine-provided class wins
	rr[5].Error = "HTTP 502"
	o := AggregateRuns(rr, RedactRunError)
	if o.Total != 7 || o.Success != 2 || o.Failed != 3 || o.Partial != 1 || o.Cancelled != 1 || o.LatestOK {
		t.Fatalf("counts: %+v", o)
	}
	want := map[string]int{ClassBrowserUnavailable: 2, ClassUpstream5xx: 1, ClassSelectorMissing: 1}
	for c, n := range want {
		if o.Classes[c] != n {
			t.Errorf("class %s = %d, want %d (%v)", c, o.Classes[c], n, o.Classes)
		}
	}
	d := o.Describe("Daily scrape")
	for _, s := range []string{"wf Daily scrape: last 7 runs 2 success, 3 failed, 1 with errors, 1 cancelled",
		"browser_unavailable ×2", "upstream_5xx ×1", "last failure 2026-10-13T09:00:00Z at node Open page: browser not connected",
		"latest run failed"} {
		if !strings.Contains(d, s) {
			t.Errorf("describe missing %q:\n%s", s, d)
		}
	}
	if sig := o.Signature(); sig != "failing|browser_unavailable,selector_missing,upstream_5xx" {
		t.Errorf("signature %q", sig)
	}
}

func TestRunMemoryClassify(t *testing.T) {
	cases := map[string]string{
		"":                            ClassOther,
		"step rejected by reviewer":   ClassRejectedByHuman,
		"HTTP 429 Too Many Requests":  ClassRateLimited,
		"status 401: invalid api key": ClassAuth,
		"upstream returned 503 Service Unavailable": ClassUpstream5xx,
		"context deadline exceeded":                 ClassTimeout,
		"no browser connected for profile default":  ClassBrowserUnavailable,
		"waiting for selector .post-title":          ClassSelectorMissing,
		"gmail connection not configured":           ClassNotSetup,
		"login_required: sign in to monoes.me":      ClassNotSetup,
		"invalid config: field url is required":     ClassInvalidConfig,
		"something odd happened":                    ClassOther,
	}
	for msg, want := range cases {
		if got := ClassifyRunError(msg); got != want {
			t.Errorf("ClassifyRunError(%q) = %s, want %s", msg, got, want)
		}
	}
}

func TestRunMemoryRedaction(t *testing.T) {
	// Built at run time so no credential-shaped literal sits in the source.
	fakeKey := "sk" + "-live-" + strings.Repeat("9a", 8)
	fakeJWT := "eyJ" + "hbGciOi.tok"
	rr := runs("FAILED")
	rr[0].Error = "GET https://user:hunter2@api.example.com/v1?api_" + "key=abc123 failed: Authorization: Bearer " + fakeJWT + " " + "tok" + "en=" + fakeKey
	rs := &recordingSync{}
	now := memT0
	src := &stubRunSource{wfs: map[string]*RunWorkflow{"w1": {ID: "w1", Name: "W", ProfileID: "p1"}}, runs: map[string][]RunRecord{"w1": rr}}
	m := newMem(t, src, rs, &now)
	if err := m.Observe(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if len(rs.calls) != 1 {
		t.Fatalf("calls %d", len(rs.calls))
	}
	d := findNode(rs.calls[0].nodes, RunOutcomeEntity("W")).Description
	for _, secret := range []string{"hunter2", "abc123", fakeJWT, fakeKey} {
		if strings.Contains(d, secret) {
			t.Errorf("secret %q leaked: %s", secret, d)
		}
	}
	if !strings.Contains(d, "<redacted>") || !strings.Contains(d, "api.example.com") {
		t.Errorf("redaction lost context: %s", d)
	}
	long := runs("FAILED")
	long[0].Error = strings.Repeat("word ", 200)
	if lf := AggregateRuns(long, RedactRunError).LastFailure; len([]rune(lf.Error)) > maxRunErrorChars {
		t.Errorf("error not shortened: %d runes", len([]rune(lf.Error)))
	}
}

func TestRunMemoryObserveGraph(t *testing.T) {
	rr := runs("FAILED", "SUCCESS")
	rr[0].Error = "HTTP 429"
	rs := &recordingSync{}
	now := memT0
	src := &stubRunSource{
		wfs:  map[string]*RunWorkflow{"w1": {ID: "w1", Name: "Feed", Description: "Reads the feed.", ProfileID: "p2", NodeTypes: []string{"http.request", "trigger.schedule", "http.request"}}},
		runs: map[string][]RunRecord{"w1": rr},
	}
	m := newMem(t, src, rs, &now)
	if err := m.Observe(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if len(rs.calls) != 1 || rs.calls[0].profile != "p2" || rs.calls[0].ref != "workflow-runs:w1" {
		t.Fatalf("calls %+v", rs.calls)
	}
	c := rs.calls[0]
	wf := findNode(c.nodes, "workflow:Feed")
	if wf == nil || wf.Type != "workflow" || wf.Description != "Reads the feed. Node types: http.request, trigger.schedule." {
		t.Fatalf("workflow node %+v", wf)
	}
	for _, n := range []string{"run outcome:Feed", "node type:http.request", "node type:trigger.schedule", "failure class:rate_limited"} {
		if findNode(c.nodes, n) == nil {
			t.Errorf("missing node %s", n)
		}
	}
	if len(c.nodes) != 5 {
		t.Errorf("nodes not deduped: %d", len(c.nodes))
	}
	for _, e := range [][3]string{{"workflow:Feed", "uses", "node type:http.request"}, {"workflow:Feed", "fails_with", "failure class:rate_limited"}, {"workflow:Feed", "has_outcome", "run outcome:Feed"}} {
		if !hasEdge(c.edges, e[0], e[1], e[2]) {
			t.Errorf("missing edge %v", e)
		}
	}
}

func TestRunMemoryThrottle(t *testing.T) {
	rs := &recordingSync{}
	now := memT0
	src := &stubRunSource{wfs: map[string]*RunWorkflow{"w1": {ID: "w1", Name: "W", ProfileID: "p1"}}, runs: map[string][]RunRecord{"w1": runs("SUCCESS")}}
	m := newMem(t, src, rs, &now)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(m.Observe(ctx, "w1"))
	// Same outcome, more runs: no ingest even after the interval.
	src.runs["w1"] = runs("SUCCESS", "SUCCESS", "SUCCESS")
	now = now.Add(2 * time.Hour)
	must(m.Observe(ctx, "w1"))
	if len(rs.calls) != 1 {
		t.Fatalf("unchanged outcome ingested: %d calls", len(rs.calls))
	}
	// A status flip after the interval is ingested.
	f := runs("FAILED", "SUCCESS")
	f[0].Error = "timed out"
	src.runs["w1"] = f
	must(m.Observe(ctx, "w1"))
	if len(rs.calls) != 2 {
		t.Fatalf("status flip not ingested: %d", len(rs.calls))
	}
	// A new failure class 5 minutes later is throttled and marked pending.
	g := runs("FAILED", "FAILED", "SUCCESS")
	g[0].Error = "HTTP 503"
	g[1].Error = "timed out"
	src.runs["w1"] = g
	now = now.Add(5 * time.Minute)
	must(m.Observe(ctx, "w1"))
	if len(rs.calls) != 2 {
		t.Fatalf("throttle ignored: %d", len(rs.calls))
	}
	if st := m.loadState(); !st.Workflows["w1"].Pending {
		t.Fatal("throttled change not pending")
	}
	// A sweep after the interval picks the pending workflow up with no new runs.
	now = now.Add(time.Hour)
	must(m.Sweep(ctx))
	if len(rs.calls) != 3 {
		t.Fatalf("pending change not swept: %d", len(rs.calls))
	}
	if st := m.loadState(); st.Workflows["w1"].Pending {
		t.Fatal("still pending after ingest")
	}
}

func TestRunMemorySweepWatermarkAndBatching(t *testing.T) {
	rs := &recordingSync{}
	now := memT0
	src := &stubRunSource{
		wfs: map[string]*RunWorkflow{
			"a": {ID: "a", Name: "A", ProfileID: "p1"},
			"b": {ID: "b", Name: "B", ProfileID: "p1"},
			"c": {ID: "c", Name: "C", ProfileID: "p2"},
		},
		runs: map[string][]RunRecord{"a": runs("SUCCESS"), "b": runs("SUCCESS"), "c": runs("SUCCESS")},
		finished: []FinishedRun{
			{WorkflowID: "a", FinishedAt: memT0.Add(-3 * time.Minute)},
			{WorkflowID: "b", FinishedAt: memT0.Add(-2 * time.Minute)},
			{WorkflowID: "c", FinishedAt: memT0.Add(-1 * time.Minute)},
			{WorkflowID: "gone", FinishedAt: memT0.Add(-4 * time.Minute)},
		},
	}
	m := newMem(t, src, rs, &now)
	if err := m.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rs.calls) != 2 || rs.calls[0].profile != "p1" || rs.calls[1].profile != "p2" {
		t.Fatalf("want one sync per profile, got %+v", rs.calls)
	}
	if findNode(rs.calls[0].nodes, "workflow:A") == nil || findNode(rs.calls[0].nodes, "workflow:B") == nil {
		t.Fatalf("p1 batch: %+v", rs.calls[0].nodes)
	}
	if st := m.loadState(); !st.Watermark.Equal(memT0.Add(-time.Minute)) {
		t.Fatalf("watermark %v", st.Watermark)
	}
	if err := m.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := src.since[len(src.since)-1]; !got.Equal(memT0.Add(-time.Minute)) {
		t.Fatalf("second sweep since %v", got)
	}
	if len(rs.calls) != 2 {
		t.Fatalf("second sweep re-ingested: %d", len(rs.calls))
	}
}

func TestRunMemorySyncFailureStaysPending(t *testing.T) {
	rs := &recordingSync{err: errors.New("monomind missing")}
	now := memT0
	src := &stubRunSource{wfs: map[string]*RunWorkflow{"w1": {ID: "w1", Name: "W", ProfileID: "p1"}}, runs: map[string][]RunRecord{"w1": runs("SUCCESS")}}
	m := newMem(t, src, rs, &now)
	if err := m.Observe(context.Background(), "w1"); err == nil {
		t.Fatal("want sync error")
	}
	rs.err = nil
	if err := m.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rs.calls) != 2 {
		t.Fatalf("failed sync not retried: %d", len(rs.calls))
	}
}

func TestRunMemoryFixEdge(t *testing.T) {
	rs := &recordingSync{}
	now := memT0
	f := runs("FAILED")
	f[0].Error = "waiting for selector .title"
	wf := &RunWorkflow{ID: "w1", Name: "W", ProfileID: "p1", UpdatedAt: memT0.Add(-time.Hour)}
	src := &stubRunSource{wfs: map[string]*RunWorkflow{"w1": wf}, runs: map[string][]RunRecord{"w1": f}}
	m := newMem(t, src, rs, &now)
	ctx := context.Background()
	if err := m.Observe(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	// Edited after the failure, then a success.
	wf.UpdatedAt = memT0.Add(10 * time.Minute)
	ok := runs("SUCCESS")
	ok[0].FinishedAt = memT0.Add(20 * time.Minute)
	src.runs["w1"] = append(ok, f...)
	now = memT0.Add(time.Hour)
	if err := m.Observe(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	c := rs.calls[len(rs.calls)-1]
	fixName := "fix:W 2026-10-13T09:10:00Z"
	fx := findNode(c.nodes, fixName)
	if fx == nil || fx.Type != "fix" || !strings.Contains(fx.Description, "selector replaced") {
		t.Fatalf("fix node %+v in %+v", fx, c.nodes)
	}
	if !hasEdge(c.edges, "workflow:W", "fix", fixName) || !hasEdge(c.edges, fixName, "resolves", "failure class:selector_missing") {
		t.Fatalf("fix edges %+v", c.edges)
	}
}

func TestRunMemoryOptOut(t *testing.T) {
	rs := &recordingSync{}
	now := memT0
	src := &stubRunSource{wfs: map[string]*RunWorkflow{"w1": {ID: "w1", Name: "W", ProfileID: "p1"}}, runs: map[string][]RunRecord{"w1": runs("SUCCESS")},
		finished: []FinishedRun{{WorkflowID: "w1", FinishedAt: memT0}}}
	m := newMem(t, src, rs, &now)
	if !RunMemoryEnabled(m.DB) {
		t.Fatal("default must be on")
	}
	if _, err := m.DB.Exec(`INSERT INTO configs (name, config_data) VALUES ('memory.runs', 'off')`); err != nil {
		t.Fatal(err)
	}
	if RunMemoryEnabled(m.DB) {
		t.Fatal("memory.runs off ignored")
	}
	if err := m.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Observe(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if len(rs.calls) != 0 {
		t.Fatalf("opted out but synced %d", len(rs.calls))
	}
}
