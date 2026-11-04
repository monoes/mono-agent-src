package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func addMembers(t *testing.T, s *SQLStore, r Run, kind string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		m := Member{Profile: r.Profile, Generation: r.Generation, ID: fmt.Sprintf("%s-%d", kind, i), Kind: kind, Pid: 100 + i, Stamp: "s"}
		if err := s.Register(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
}

func appendN(t *testing.T, s *SQLStore, r Run, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := s.AppendAudit(ctx, r.Profile, r.Generation, "x.note", fmt.Sprintf("row %d", i)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadViewNoRun(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.ReadView(ctx, "p", 0, ViewQuery{}); !errors.Is(err, ErrNoRun) {
		t.Fatalf("no run: %v", err)
	}
	enter(t, s, "p")
	if _, err := s.ReadView(ctx, "p", 7, ViewQuery{}); !errors.Is(err, ErrNoRun) {
		t.Fatalf("unknown generation: %v", err)
	}
	if _, err := s.ReadView(ctx, "other", 0, ViewQuery{}); !errors.Is(err, ErrNoRun) {
		t.Fatalf("other profile: %v", err)
	}
}

func TestReadViewReportsRunMembersTotalsAndHead(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	addMembers(t, s, r, "session", 3)
	addMembers(t, s, r, "tool", 1)
	if err := s.Release(ctx, "p", r.Generation, "session-0"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeCumulative(ctx, "p", r.Generation, "ask_spend_micro", "ap-1", 500, 10_000); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeCumulative(ctx, "p", r.Generation, "ask_spend_micro", "ap-2", 250, 10_000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendAudit(ctx, "p", r.Generation, "free.text.kind", "the vault item named SECRETNAME holds SECRETVALUE"); err != nil {
		t.Fatal(err)
	}
	v, err := s.ReadView(ctx, "p", 0, ViewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Run.Generation != r.Generation || v.Run.State != StateRunning || v.Limits.BudgetMicro != 5_000_000 {
		t.Fatalf("run/limits: %+v %+v", v.Run, v.Limits)
	}
	if !v.Chain.Verified || v.Chain.Reason != "" {
		t.Fatalf("chain: %+v", v.Chain)
	}
	h, err := s.AuditHead(ctx, "p", r.Generation)
	if err != nil || h.Seq != v.Chain.HeadSeq || string(h.MAC) != string(v.Chain.HeadMAC) {
		t.Fatalf("head differs from AuditHead: %+v vs %+v (%v)", v.Chain, h, err)
	}
	if v.Members.Total != 4 || v.Members.Live != 3 || len(v.Members.Clusters) != 2 || v.Members.Clusters[0] != (MemberCluster{"session", 3, 2}) {
		t.Fatalf("members: %+v", v.Members)
	}
	if len(v.Totals) != 1 || v.Totals[0] != (CounterTotal{"ask_spend_micro", 750}) {
		t.Fatalf("totals: %+v", v.Totals)
	}
	if len(v.Feed) == 0 || v.Feed[0].Seq != v.Chain.HeadSeq {
		t.Fatalf("feed is not newest first: %+v", v.Feed)
	}
	if v.Feed[0].Kind != RedactedKind || v.Feed[0].Class != ClassOther {
		t.Fatalf("unknown kind not redacted: %+v", v.Feed[0])
	}
	raw, _ := json.Marshal(v.Feed)
	if strings.Contains(string(raw), "SECRET") {
		t.Fatalf("a payload leaked into the feed: %s", raw)
	}
	var spend *FeedItem
	for i := range v.Feed {
		if v.Feed[i].Kind == "counter.consume" {
			spend = &v.Feed[i]
			break
		}
	}
	if spend == nil || spend.AmountMicro == nil || *spend.AmountMicro != 250 || spend.Ref != "ap-2" || spend.Class != ClassSpend {
		t.Fatalf("consume row: %+v", spend)
	}
}

// A chain that does not verify yields no rows and no derived numbers, only the run's identity and the failure.
func TestReadViewWithholdsEverythingOnAChainThatDoesNotVerify(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	addMembers(t, s, r, "session", 2)
	if err := s.ConsumeCumulative(ctx, "p", r.Generation, "ask_tasks", "ap-1", 1, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER doomsday_audit_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE doomsday_audit SET payload = 'forged' WHERE profile = 'p' AND seq = 2`); err != nil {
		t.Fatal(err)
	}
	v, err := s.ReadView(ctx, "p", 0, ViewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Chain.Verified || v.Chain.Reason != "does_not_verify" {
		t.Fatalf("chain reported as fine: %+v", v.Chain)
	}
	if len(v.Feed) != 0 || v.Members.Total != 0 || len(v.Totals) != 0 || v.FeedNext != 0 || v.Chain.HeadSeq != 0 {
		t.Fatalf("data from a bad chain: %+v", v)
	}
	if v.Run.Generation != r.Generation {
		t.Fatalf("run identity missing: %+v", v.Run)
	}
}

func TestReadViewRefusesAForgedRunRow(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	if _, err := db.Exec(`DROP TRIGGER doomsday_runs_forward_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE doomsday_runs SET cause = 'forged' WHERE profile = 'p'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadView(ctx, "p", 0, ViewQuery{}); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("forged run: %v", err)
	}
}

func TestReadViewPaginationCoversEveryRowOnceAndIsBounded(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	appendN(t, s, r, 129) // seq 1 is run.enter: 130 rows
	seen := map[int64]bool{}
	var before int64
	pages := 0
	for {
		v, err := s.ReadView(ctx, "p", 0, ViewQuery{FeedBefore: before, FeedLimit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Feed) > 50 {
			t.Fatalf("page of %d rows over limit 50", len(v.Feed))
		}
		for i, it := range v.Feed {
			if seen[it.Seq] || (i > 0 && it.Seq >= v.Feed[i-1].Seq) || (before > 0 && it.Seq >= before) {
				t.Fatalf("order/duplicate at seq %d (before %d)", it.Seq, before)
			}
			seen[it.Seq] = true
		}
		pages++
		if v.FeedNext == 0 {
			break
		}
		before = v.FeedNext
	}
	if len(seen) != 130 || pages != 3 {
		t.Fatalf("saw %d rows in %d pages", len(seen), pages)
	}
	v, _ := s.ReadView(ctx, "p", 0, ViewQuery{FeedBefore: 100000})
	if len(v.Feed) != DefaultFeedLimit || v.Feed[0].Seq != 130 {
		t.Fatalf("cursor beyond head: %d rows, first %d", len(v.Feed), v.Feed[0].Seq)
	}
	v, _ = s.ReadView(ctx, "p", 0, ViewQuery{FeedLimit: MaxFeedLimit})
	if len(v.Feed) != 130 || v.FeedNext != 0 {
		t.Fatalf("whole chain in one max page: %d next %d", len(v.Feed), v.FeedNext)
	}
}

func TestReadViewRefusesUnboundedOrNegativeRequests(t *testing.T) {
	s, _ := newStore(t)
	enter(t, s, "p")
	for _, q := range []ViewQuery{{FeedLimit: MaxFeedLimit + 1}, {FeedLimit: 1 << 30}, {FeedLimit: -1}, {FeedBefore: -1}} {
		if _, err := s.ReadView(ctx, "p", 0, q); !errors.Is(err, ErrViewLimit) {
			t.Fatalf("%+v was not refused: %v", q, err)
		}
	}
	if _, err := s.ReadView(ctx, "p", -1, ViewQuery{}); !errors.Is(err, ErrViewLimit) {
		t.Fatalf("negative generation: %v", err)
	}
}

// Work and allocation bound: a run with many members and many rows returns at most MaxMemberClusters clusters and one
// page, whatever the run's size.
func TestReadViewBoundsWhatItReturnsWhateverTheRunSize(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	for i := 0; i < MaxMemberClusters+36; i++ {
		addMembers(t, s, r, fmt.Sprintf("kind%03d", i), 1)
	}
	appendN(t, s, r, 300)
	v, err := s.ReadView(ctx, "p", 0, ViewQuery{FeedLimit: MaxFeedLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Members.Clusters) != MaxMemberClusters || !v.Members.Truncated || v.Members.Total != MaxMemberClusters+36 {
		t.Fatalf("members: %d clusters truncated=%v total=%d", len(v.Members.Clusters), v.Members.Truncated, v.Members.Total)
	}
	if len(v.Feed) != MaxFeedLimit {
		t.Fatalf("feed: %d rows", len(v.Feed))
	}
}

// The view never writes: the connection it reads on is query_only, the write is refused by the database, and the
// connection goes back to the pool writable.
func TestReadViewConnectionRefusesWritesAndIsRestored(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	var wrote []error
	err := s.withReadOnly(ctx, func(tx *sql.Tx) error {
		_, e1 := tx.ExecContext(ctx, `INSERT INTO doomsday_audit(profile, generation, seq, kind, payload, prev, mac) VALUES ('p',1,99,'x','y',x'00',x'00')`)
		_, e2 := tx.ExecContext(ctx, `INSERT INTO doomsday_counter_deltas(profile, generation, kind, approval_id, n) VALUES ('p',1,'sends','ap-w',1)`)
		_, e3 := tx.ExecContext(ctx, `UPDATE doomsday_members SET live = 0`)
		_, e4 := tx.ExecContext(ctx, `CREATE TABLE intruder(x)`)
		wrote = []error{e1, e2, e3, e4}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range wrote {
		if e == nil || !strings.Contains(strings.ToLower(e.Error()), "readonly") && !strings.Contains(strings.ToLower(e.Error()), "read-only") && !strings.Contains(strings.ToLower(e.Error()), "query_only") {
			t.Fatalf("write %d was not refused by the database: %v", i, e)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_audit WHERE seq = 99`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a write got through: %d %v", n, err)
	}
	// The pool's connection is writable again afterwards.
	if _, err := s.AppendAudit(ctx, "p", r.Generation, "x.note", "after"); err != nil {
		t.Fatalf("the connection stayed read-only: %v", err)
	}
	if _, err := s.ReadView(ctx, "p", 0, ViewQuery{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendAudit(ctx, "p", r.Generation, "x.note", "after view"); err != nil {
		t.Fatalf("a view left the connection read-only: %v", err)
	}
}

func TestRedactRowTable(t *testing.T) {
	i64 := func(n int64) *int64 { return &n }
	ops := `{"Op":"ask.answer","ApprovalID":"ap-9f","Result":"approved","Exceeded":["ask_spend_micro","SECRET LIMIT"],"Actual":{"Class":"clarify","Tasks":2,"SpendMicro":300},"ArgsHash":"abc"}`
	grant := `{"Op":"spend.grant","ApprovalID":"ap-1","Result":"approved","ActualGrant":{"Target":"lead","AmountMicro":2000}}`
	cases := []struct {
		name, kind, payload string
		want                FeedItem
	}{
		{"enter", "run.enter", "operator ch-123", FeedItem{Seq: 1, Kind: "run.enter", Class: ClassRun}},
		{"advance", "run.advance", "running>stop-requested operator", FeedItem{Seq: 1, Kind: "run.advance", Class: ClassRun, Result: "stop-requested"}},
		{"advance junk", "run.advance", "running>SECRET x", FeedItem{Seq: 1, Kind: "run.advance", Class: ClassRun}},
		{"consume micro", "counter.consume", "grant_spend_micro ap-1 40", FeedItem{Seq: 1, Kind: "counter.consume", Class: ClassSpend, Op: "grant_spend_micro", Ref: "ap-1", AmountMicro: i64(40)}},
		{"consume count", "counter.consume", "sends ap-2 3", FeedItem{Seq: 1, Kind: "counter.consume", Class: ClassSpend, Op: "sends", Ref: "ap-2", Count: i64(3)}},
		{"consume unknown counter", "counter.consume", "vault_names ap-2 3", redacted(1)},
		{"consume bad shape", "counter.consume", "sends ap-2", redacted(1)},
		{"ask answer", "ops.ask.answer.approved", ops, FeedItem{Seq: 1, Kind: "ops.ask.answer.approved", Class: ClassAskAnswer, Op: "ask.answer", Result: "approved", Ref: "ap-9f", Exceeded: []string{"ask_spend_micro"}, AnswerClass: "clarify", AmountMicro: i64(300), Count: i64(2)}},
		{"grant", "ops.spend.grant.approved", grant, FeedItem{Seq: 1, Kind: "ops.spend.grant.approved", Class: ClassGrant, Op: "spend.grant", Result: "approved", Ref: "ap-1", AmountMicro: i64(2000)}},
		{"unknown op", "ops.vault.read.approved", `{}`, redacted(1)},
		{"unknown result", "ops.ask.answer.leaked", `{}`, redacted(1)},
		{"person item", "ops.person-item", `{"op":"plan.approve","subject":"SECRET text","exceeded":["run_tasks","x"]}`, FeedItem{Seq: 1, Kind: "ops.person-item", Class: ClassPark, Op: "plan.approve", Exceeded: []string{"run_tasks"}}},
		{"approval issue", "approval.issue", `{"id":"ap-ab12","op":"spend.grant","args_hash":"h"}`, FeedItem{Seq: 1, Kind: "approval.issue", Class: ClassApproval, Op: "spend.grant", Ref: "ap-ab12"}},
		{"approval bad ref", "approval.issue", `{"id":"Ap SECRET","op":"nope"}`, FeedItem{Seq: 1, Kind: "approval.issue", Class: ClassApproval}},
		{"finish", "finish.commit", `{"anything":"SECRET"}`, FeedItem{Seq: 1, Kind: "finish.commit", Class: ClassFinish}},
		{"denied", "denied-attempt", "SECRET why", FeedItem{Seq: 1, Kind: "denied-attempt", Class: ClassRefusal}},
		{"unknown kind", "merge.something", "x", redacted(1)},
		{"oversized", "run.enter", strings.Repeat("a", maxPayloadBytes+1), redacted(1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactRow(1, c.kind, c.payload)
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(c.want)
			if string(a) != string(b) {
				t.Fatalf("got  %s\nwant %s", a, b)
			}
			if strings.Contains(string(a), "SECRET") {
				t.Fatalf("leak: %s", a)
			}
		})
	}
}
