package strategy

import (
	"errors"
	"strconv"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

func wantIs(t *testing.T, err, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s: err = %v, want %v", what, err, target)
	}
}

// The store serves exactly one run per session: profile is the decimal session id and the generation is always 1.
func TestWrongSessionOrGenerationIsRefused(t *testing.T) {
	f := newFixture(t)
	f.openEpoch(t, 1)
	other := addSession(t, f.db, "running")
	for _, bad := range []struct {
		profile string
		gen     int64
	}{
		{f.profile, 2}, {f.profile, 0}, {"", 1}, {"abc", 1}, {"0", 1}, {"-" + f.profile, 1}, {"+" + f.profile, 1}, {"0" + f.profile, 1}, {f.profile + " ", 1},
	} {
		if _, _, err := f.store.LatestEpoch(bg, bad.profile, bad.gen); !errors.Is(err, ErrInvalid) {
			t.Errorf("LatestEpoch(%q, %d): %v", bad.profile, bad.gen, err)
		}
		if _, err := f.store.ListEpochs(bg, bad.profile, bad.gen); !errors.Is(err, ErrInvalid) {
			t.Errorf("ListEpochs(%q, %d): %v", bad.profile, bad.gen, err)
		}
		if _, err := f.store.RecordJudgment(bg, bad.profile, bad.gen, 1, []byte(`{}`)); !errors.Is(err, ErrInvalid) {
			t.Errorf("RecordJudgment(%q, %d): %v", bad.profile, bad.gen, err)
		}
		if err := f.store.MoveEpoch(bg, bad.profile, bad.gen, 1, types.EpochAbandoned); !errors.Is(err, ErrInvalid) {
			t.Errorf("MoveEpoch(%q, %d): %v", bad.profile, bad.gen, err)
		}
		if ok, err := f.store.Running(bg, bad.profile, bad.gen); ok || !errors.Is(err, ErrInvalid) {
			t.Errorf("Running(%q, %d): %t %v", bad.profile, bad.gen, ok, err)
		}
		e := f.epoch(9)
		e.Profile, e.Generation = bad.profile, bad.gen
		if _, _, err := f.store.InsertEpoch(bg, e); !errors.Is(err, ErrInvalid) {
			t.Errorf("InsertEpoch(%q, %d): %v", bad.profile, bad.gen, err)
		}
	}
	// the record itself must name the same session as the call
	e := f.epoch(2)
	e.Profile = strconv.FormatInt(other, 10)
	if _, _, err := f.store.InsertEpoch(bg, e); err != nil {
		t.Fatalf("an epoch of another running session is that session's: %v", err)
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_strategy_epochs WHERE session_id = ?`, f.session); n != 1 {
		t.Errorf("the first session holds %d epochs, want its own 1", n)
	}
	// a session that does not exist is not running
	ghost := f.epoch(1)
	ghost.Profile = "999999"
	if _, _, err := f.store.InsertEpoch(bg, ghost); !errors.Is(err, ErrNotRunning) {
		t.Errorf("a session that does not exist: %v", err)
	}
	if ok, err := f.store.Running(bg, "999999", 1); ok || err != nil {
		t.Errorf("Running on a missing session: %t %v", ok, err)
	}
}

func TestInsertEpochIsIdempotentAndConflictsOnOtherContent(t *testing.T) {
	f := newFixture(t)
	first := f.openEpoch(t, 1)
	again, created, err := f.store.InsertEpoch(bg, f.epoch(1))
	if err != nil || created || again != first {
		t.Fatalf("repeat: %+v created=%t err=%v", again, created, err)
	}
	for _, mut := range []func(*types.Epoch){
		func(e *types.Epoch) { e.Trigger = types.TriggerNewEvidence },
		func(e *types.Epoch) { e.StartedAt++ },
		func(e *types.Epoch) { e.ModelPin = "other/model/2" },
	} {
		e := f.epoch(1)
		mut(&e)
		if _, _, err := f.store.InsertEpoch(bg, e); !errors.Is(err, ErrConflict) {
			t.Errorf("same identity, other content: %v", err)
		}
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_strategy_epochs`); n != 1 {
		t.Errorf("%d epoch rows, want 1", n)
	}
	judged := f.epoch(2)
	judged.State = types.EpochJudged
	if _, _, err := f.store.InsertEpoch(bg, judged); !errors.Is(err, ErrInvalid) {
		t.Errorf("a new epoch that is not open: %v", err)
	}
	bad := f.epoch(3)
	bad.Trigger = "whenever"
	if _, _, err := f.store.InsertEpoch(bg, bad); !errors.Is(err, ErrInvalid) {
		t.Errorf("an unknown trigger: %v", err)
	}
	// a repeat of an existing epoch is answered even after the session stopped: nothing is written
	f.setState(t, "stopped")
	if _, created, err := f.store.InsertEpoch(bg, f.epoch(1)); err != nil || created {
		t.Errorf("repeat after a stop: created=%t err=%v", created, err)
	}
}

func TestJudgmentNeedsAnOpenEpoch(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.RecordJudgment(bg, f.profile, 1, 1, []byte(`{"a":1}`)); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such epoch: %v", err)
	}
	f.openEpoch(t, 1)
	created, err := f.store.RecordJudgment(bg, f.profile, 1, 1, []byte(`{"a":1}`))
	if err != nil || !created {
		t.Fatalf("judge: %t %v", created, err)
	}
	if created, err := f.store.RecordJudgment(bg, f.profile, 1, 1, []byte(`{"a":1}`)); err != nil || created {
		t.Errorf("same body again: %t %v", created, err)
	}
	if _, err := f.store.RecordJudgment(bg, f.profile, 1, 1, []byte(`{"a":2}`)); !errors.Is(err, ErrConflict) {
		t.Errorf("another body: %v", err)
	}
	e, _, _ := f.store.LatestEpoch(bg, f.profile, 1)
	if e.State != types.EpochJudged || !e.HasJudgment || e.JudgmentHash != types.BodyHash([]byte(`{"a":1}`)) {
		t.Errorf("epoch after the judgment: %+v", e)
	}
	rec, err := f.store.GetJudgment(bg, f.profile, 1, 1)
	if err != nil || string(rec.Body) != `{"a":1}` || rec.BodyHash != e.JudgmentHash {
		t.Errorf("GetJudgment: %+v %v", rec, err)
	}
	if _, err := f.store.RecordJudgment(bg, f.profile, 1, 2, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty body: %v", err)
	}
	// an epoch that was abandoned unjudged cannot be judged later
	f.openEpoch(t, 2)
	if err := f.store.MoveEpoch(bg, f.profile, 1, 2, types.EpochAbandoned); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RecordJudgment(bg, f.profile, 1, 2, []byte(`{}`)); !errors.Is(err, ErrNeedsOpenEpoch) {
		t.Errorf("judge an abandoned epoch: %v", err)
	}
	if _, err := f.store.GetJudgment(bg, f.profile, 1, 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetJudgment without a judgment: %v", err)
	}
}

func TestMoveEpochForwardOnly(t *testing.T) {
	f := newFixture(t)
	f.openEpoch(t, 1)
	wantIs(t, f.store.MoveEpoch(bg, f.profile, 1, 1, types.EpochJudged), ErrInvalid, "judged only through RecordJudgment")
	wantIs(t, f.store.MoveEpoch(bg, f.profile, 1, 1, types.EpochOpen), ErrInvalid, "back to open")
	wantIs(t, f.store.MoveEpoch(bg, f.profile, 1, 1, types.EpochApplied), ErrBackwardMove, "open straight to applied")
	wantIs(t, f.store.MoveEpoch(bg, f.profile, 1, 7, types.EpochAbandoned), ErrNotFound, "no such epoch")
	if _, err := f.store.RecordJudgment(bg, f.profile, 1, 1, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := f.store.MoveEpoch(bg, f.profile, 1, 1, types.EpochApplied); err != nil {
		t.Fatal(err)
	}
	for _, to := range []types.EpochState{types.EpochBadJudgment, types.EpochAbandoned} {
		wantIs(t, f.store.MoveEpoch(bg, f.profile, 1, 1, to), ErrBackwardMove, "out of applied to "+string(to))
	}
	f.openEpoch(t, 2)
	if err := f.store.MoveEpoch(bg, f.profile, 1, 2, types.EpochBadJudgment); err != nil {
		t.Fatal(err)
	}
	wantIs(t, f.store.MoveEpoch(bg, f.profile, 1, 2, types.EpochAbandoned), ErrBackwardMove, "out of bad-judgment")
}

// Nothing is written while the session is not running (paused, stopped, done, failed, draft) and the typed error says so;
// the one move left is closing an unfinished epoch as abandoned.
func TestNoWriteWhenTheSessionIsNotRunning(t *testing.T) {
	for _, state := range []string{"paused", "stopped", "done", "failed", "draft"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			f.openEpoch(t, 1)
			f.addDirection(t, "d1", "first direction")
			f.openEpoch(t, 2)
			f.setState(t, state)
			if ok, err := f.store.Running(bg, f.profile, 1); ok || err != nil {
				t.Errorf("Running: %t %v", ok, err)
			}
			wantIs(t, insertErr(f, 3), ErrNotRunning, "InsertEpoch")
			_, err := f.store.RecordJudgment(bg, f.profile, 1, 1, []byte(`{}`))
			wantIs(t, err, ErrNotRunning, "RecordJudgment")
			wantIs(t, f.store.MoveEpoch(bg, f.profile, 1, 1, types.EpochBadJudgment), ErrNotRunning, "MoveEpoch bad-judgment")
			wantIs(t, f.store.MoveDirection(bg, f.profile, 1, "d1", types.DirActive, "lead"), ErrNotRunning, "MoveDirection")
			d := f.direction("d2", "second direction")
			wantIs(t, f.store.AcceptProposal(bg, d, accepted(d, 2)), ErrNotRunning, "AcceptProposal")
			wantIs(t, f.store.RecordRefusal(bg, types.NoveltyRow{Profile: f.profile, Generation: 1, KeyHash: d.KeyHash, Outcome: types.NoveltyRefusedLedger}), ErrNotRunning, "RecordRefusal")
			// reads still work
			if all, err := f.store.ListEpochs(bg, f.profile, 1); err != nil || len(all) != 2 {
				t.Errorf("ListEpochs: %d %v", len(all), err)
			}
			// abandoning is the one move left
			if err := f.store.MoveEpoch(bg, f.profile, 1, 2, types.EpochAbandoned); err != nil {
				t.Errorf("abandon after %s: %v", state, err)
			}
			if got, _ := f.store.GetEpoch(bg, f.profile, 1, 2); got.State != types.EpochAbandoned {
				t.Errorf("epoch 2 = %+v", got)
			}
		})
	}
}

func insertErr(f fixture, seq int64) error {
	_, _, err := f.store.InsertEpoch(bg, f.epoch(seq))
	return err
}

func TestDirectionsMoveForwardAndBindTheLeadOnce(t *testing.T) {
	f := newFixture(t)
	f.addDirection(t, "d1", "first direction")
	wantIs(t, f.store.MoveDirection(bg, f.profile, 1, "d1", types.DirFinished, ""), ErrBackwardMove, "proposed to finished")
	wantIs(t, f.store.MoveDirection(bg, f.profile, 1, "nope", types.DirActive, ""), ErrNotFound, "no such direction")
	if err := f.store.MoveDirection(bg, f.profile, 1, "d1", types.DirActive, "lead-a"); err != nil {
		t.Fatal(err)
	}
	wantIs(t, f.store.MoveDirection(bg, f.profile, 1, "d1", types.DirStalled, "lead-b"), ErrConflict, "a second lead")
	if err := f.store.MoveDirection(bg, f.profile, 1, "d1", types.DirStalled, "lead-a"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.MoveDirection(bg, f.profile, 1, "d1", types.DirPruned, ""); err != nil {
		t.Fatal(err)
	}
	wantIs(t, f.store.MoveDirection(bg, f.profile, 1, "d1", types.DirActive, ""), ErrBackwardMove, "out of pruned")
	d, err := f.store.GetDirection(bg, f.profile, 1, "d1")
	if err != nil || d.State != types.DirPruned || d.SessionID != "lead-a" || d.Profile != f.profile || d.Generation != 1 {
		t.Errorf("direction: %+v %v", d, err)
	}
	open, err := f.store.OpenDirections(bg, f.profile, 1)
	if err != nil || len(open) != 0 {
		t.Errorf("open directions: %v %v", open, err)
	}
}

func TestDuplicateKeyIsRefusedAndNamesTheExisting(t *testing.T) {
	f := newFixture(t)
	f.addDirection(t, "d1", "first direction")
	again := f.direction("d2", "first direction")
	err := f.store.AcceptProposal(bg, again, accepted(again, 1))
	var dup *DuplicateKeyError
	if !errors.As(err, &dup) || dup.ExistingID != "d1" {
		t.Fatalf("duplicate key: %v", err)
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_strategy_directions`); n != 1 {
		t.Errorf("%d directions", n)
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_strategy_novelty`); n != 1 {
		t.Errorf("a refused proposal left %d novelty rows, want the first one's only", n)
	}
	// the same id under another title is also refused, not replaced
	sameID := f.direction("d1", "something else entirely")
	if err := f.store.AcceptProposal(bg, sameID, accepted(sameID, 1)); err == nil {
		t.Error("the same direction id was accepted twice")
	}
	if d, _ := f.store.GetDirection(bg, f.profile, 1, "d1"); d.Title != "first direction" {
		t.Errorf("direction replaced: %+v", d)
	}
}

func TestUncheckedLimitPerEpoch(t *testing.T) {
	f := newFixture(t)
	f.openEpoch(t, 1)
	f.openEpoch(t, 2)
	unchecked := func(id, title string, seq int64) error {
		d := f.direction(id, title)
		return f.store.AcceptProposal(bg, d, types.NoveltyRow{EpochSeq: seq, Outcome: types.NoveltyAcceptedUnchecked})
	}
	if err := unchecked("d1", "one", 1); err != nil {
		t.Fatal(err)
	}
	wantIs(t, unchecked("d2", "two", 1), ErrUncheckedLimit, "second unchecked in epoch 1")
	if err := unchecked("d3", "three", 2); err != nil {
		t.Errorf("a new epoch has its own allowance: %v", err)
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_strategy_directions`); n != 2 {
		t.Errorf("%d directions, want 2", n)
	}
	d := f.direction("d9", "nine")
	wantIs(t, f.store.AcceptProposal(bg, d, types.NoveltyRow{EpochSeq: 1, Outcome: types.NoveltyRefusedLedger}), ErrInvalid, "a refusal outcome on an accepted proposal")
	notNew := f.direction("d9", "nine")
	notNew.State = types.DirActive
	wantIs(t, f.store.AcceptProposal(bg, notNew, accepted(notNew, 1)), ErrInvalid, "a direction that is not born proposed")
}

func TestRefusalsAreRecordedAndReadBack(t *testing.T) {
	f := newFixture(t)
	d := f.addDirection(t, "d1", "first direction")
	row := types.NoveltyRow{Profile: f.profile, Generation: 1, EpochSeq: 1, KeyHash: d.KeyHash, MatchedID: "d1", Outcome: types.NoveltyRefusedDirection}
	if err := f.store.RecordRefusal(bg, row); err != nil {
		t.Fatal(err)
	}
	wantIs(t, f.store.RecordRefusal(bg, types.NoveltyRow{Profile: f.profile, Generation: 1, Outcome: types.NoveltyAccepted}), ErrInvalid, "an accepted outcome as a refusal")
	wantIs(t, f.store.RecordRefusal(bg, types.NoveltyRow{Profile: f.profile, Generation: 1, DirectionID: "d1", Outcome: types.NoveltyRefusedLedger}), ErrInvalid, "a refusal that names a direction")
	all, err := f.store.ListNovelty(bg, f.profile, 1)
	if err != nil || len(all) != 2 || all[0].Outcome != types.NoveltyAccepted || all[1] != row {
		t.Fatalf("ledger: %+v %v", all, err)
	}
	got, ok, err := f.store.FindNovelty(bg, f.profile, 1, d.KeyHash)
	if err != nil || !ok || got.Outcome != types.NoveltyAccepted || got.DirectionID != "d1" {
		t.Errorf("FindNovelty: %+v %t %v", got, ok, err)
	}
	if _, ok, _ := f.store.FindNovelty(bg, f.profile, 1, [32]byte{1}); ok {
		t.Error("FindNovelty found a key that was never stored")
	}
}

func TestParkedFinishRounds(t *testing.T) {
	f := newFixture(t)
	if n, err := f.store.ParkedFinishRounds(bg, f.profile, 1); err != nil || n != 0 {
		t.Fatalf("none: %d %v", n, err)
	}
	for seq, trig := range []types.Trigger{types.TriggerTimer, types.TriggerFinishParked, types.TriggerNewEvidence, types.TriggerFinishParked} {
		e := f.epoch(int64(seq + 1))
		e.Trigger = trig
		if _, _, err := f.store.InsertEpoch(bg, e); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := f.store.ParkedFinishRounds(bg, f.profile, 1); err != nil || n != 2 {
		t.Errorf("parked rounds = %d %v, want 2", n, err)
	}
	other := newFixtureOn(t, f)
	if n, _ := other.store.ParkedFinishRounds(bg, other.profile, 1); n != 0 {
		t.Errorf("another session counted %d", n)
	}
}

// A host restart finds the epoch where it was left: a new store over the same database resumes it, never re-opens it.
func TestRestartResumesTheLatestEpoch(t *testing.T) {
	f := newFixture(t)
	f.openEpoch(t, 1)
	if _, err := f.store.RecordJudgment(bg, f.profile, 1, 1, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	f.openEpoch(t, 2)
	restarted := NewStore(f.db)
	e, ok, err := restarted.LatestEpoch(bg, f.profile, 1)
	if err != nil || !ok || e.Seq != 2 || e.State != types.EpochOpen {
		t.Fatalf("latest after restart: %+v %t %v", e, ok, err)
	}
	if seq, err := restarted.NextEpochSeq(bg, f.profile, 1); err != nil || seq != 3 {
		t.Errorf("next seq = %d %v", seq, err)
	}
	again, created, err := restarted.InsertEpoch(bg, f.epoch(2))
	if err != nil || created || again.Seq != 2 {
		t.Errorf("the open epoch is re-inserted as new: %+v %t %v", again, created, err)
	}
	empty := newFixtureOn(t, f)
	if seq, _ := empty.store.NextEpochSeq(bg, empty.profile, 1); seq != 1 {
		t.Errorf("first seq = %d", seq)
	}
	if _, ok, _ := empty.store.LatestEpoch(bg, empty.profile, 1); ok {
		t.Error("an empty session has a latest epoch")
	}
}

// newFixtureOn is a second running session in the same database.
func newFixtureOn(t *testing.T, f fixture) fixture {
	t.Helper()
	g := fixture{db: f.db, store: f.store}
	g.session = addSession(t, f.db, "running")
	g.profile = strconv.FormatInt(g.session, 10)
	return g
}

func TestSessionsDoNotSeeEachOther(t *testing.T) {
	a := newFixture(t)
	b := newFixtureOn(t, a)
	a.openEpoch(t, 1)
	a.addDirection(t, "d1", "shared title")
	b.addDirection(t, "d1", "shared title") // the same id and key in another session is its own row
	if all, _ := b.store.ListEpochs(bg, b.profile, 1); len(all) != 0 {
		t.Errorf("session b sees %d epochs of a", len(all))
	}
	if _, err := b.store.GetDirection(bg, a.profile, 1, "d1"); err != nil {
		t.Error(err)
	}
	if n := count(t, a.db, `SELECT COUNT(*) FROM mission_strategy_directions`); n != 2 {
		t.Errorf("%d directions, want one per session", n)
	}
}
