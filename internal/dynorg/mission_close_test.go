package dynorg

import (
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// closeRig is a rig whose task a is done; with integration the plan has an integration
// check task too, and settle runs it to done.
func closeRig(t *testing.T, integration, settle bool) (*missionRig, tasks.Task, tasks.Task) {
	t.Helper()
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	var it tasks.Task
	if integration {
		var err error
		it, err = r.store.AddSessionTask(tbg, "default", r.sid, tasks.SessionTaskInput{Title: integrationTitle, Kind: tasks.KindCheck,
			Acceptance: "`go test ./...` exits 0", DependsOn: []int64{a.ID}, SuggestedExecutor: ExecLead}, operator)
		if err != nil {
			t.Fatal(err)
		}
	}
	r.approve()
	r.autoClose()
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	r.work(a.ID)
	if settle {
		r.next(r.c)
		lead := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName, Session: r.sid, Task: it.ID}
		if _, err := r.store.SessionFinish(tbg, "default", r.sid, it.ID, tasks.Outcome{Result: "all pass"}, lead); err != nil {
			t.Fatal(err)
		}
		if _, err := r.verify(it.ID); err != nil {
			t.Fatal(err)
		}
		r.waitStatus(it.ID, tasks.StatusDone)
	}
	return r, a, it
}

func (r *missionRig) tryClose() error {
	_, err := r.c.operatorClose(tbg, r.mc, r.sid, "the report", "test close")
	return err
}

func TestOperatorCloseRefusesEveryUnsafeState(t *testing.T) {
	cases := []struct {
		name        string
		integration bool
		settle      bool
		mutate      func(r *missionRig, a tasks.Task)
		want        string
	}{
		{"session paused", true, true, func(r *missionRig, a tasks.Task) {
			if _, err := r.store.PauseSession(tbg, "default", r.sid, operator); err != nil {
				t.Fatal(err)
			}
		}, "not running"},
		{"a task is not done", true, true, func(r *missionRig, a tasks.Task) {
			r.add(spec{title: "late", files: "l.go"})
		}, "not done or verified"},
		{"a task is blocked", true, true, func(r *missionRig, a tasks.Task) {
			x := r.add(spec{title: "late", files: "l.go"})
			r.toReview(x.ID)
			if _, err := r.store.BlockSessionTask(tbg, "default", r.sid, x.ID, "stuck", operator); err != nil {
				t.Fatal(err)
			}
		}, "blocked"},
		{"a merge is pending", true, true, func(r *missionRig, a tasks.Task) {
			art := tasks.ArtifactInput{Kind: tasks.ArtifactBranch, Ref: "mission/a"}
			if _, err := r.store.AddArtifact(tbg, "default", r.sid, a.ID, art, operator); err != nil {
				t.Fatal(err)
			}
		}, "not merged"},
		{"no integration task", false, false, nil, "no integration check"},
		{"integration not settled", true, false, nil, "not done or verified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, a, _ := closeRig(t, tc.integration, tc.settle)
			if tc.mutate != nil {
				tc.mutate(r, a)
			}
			err := r.tryClose()
			var ref *closeRefusal
			if !errors.As(err, &ref) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want a refusal containing %q", err, tc.want)
			}
			if st := r.session().State; st == tasks.SessionDone {
				t.Fatalf("the session closed despite the refusal")
			}
			if tc.integration && tc.settle {
				// nothing was attached either
				d, _ := r.store.SessionTask(tbg, "default", r.sid, r.integrationID(), operator)
				for _, art := range d.Artifacts {
					if art.Ref == "final-report" {
						t.Fatal("a final report was attached to a refused close")
					}
				}
			}
		})
	}
}

func (r *missionRig) integrationID() int64 {
	it, _ := taskByTitle(r.t, r, integrationTitle)
	return it.ID
}

func TestOperatorCloseClosesAFullySettledMission(t *testing.T) {
	r, _, it := closeRig(t, true, true)
	if err := r.tryClose(); err != nil {
		t.Fatal(err)
	}
	if st := r.session().State; st != tasks.SessionDone {
		t.Fatalf("state %s", st)
	}
	d, _ := r.store.SessionTask(tbg, "default", r.sid, it.ID, operator)
	found := false
	for _, art := range d.Artifacts {
		found = found || art.Ref == "final-report"
	}
	if !found {
		t.Fatal("no final report")
	}
}
