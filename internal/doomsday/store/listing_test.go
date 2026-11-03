package store

import (
	"bytes"
	"testing"
)

func TestOpenRunsListsOnlyUnfinishedRunsOfTheProfile(t *testing.T) {
	s, _ := newStore(t)
	if rs, err := s.OpenRuns(ctx, "p"); err != nil || len(rs) != 0 {
		t.Fatalf("empty store: %v %v", rs, err)
	}
	r1 := enter(t, s, "p")
	enter(t, s, "other")
	rs, err := s.OpenRuns(ctx, "p")
	if err != nil || len(rs) != 1 || rs[0].Generation != r1.Generation || rs[0].State != StateRunning {
		t.Fatalf("open runs: %+v %v", rs, err)
	}
	walk(t, s, r1, StateStopRequested, StateAuthorityRevoked, StateCleanupPending)
	if rs, _ = s.OpenRuns(ctx, "p"); len(rs) != 1 || rs[0].State != StateCleanupPending {
		t.Fatalf("cleanup-pending is still open: %+v", rs)
	}
	walk(t, s, r1, StateQuiescent)
	if rs, _ = s.OpenRuns(ctx, "p"); len(rs) != 0 {
		t.Fatalf("a quiescent run is listed: %+v", rs)
	}
}

// A run row whose MAC no longer matches must not be listed as a healthy run: the listing fails closed.
func TestOpenRunsFailsClosedOnATamperedRow(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	if _, err := db.Exec(`DROP TRIGGER doomsday_runs_forward_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE doomsday_runs SET cause = 'forged' WHERE profile = 'p'`); err != nil {
		t.Fatal(err)
	}
	if rs, err := s.OpenRuns(ctx, "p"); err == nil {
		t.Fatalf("a forged run row was listed: %+v", rs)
	}
}

func TestAuditHeadIsTheLastRowMACAndMovesWithAppends(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	h0, err := s.AuditHead(ctx, "p", r.Generation)
	if err != nil || h0.Seq != 1 || len(h0.MAC) == 0 {
		t.Fatalf("head after enter: %+v %v", h0, err)
	}
	row, err := s.AppendAudit(ctx, "p", r.Generation, "x", "y")
	if err != nil {
		t.Fatal(err)
	}
	h1, err := s.AuditHead(ctx, "p", r.Generation)
	if err != nil || h1.Seq != row.Seq || bytes.Equal(h1.MAC, h0.MAC) {
		t.Fatalf("head did not move: %+v %+v", h0, h1)
	}
	if e, err := s.AuditHead(ctx, "p", 99); err != nil || e.Seq != 0 || len(e.MAC) != 0 {
		t.Fatalf("empty chain: %+v %v", e, err)
	}
}

// The head is only reported for a chain that verifies: a forged row is an error, not a plausible head.
func TestAuditHeadRefusesAChainThatDoesNotVerify(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	if _, err := db.Exec(`DROP TRIGGER doomsday_audit_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE doomsday_audit SET payload = 'forged' WHERE profile = 'p'`); err != nil {
		t.Fatal(err)
	}
	if h, err := s.AuditHead(ctx, "p", r.Generation); err == nil {
		t.Fatalf("a forged chain gave a head: %+v", h)
	}
}
