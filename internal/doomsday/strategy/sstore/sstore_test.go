package sstore

import (
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

func TestAcceptProposalStoresDirectionNoveltyAndAuditAtomically(t *testing.T) {
	e := newEnv(t)
	d := e.accept(t, "d1", "Build the parser")
	got, err := e.s.GetDirection(ctx, e.prof, e.gen, "d1")
	if err != nil || got != d {
		t.Fatalf("got %+v err %v", got, err)
	}
	nv, err := e.s.ListNovelty(ctx, e.prof, e.gen)
	if err != nil || len(nv) != 1 || nv[0].DirectionID != "d1" || nv[0].Outcome != types.NoveltyAccepted {
		t.Fatalf("novelty %+v %v", nv, err)
	}
	if k := e.sink.kinds; len(k) != 2 || k[0] != types.KindProposal || k[1] != types.KindNovelty {
		t.Fatalf("audit kinds %v", k)
	}
	e.auditOK(t)
}

func TestAuditFailureRollsTheRowBack(t *testing.T) {
	e := newEnv(t)
	e.sink.fail = true
	err := e.s.AcceptProposal(ctx, e.dir("d1", "x"), types.NoveltyRow{Outcome: types.NoveltyAccepted})
	if !errors.Is(err, ErrAudit) {
		t.Fatalf("want ErrAudit, got %v", err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM doomsday_strategy_directions`) + e.count(t, `SELECT COUNT(*) FROM doomsday_strategy_novelty`) + e.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE kind LIKE 'strategy.%'`); n != 0 {
		t.Fatalf("rows survived a failed audit append: %d", n)
	}
}

func TestDuplicateKeyCitesTheExistingDirection(t *testing.T) {
	e := newEnv(t)
	e.accept(t, "d1", "same")
	err := e.s.AcceptProposal(ctx, e.dir("d2", "same"), types.NoveltyRow{Outcome: types.NoveltyAccepted})
	var dup *DuplicateKeyError
	if !errors.As(err, &dup) || dup.ExistingID != "d1" {
		t.Fatalf("got %v", err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM doomsday_strategy_novelty`); n != 1 {
		t.Fatalf("novelty rows %d", n)
	}
}

func TestInsertsRefusedWhenRunNotRunning(t *testing.T) {
	e := newEnv(t)
	e.stop(t)
	if err := e.s.AcceptProposal(ctx, e.dir("d1", "x"), types.NoveltyRow{Outcome: types.NoveltyAccepted}); !errors.Is(err, ErrRunNotRunning) {
		t.Fatalf("direction: %v", err)
	}
	if _, _, err := e.s.InsertEpoch(ctx, e.epoch(1, types.TriggerTimer)); !errors.Is(err, ErrRunNotRunning) {
		t.Fatalf("epoch: %v", err)
	}
	if err := e.s.RecordRefusal(ctx, types.NoveltyRow{Profile: e.prof, Generation: e.gen, Outcome: types.NoveltyRefusedLedger}); !errors.Is(err, ErrRunNotRunning) {
		t.Fatalf("novelty: %v", err)
	}
}

func TestMoveDirectionForwardOnlyAndInertAfterStop(t *testing.T) {
	e := newEnv(t)
	e.accept(t, "d1", "x")
	if err := e.s.MoveDirection(ctx, e.prof, e.gen, "d1", types.DirFinished, ""); !errors.Is(err, ErrBackwardMove) {
		t.Fatalf("proposed->finished: %v", err)
	}
	if err := e.s.MoveDirection(ctx, e.prof, e.gen, "d1", types.DirActive, "lead-1"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.MoveDirection(ctx, e.prof, e.gen, "d1", types.DirStalled, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.s.MoveDirection(ctx, e.prof, e.gen, "d1", types.DirActive, ""); !errors.Is(err, ErrBackwardMove) {
		t.Fatalf("stalled->active: %v", err)
	}
	got, _ := e.s.GetDirection(ctx, e.prof, e.gen, "d1")
	if got.State != types.DirStalled || got.SessionID != "lead-1" {
		t.Fatalf("%+v", got)
	}
	e.stop(t)
	if err := e.s.MoveDirection(ctx, e.prof, e.gen, "d1", types.DirPruned, ""); !errors.Is(err, ErrRunNotRunning) {
		t.Fatalf("after stop: %v", err)
	}
	if err := e.s.MoveDirection(ctx, e.prof, e.gen, "nope", types.DirActive, ""); !errors.Is(err, ErrRunNotRunning) {
		t.Fatalf("after stop, unknown: %v", err)
	}
}

func TestSessionBindingIsOneTime(t *testing.T) {
	e := newEnv(t)
	e.accept(t, "d1", "x")
	if err := e.s.MoveDirection(ctx, e.prof, e.gen, "d1", types.DirActive, "lead-1"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.MoveDirection(ctx, e.prof, e.gen, "d1", types.DirStalled, "lead-2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rebind: %v", err)
	}
}

func TestEpochInsertIsIdempotentAndConflictsAreTyped(t *testing.T) {
	e := newEnv(t)
	ep := e.epoch(1, types.TriggerTimer)
	if _, created, err := e.s.InsertEpoch(ctx, ep); err != nil || !created {
		t.Fatalf("first: %v %v", created, err)
	}
	got, created, err := e.s.InsertEpoch(ctx, ep)
	if err != nil || created || got != ep {
		t.Fatalf("replay: %+v %v %v", got, created, err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = ?`, types.KindEpochBegin); n != 1 {
		t.Fatalf("epoch.begin lines %d", n)
	}
	other := ep
	other.Trigger = types.TriggerNewEvidence
	if _, _, err := e.s.InsertEpoch(ctx, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
}

func TestJudgmentLifecycle(t *testing.T) {
	e := newEnv(t)
	e.s.InsertEpoch(ctx, e.epoch(1, types.TriggerTimer))
	body := []byte(`{"criteria":[],"directions":[],"next_actions":[]}`)
	if created, err := e.s.RecordJudgment(ctx, e.prof, e.gen, 1, body); err != nil || !created {
		t.Fatalf("%v %v", created, err)
	}
	if created, err := e.s.RecordJudgment(ctx, e.prof, e.gen, 1, body); err != nil || created {
		t.Fatalf("replay %v %v", created, err)
	}
	if _, err := e.s.RecordJudgment(ctx, e.prof, e.gen, 1, []byte(`{"x":1}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("different body: %v", err)
	}
	ep, _ := e.s.GetEpoch(ctx, e.prof, e.gen, 1)
	if ep.State != types.EpochJudged || !ep.HasJudgment || ep.JudgmentHash != types.BodyHash(body) {
		t.Fatalf("%+v", ep)
	}
	j, err := e.s.GetJudgment(ctx, e.prof, e.gen, 1)
	if err != nil || string(j.Body) != string(body) {
		t.Fatalf("%v %v", j, err)
	}
	if _, err := e.s.RecordJudgment(ctx, e.prof, e.gen, 9, body); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown epoch: %v", err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = ?`, types.KindJudgment); n != 1 {
		t.Fatalf("judgment lines %d", n)
	}
	if err := e.s.MoveEpoch(ctx, e.prof, e.gen, 1, types.EpochApplied); err != nil {
		t.Fatal(err)
	}
	if err := e.s.MoveEpoch(ctx, e.prof, e.gen, 1, types.EpochAbandoned); !errors.Is(err, ErrBackwardMove) {
		t.Fatalf("applied->abandoned: %v", err)
	}
	e.auditOK(t)
}

func TestJudgmentSizeAndJudgedMoveRefused(t *testing.T) {
	e := newEnv(t)
	e.s.InsertEpoch(ctx, e.epoch(1, types.TriggerTimer))
	if _, err := e.s.RecordJudgment(ctx, e.prof, e.gen, 1, make([]byte, types.MaxJudgmentBytes+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize: %v", err)
	}
	if err := e.s.MoveEpoch(ctx, e.prof, e.gen, 1, types.EpochJudged); !errors.Is(err, ErrInvalid) {
		t.Fatalf("judged via move: %v", err)
	}
}

func TestBadJudgmentAndAbandonAfterStop(t *testing.T) {
	e := newEnv(t)
	e.s.InsertEpoch(ctx, e.epoch(1, types.TriggerTimer))
	e.s.InsertEpoch(ctx, e.epoch(2, types.TriggerTimer))
	if err := e.s.MoveEpoch(ctx, e.prof, e.gen, 1, types.EpochBadJudgment); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = ?`, types.KindBadJudgment); n != 1 {
		t.Fatal("no bad-judgment line")
	}
	e.stop(t)
	if err := e.s.MoveEpoch(ctx, e.prof, e.gen, 2, types.EpochApplied); !errors.Is(err, ErrRunNotRunning) {
		t.Fatalf("apply after stop: %v", err)
	}
	if _, err := e.s.RecordJudgment(ctx, e.prof, e.gen, 2, []byte(`{}`)); !errors.Is(err, ErrRunNotRunning) {
		t.Fatalf("judge after stop: %v", err)
	}
	if err := e.s.MoveEpoch(ctx, e.prof, e.gen, 2, types.EpochAbandoned); err != nil {
		t.Fatalf("abandon after stop: %v", err)
	}
}

func TestUncheckedProposalCapPerEpoch(t *testing.T) {
	e := newEnv(t)
	e.s.InsertEpoch(ctx, e.epoch(1, types.TriggerTimer))
	un := types.NoveltyRow{EpochSeq: 1, Outcome: types.NoveltyAcceptedUnchecked}
	if err := e.s.AcceptProposal(ctx, e.dir("d1", "a"), un); err != nil {
		t.Fatal(err)
	}
	if err := e.s.AcceptProposal(ctx, e.dir("d2", "b"), un); !errors.Is(err, ErrUncheckedLimit) {
		t.Fatalf("second unchecked: %v", err)
	}
	e.s.InsertEpoch(ctx, e.epoch(2, types.TriggerTimer))
	un.EpochSeq = 2
	if err := e.s.AcceptProposal(ctx, e.dir("d3", "c"), un); err != nil {
		t.Fatalf("next epoch: %v", err)
	}
}

func TestRefusalRowsAndLedgerLookup(t *testing.T) {
	e := newEnv(t)
	d := e.accept(t, "d1", "x")
	r := types.NoveltyRow{Profile: e.prof, Generation: e.gen, KeyHash: types.KeyHash("y"), MatchedID: "d1", Outcome: types.NoveltyRefusedDirection}
	if err := e.s.RecordRefusal(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := e.s.RecordRefusal(ctx, types.NoveltyRow{Profile: e.prof, Generation: e.gen, Outcome: types.NoveltyAccepted}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted via refusal: %v", err)
	}
	got, ok, err := e.s.FindNovelty(ctx, e.prof, e.gen, d.KeyHash)
	if err != nil || !ok || got.DirectionID != "d1" {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	if _, ok, _ := e.s.FindNovelty(ctx, e.prof, e.gen, types.KeyHash("none")); ok {
		t.Fatal("found a key that was never stored")
	}
}

func TestTamperedRowsAreDetectedOnRead(t *testing.T) {
	e := newEnv(t)
	e.accept(t, "d1", "x")
	e.s.InsertEpoch(ctx, e.epoch(1, types.TriggerTimer))
	body := []byte(`{"criteria":[],"directions":[],"next_actions":[]}`)
	e.s.RecordJudgment(ctx, e.prof, e.gen, 1, body)
	// The migration's triggers stop an UPDATE of a title or a judgment; a direct write to the file would not, so
	// simulate it by dropping the guard for this test database only.
	for _, q := range []string{
		`DROP TRIGGER doomsday_strategy_directions_forward_only`,
		`DROP TRIGGER doomsday_strategy_judgments_no_update`,
		`UPDATE doomsday_strategy_directions SET title = 'evil' WHERE direction_id = 'd1'`,
		`UPDATE doomsday_strategy_judgments SET body = '{"x":2}'`,
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.s.GetDirection(ctx, e.prof, e.gen, "d1"); !errors.Is(err, ErrTampered) {
		t.Fatalf("direction: %v", err)
	}
	if _, err := e.s.ListDirections(ctx, e.prof, e.gen); !errors.Is(err, ErrTampered) {
		t.Fatalf("list: %v", err)
	}
	if _, err := e.s.GetJudgment(ctx, e.prof, e.gen, 1); !errors.Is(err, ErrTampered) {
		t.Fatalf("judgment: %v", err)
	}
}

func TestMACKeyMattersAndKeyFailureIsTyped(t *testing.T) {
	e := newEnv(t)
	e.accept(t, "d1", "x")
	other := New(WrapDB(e.db), failKeyer{}, e.sink)
	if _, err := other.GetDirection(ctx, e.prof, e.gen, "d1"); !errors.Is(err, ErrKey) {
		t.Fatalf("got %v", err)
	}
	wrong := New(WrapDB(e.db), constKeyer{}, e.sink)
	if _, err := wrong.GetDirection(ctx, e.prof, e.gen, "d1"); !errors.Is(err, ErrTampered) {
		t.Fatalf("got %v", err)
	}
}

type failKeyer struct{}

func (failKeyer) MAC(string, ...[]byte) ([]byte, error) { return nil, errors.New("no key") }

type constKeyer struct{}

func (constKeyer) MAC(string, ...[]byte) ([]byte, error) { return []byte("same"), nil }

func TestRunsAreIsolatedByGeneration(t *testing.T) {
	e := newEnv(t)
	e.accept(t, "d1", "x")
	if got, _ := e.s.ListDirections(ctx, e.prof, e.gen+1); len(got) != 0 {
		t.Fatalf("saw another run's rows: %v", got)
	}
	if got, _ := e.s.ListDirections(ctx, "other", e.gen); len(got) != 0 {
		t.Fatalf("saw another profile's rows: %v", got)
	}
}

func TestValidationRefusesBadRecordsBeforeTheDatabase(t *testing.T) {
	e := newEnv(t)
	bad := e.dir("d1", "x")
	bad.State = types.DirActive
	if err := e.s.AcceptProposal(ctx, bad, types.NoveltyRow{Outcome: types.NoveltyAccepted}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("state: %v", err)
	}
	if err := e.s.AcceptProposal(ctx, e.dir("d1", "x"), types.NoveltyRow{Outcome: types.NoveltyRefusedLedger}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("outcome: %v", err)
	}
	ep := e.epoch(1, types.TriggerTimer)
	ep.State = types.EpochApplied
	if _, _, err := e.s.InsertEpoch(ctx, ep); !errors.Is(err, ErrInvalid) {
		t.Fatalf("epoch state: %v", err)
	}
}
