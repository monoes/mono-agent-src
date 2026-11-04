package store

import (
	"errors"
	"testing"
)

func TestStrategyKindsAreReservedToTheTypedWriter(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	for _, k := range []string{"strategy.proposal", "strategy.", "strategy.x"} {
		if _, err := s.AppendAudit(ctx, "p", r.Generation, k, "{}"); !errors.Is(err, ErrReservedKind) {
			t.Fatalf("AppendAudit accepted the reserved kind %q: %v", k, err)
		}
		if err := s.ConsumeMany(ctx, "p", r.Generation, "x-"+k, []Charge{{"k", 1, 5}}, AuditEntry{Kind: k, Payload: "{}"}); !errors.Is(err, ErrReservedKind) {
			t.Fatalf("ConsumeMany accepted the reserved kind %q: %v", k, err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, k := range []string{"", "strategy.", "finish.commit", "counter.consume", "ops.plan.approve.approved", "Strategy.x", "xstrategy.x"} {
		if err := s.AppendStrategyAuditTx(ctx, tx, "p", r.Generation, k, "{}"); !errors.Is(err, ErrNotStrategyKind) {
			t.Fatalf("the strategy writer appended %q: %v", k, err)
		}
	}
}

func TestStrategyAuditRowsJoinTheVerifiedChainAndRollBackWithTheirTransaction(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	head := func() int {
		var n int
		if err := db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM doomsday_audit WHERE profile='p' AND generation=?`, r.Generation).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	h0 := head()
	tx, _ := db.Begin()
	if err := s.AppendStrategyAuditTx(ctx, tx, "p", r.Generation, "strategy.proposal", `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil || head() != h0 {
		t.Fatalf("a rolled-back strategy line stayed: head %d want %d (%v)", head(), h0, err)
	}
	for i := 0; i < 3; i++ {
		tx, _ = db.Begin()
		if err := s.AppendStrategyAuditTx(ctx, tx, "p", r.Generation, "strategy.novelty", "{}"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if head() != h0+3 {
		t.Fatalf("head %d, want %d", head(), h0+3)
	}
	if err := s.VerifyAudit(ctx, "p", r.Generation); err != nil {
		t.Fatalf("the chain with strategy rows does not verify: %v", err)
	}
	if _, err := s.AppendAudit(ctx, "p", r.Generation, "note", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyAudit(ctx, "p", r.Generation); err != nil {
		t.Fatalf("a generic row after strategy rows: %v", err)
	}
}
