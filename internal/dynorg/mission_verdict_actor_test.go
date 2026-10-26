package dynorg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// A verifier that unset its binding and recorded the verdict under another name.
func (r *missionRig) forgedVerdicts(as string) {
	r.ex.verify = func(ctx context.Context, id int64, o monomind.ExecOptions) (*monomind.TurnResult, error) {
		bound := verifierActorOf(r.t, o.Env)
		forged := tasks.Actor{Kind: tasks.Agent, Name: as, Session: bound.Session, Task: id, Verifier: true}
		if _, err := r.store.VerifySessionTask(tbg, "default", bound.Session, id, true, "all fine", forged); err != nil {
			return nil, err
		}
		return &monomind.TurnResult{SawDone: true, ResultText: "PASS", StopReason: monomind.StopEndTurn, CostUSD: 0.01, HasCostUSD: true}, nil
	}
}

func TestAVerdictByAnotherNameIsFlaggedAndDoesNotSettleTheTask(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.forgedVerdicts("someone-else")
	r.toReview(a.ID)
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the flag", func() bool {
		d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
		for _, e := range d.Events {
			if e.Kind == "comment" && strings.Contains(e.Note, verdictByUnexpectedActor) {
				return true
			}
		}
		return false
	})
	if n := r.noVerdictComments(a.ID); n != 1 {
		t.Errorf("the forged verdict must count as one run that ended without a verdict, got %d", n)
	}
	eventually(t, "the session event", func() bool {
		notes, _ := r.store.SessionEvents(tbg, "default", r.sid, 50, operator)
		for _, e := range notes {
			if e.Kind == tasks.EventNote && strings.Contains(e.Note, verdictByUnexpectedActor) {
				return true
			}
		}
		return false
	})
	// the mission does not treat the task as settled, and cannot close on it
	v := r.finish()
	if v.Finished || len(v.Unfinished) == 0 || v.Unfinished[0].ID != a.ID {
		t.Fatalf("mission_finish: %+v", v)
	}
}

func TestAVerdictByTheAssignedVerifierIsTrusted(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	r.toReview(a.ID)
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "verified", func() bool { return r.task(a.ID).Verified })
	eventually(t, "the verifier to end", func() bool { return !r.c.verifierLive(a.ID) })
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	if who, bad := untrustedVerdict(d, r.sid); bad {
		t.Fatalf("flagged %q", who)
	}
	for _, e := range d.Events {
		if strings.Contains(e.Note, verdictByUnexpectedActor) {
			t.Fatalf("flagged: %+v", e)
		}
	}
}

func TestOperatorCloseRefusesATaskJudgedByAnotherName(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	it, err := r.store.AddSessionTask(tbg, "default", r.sid, tasks.SessionTaskInput{Title: integrationTitle, Kind: tasks.KindCheck,
		Acceptance: "`go test ./...` exits 0", DependsOn: []int64{a.ID}, SuggestedExecutor: ExecLead}, operator)
	if err != nil {
		t.Fatal(err)
	}
	r.approve()
	r.autoClose()
	honest := func(ctx context.Context, id int64, o monomind.ExecOptions) (*monomind.TurnResult, error) {
		actor := verifierActorOf(r.t, o.Env)
		if _, err := r.store.VerifySessionTask(tbg, "default", actor.Session, id, true, "ok", actor); err != nil {
			return nil, err
		}
		return &monomind.TurnResult{SawDone: true, ResultText: "PASS", StopReason: monomind.StopEndTurn}, nil
	}
	r.ex.verify = func(ctx context.Context, id int64, o monomind.ExecOptions) (*monomind.TurnResult, error) {
		if id != a.ID {
			return honest(ctx, id, o)
		}
		bound := verifierActorOf(r.t, o.Env)
		forged := tasks.Actor{Kind: tasks.Agent, Name: "someone-else", Session: bound.Session, Task: id, Verifier: true}
		if _, err := r.store.VerifySessionTask(tbg, "default", bound.Session, id, true, "fine", forged); err != nil {
			return nil, err
		}
		return &monomind.TurnResult{SawDone: true, ResultText: "PASS", StopReason: monomind.StopEndTurn}, nil
	}
	r.toReview(a.ID)
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(a.ID, tasks.StatusDone) // auto-close: the forged verdict closed it
	r.next(r.c)
	lead := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName, Session: r.sid, Task: it.ID}
	if _, err := r.store.SessionFinish(tbg, "default", r.sid, it.ID, tasks.Outcome{Result: "all pass"}, lead); err != nil {
		t.Fatal(err)
	}
	if _, err := r.verify(it.ID); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(it.ID, tasks.StatusDone)
	var ref *closeRefusal
	err = r.tryClose()
	if !errors.As(err, &ref) || !strings.Contains(err.Error(), verdictByUnexpectedActor) {
		t.Fatalf("close: %v", err)
	}
	if r.session().State == tasks.SessionDone {
		t.Fatal("closed")
	}
}
