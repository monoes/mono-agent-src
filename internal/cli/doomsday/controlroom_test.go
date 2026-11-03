package doomsday

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

var updateGolden = flag.Bool("update", false, "rewrite the control room golden files")

// viewEngine is an Engine with the control room's read view; it records what reached it.
type viewEngine struct {
	fakeEngine
	view  store.View
	err   error
	calls []store.ViewQuery
}

func (v *viewEngine) ReadView(_ context.Context, q store.ViewQuery) (store.View, error) {
	v.calls = append(v.calls, q)
	return v.view, v.err
}

func viewRig(v *viewEngine) *rig {
	r := newRig()
	r.deps.Engine = func() Engine { return v }
	r.deps.JSONOutput = func() bool { return true }
	return r
}

func i64(n int64) *int64 { return &n }

func runningView() store.View {
	return store.View{
		Run:              store.Run{Profile: "p1", Generation: 2, CharterID: "ch-abc", State: store.StateRunning},
		Limits:           store.Limits{BudgetMicro: 5_000_000},
		RunSpendCapMicro: 1_000_000, RunTasksCap: 20,
		Chain: store.ChainState{Verified: true, HeadSeq: 42, HeadMAC: []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6}},
		Feed: []store.FeedItem{
			{Seq: 42, Kind: "ops.ask.answer.approved", Class: store.ClassAskAnswer, Op: "ask.answer", Result: "approved", Ref: "ap-9f", AnswerClass: "clarify", AmountMicro: i64(300), Count: i64(2)},
			{Seq: 41, Kind: "ops.spend.grant.parked", Class: store.ClassGrant, Op: "spend.grant", Result: "parked", Exceeded: []string{"grant_max_per_grant_micro"}},
			{Seq: 40, Kind: store.RedactedKind, Class: store.ClassOther},
		},
		FeedNext: 40,
		Members: store.MemberSummary{Total: 7, Live: 5, Truncated: false, Clusters: []store.MemberCluster{
			{Kind: "session", Total: 5, Live: 4}, {Kind: "tool", Total: 2, Live: 1}}},
		Totals: []store.CounterTotal{{Kind: "ask_spend_micro", Total: 750}, {Kind: "ask_tasks", Total: 3}, {Kind: "grant_spend_micro", Total: 2000}},
	}
}

func unverifiedView() store.View {
	return store.View{Run: store.Run{Profile: "p1", Generation: 2, CharterID: "ch-abc", State: store.StateStopRequested},
		Limits: store.Limits{BudgetMicro: 5_000_000}, Chain: store.ChainState{Reason: "does_not_verify"}}
}

func TestControlRoomGoldenDocuments(t *testing.T) {
	real := now
	now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	t.Cleanup(func() { now = real })
	cases := []struct {
		name string
		eng  *viewEngine
		args []string
	}{
		{"overview_running", &viewEngine{view: runningView()}, []string{"overview"}},
		{"overview_unverified", &viewEngine{view: unverifiedView()}, []string{"overview"}},
		{"overview_run_row_forged", &viewEngine{err: store.ErrUnverifiable}, []string{"overview"}},
		{"overview_no_run", &viewEngine{err: store.ErrNoRun}, []string{"overview"}},
		{"overview_off", &viewEngine{err: ErrOff}, []string{"overview"}},
		{"feed_page", &viewEngine{view: runningView()}, []string{"feed", "--limit", "3"}},
		{"feed_unverified", &viewEngine{view: unverifiedView()}, []string{"feed"}},
		{"feed_no_run", &viewEngine{err: store.ErrNoRun}, []string{"feed"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := run(t, viewRig(c.eng).deps, "", c.args...)
			if err != nil {
				t.Fatal(err, out)
			}
			path := filepath.Join("testdata", "controlroom_"+c.name+".golden.json")
			if *updateGolden {
				if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if out != string(want) {
				t.Fatalf("contract changed (a field change is a new schema_version):\n got %s\nwant %s", out, want)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("not JSON: %s", out)
			}
		})
	}
}

// The mode off: an engine without a read view (Off, a build without the mode) is available:false, exit 0, for both verbs.
func TestControlRoomVerbsAreUnavailableWhenTheModeIsOff(t *testing.T) {
	for _, verb := range ControlRoomVerbs {
		t.Run(verb, func(t *testing.T) {
			r := newRig()
			r.deps.Engine = func() Engine { return Off{} }
			r.deps.JSONOutput = func() bool { return true }
			out, err := run(t, r.deps, "", verb)
			if err != nil {
				t.Fatal(err, out)
			}
			var doc map[string]any
			if json.Unmarshal([]byte(out), &doc) != nil || doc["available"] != false || doc["reason"] != ReasonOff || doc["schema_version"] != float64(1) {
				t.Fatalf("off document: %s", out)
			}
			r.deps.Engine = nil // no engine at all is the same
			if out, err = run(t, r.deps, "", verb); err != nil || !strings.Contains(out, `"available":false`) {
				t.Fatalf("nil engine: %v %s", err, out)
			}
		})
	}
}

func TestControlRoomVerbsAreOperatorOnlyAndTouchNothingWhenRefused(t *testing.T) {
	for _, verb := range ControlRoomVerbs {
		for name, set := range map[string]func(*rig){
			"agent or marker": func(r *rig) { r.refusal = "an agent token is in use" },
			"not the user":    func(r *rig) { r.operator = clicore.OperatorOnly("not the user") },
		} {
			t.Run(verb+"/"+name, func(t *testing.T) {
				v := &viewEngine{view: runningView()}
				r := viewRig(v)
				set(r)
				out, err := run(t, r.deps, "", verb)
				if err == nil || exitOf(err) != 3 {
					t.Fatalf("not refused as operator_only: %v %s", err, out)
				}
				if len(v.calls) != 0 || out != "" {
					t.Fatalf("a refused read reached the store or printed: %v %q", v.calls, out)
				}
			})
		}
	}
}

func TestFeedFlagsAreBoundedAndPassedThrough(t *testing.T) {
	for _, args := range [][]string{{"feed", "--limit", "201"}, {"feed", "--limit", "-1"}, {"feed", "--before", "-5"}} {
		v := &viewEngine{view: runningView()}
		if out, err := run(t, viewRig(v).deps, "", args...); err == nil || len(v.calls) != 0 {
			t.Fatalf("%v accepted: %v %s", args, err, out)
		}
	}
	v := &viewEngine{view: runningView()}
	if out, err := run(t, viewRig(v).deps, "", "feed", "--before", "40", "--limit", "7"); err != nil {
		t.Fatal(err, out)
	}
	if len(v.calls) != 1 || v.calls[0] != (store.ViewQuery{FeedBefore: 40, FeedLimit: 7}) {
		t.Fatalf("query: %+v", v.calls)
	}
	v = &viewEngine{view: runningView()}
	run(t, viewRig(v).deps, "", "feed")
	if v.calls[0].FeedLimit != store.DefaultFeedLimit {
		t.Fatalf("default limit: %+v", v.calls)
	}
}

func TestAViewThatCannotBeReadIsAnErrorNotAnAbsentRun(t *testing.T) {
	v := &viewEngine{err: errors.New("database is locked")}
	out, err := run(t, viewRig(v).deps, "", "overview")
	if err == nil || strings.Contains(out, `"available"`) {
		t.Fatalf("a read failure was reported as a document: %v %s", err, out)
	}
}

func TestUnverifiableChainNeverShowsRowsTreeOrSpend(t *testing.T) {
	v := &viewEngine{view: unverifiedView()}
	r := viewRig(v)
	out, _ := run(t, r.deps, "", "overview")
	var o Overview
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatal(err)
	}
	if !o.Available || o.Integrity.ChainVerified != VerifiedNo || o.Tree != nil || o.Spend != nil {
		t.Fatalf("overview of a bad chain: %s", out)
	}
	// a view that wrongly carries data with a bad chain still does not show it
	bad := unverifiedView()
	bad.Feed = runningView().Feed
	bad.Totals = runningView().Totals
	bad.Members = runningView().Members
	out, _ = run(t, viewRig(&viewEngine{view: bad}).deps, "", "overview")
	if strings.Contains(out, `"tree"`) || strings.Contains(out, `"spend"`) {
		t.Fatalf("data of a bad chain in the overview: %s", out)
	}
}

// End to end on the real store: the verbs read, never write, and no payload text reaches the output.
func TestControlRoomOverTheRealStoreReadsAndNeverWrites(t *testing.T) {
	e := realEngine(t)
	r := newRig()
	r.deps.Engine = func() Engine { return e }
	r.deps.JSONOutput = func() bool { return true }
	ctx := context.Background()
	if out, err := run(t, r.deps, "", "overview"); err != nil || !strings.Contains(out, `"reason":"no_run"`) {
		t.Fatalf("no run: %v %s", err, out)
	}
	ch := store.Charter{Profile: "p1", Generation: 1, Goal: "g", Criteria: []store.Criterion{{Kind: "command", Spec: "true", Expected: "ok"}},
		Limits:  store.Limits{TreeDepth: 4, LivePerTree: 5, SpawnRate: 1, OpenSessions: 5, MaxConcurrent: 5, MaxTotal: 10, BudgetMicro: 9_000_000},
		BossPin: store.Pin{Provider: "a", Model: "m", Version: "1", PurposePolicy: "j"}, VerifierPin: store.Pin{Provider: "a", Model: "n", Version: "1", PurposePolicy: "j"}}
	run1, err := e.Store.Enter(ctx, ch, ch.Hash(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Register(ctx, store.Member{Profile: "p1", Generation: 1, ID: "m1", Kind: "session", Pid: 5, Stamp: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.AppendAudit(ctx, "p1", run1.Generation, "boss.note", "TOPSECRETVALUE in the operator's words"); err != nil {
		t.Fatal(err)
	}
	headBefore, err := e.Store.(store.AuditHeadReader).AuditHead(ctx, "p1", 1)
	if err != nil {
		t.Fatal(err)
	}
	var docs string
	for _, args := range [][]string{{"overview"}, {"feed"}, {"feed", "--limit", "1"}} {
		out, err := run(t, r.deps, "", args...)
		if err != nil {
			t.Fatal(err, out)
		}
		docs += out
	}
	for _, want := range []string{`"available":true`, `"chain_verified":"yes"`, `"kind":"(redacted)"`, `"run_id":"p1/1"`, `"total":1`} {
		if !strings.Contains(docs, want) {
			t.Fatalf("missing %s in %s", want, docs)
		}
	}
	if strings.Contains(docs, "TOPSECRET") || strings.Contains(docs, "operator's words") {
		t.Fatalf("payload text in the output: %s", docs)
	}
	headAfter, err := e.Store.(store.AuditHeadReader).AuditHead(ctx, "p1", 1)
	if err != nil || headAfter.Seq != headBefore.Seq || string(headAfter.MAC) != string(headBefore.MAC) {
		t.Fatalf("the verbs changed the audit chain: %+v -> %+v (%v)", headBefore, headAfter, err)
	}
}
