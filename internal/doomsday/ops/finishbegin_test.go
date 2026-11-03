package ops

import (
	"context"
	"errors"
	"testing"
)

type fakeFinisher struct {
	calls int
	res   FinishBeginResult
	err   error
}

func (f *fakeFinisher) FinishBegin(context.Context) (FinishBeginResult, error) {
	f.calls++
	return f.res, f.err
}

func finishReq() Request {
	r := base(OpFinishBegin)
	r.ApprovalID = "" // finish.begin has no approval record
	return r
}

func TestFinishBeginIsOnTheAllowlistButHasNoApproval(t *testing.T) {
	if !Allowed(OpFinishBegin) {
		t.Fatal("finish.begin is not on the allowlist")
	}
	r := newRig(t)
	// Decide never decides it: no approval, no limit matcher, no counter
	if _, err := r.e.Decide(bg, finishReq()); !errors.Is(err, ErrBadArguments) {
		t.Fatalf("%v", err)
	}
	if len(r.l.consumed) != 0 || len(r.a.rows) != 0 || len(r.pk.parks) != 0 {
		t.Fatal("Decide touched the store for finish.begin")
	}
}

func TestBeginFinishRunsTheFinishForTheVerifiedBossOnTheLiveCharter(t *testing.T) {
	r := newRig(t)
	f := &fakeFinisher{res: FinishBeginResult{Generation: 7, Accepted: true, State: "quiescent"}}
	got, err := r.e.BeginFinish(bg, finishReq(), f)
	if err != nil || !got.Accepted || got.Generation != 7 || f.calls != 1 {
		t.Fatalf("%+v %v calls=%d", got, err, f.calls)
	}
	if len(r.l.consumed) != 0 || len(r.l.used) != 0 {
		t.Fatal("finish.begin spent a counter or an approval")
	}
	// a refusal of the finish's own preconditions comes back as data, not as an acceptance
	f.res = FinishBeginResult{Generation: 7, Refused: "2 person item(s) are open"}
	if got, err := r.e.BeginFinish(bg, finishReq(), f); err != nil || got.Accepted || got.Refused == "" {
		t.Fatalf("%+v %v", got, err)
	}
}

// Default deny, before the finisher is reached. Mutant tried: skipping the Active check lets the first case through.
func TestBeginFinishRefusesWithoutTheLiveCharterTheCallerOrTheOperation(t *testing.T) {
	type tc struct {
		name string
		mut  func(*rig, *Request)
		want error
	}
	for _, c := range []tc{
		{"no charter", func(r *rig, _ *Request) { r.c.live = false }, ErrNoCharter},
		{"stale hash", func(_ *rig, q *Request) { q.Hash = "old" }, ErrCharterStale},
		{"stale generation", func(_ *rig, q *Request) { q.Generation = 6 }, ErrCharterStale},
		{"another profile", func(_ *rig, q *Request) { q.Profile = "other" }, ErrCharterStale},
		{"no caller", func(_ *rig, q *Request) { q.Caller = Caller{} }, ErrCallerMismatch},
		{"not the addressed session", func(_ *rig, q *Request) { q.Addressed = 5 }, ErrCallerMismatch},
		{"another operation", func(_ *rig, q *Request) { q.Op = OpSpendGrant }, ErrNotAllowlisted},
	} {
		r := newRig(t)
		f := &fakeFinisher{res: FinishBeginResult{Accepted: true}}
		q := finishReq()
		c.mut(r, &q)
		if _, err := r.e.BeginFinish(bg, q, f); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
		if f.calls != 0 {
			t.Errorf("%s: the finish was begun", c.name)
		}
	}
	r := newRig(t)
	if _, err := r.e.BeginFinish(bg, finishReq(), nil); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("no finisher: %v", err)
	}
}

func TestBeginFinishPassesTheFinishersErrorOn(t *testing.T) {
	r := newRig(t)
	boom := errors.New("boom")
	if _, err := r.e.BeginFinish(bg, finishReq(), &fakeFinisher{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
}
