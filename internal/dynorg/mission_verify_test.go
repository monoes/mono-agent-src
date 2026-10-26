package dynorg

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// verifierActorOf is who a verifier's `task` commands act as: the binding its environment
// holds, exactly as the CLI reads it.
func verifierActorOf(t *testing.T, env map[string]string) tasks.Actor {
	t.Helper()
	sid, err1 := strconv.ParseInt(env["MONOAGENT_TASK_SESSION"], 10, 64)
	tid, err2 := strconv.ParseInt(env["MONOAGENT_TASK_ID"], 10, 64)
	if err1 != nil || err2 != nil {
		t.Fatalf("env %v", env)
	}
	return tasks.Actor{Kind: tasks.Agent, Name: env["MONOAGENT_ACTOR"], Session: sid, Task: tid, Verifier: env["MONOAGENT_TASK_ROLE"] == "verifier"}
}

// verdictExec makes the rig's verifiers record a verdict through the store as the bound actor.
func (r *missionRig) verdicts(pass func(task int64) (bool, string)) {
	r.ex.verify = func(ctx context.Context, id int64, o monomind.ExecOptions) (*monomind.TurnResult, error) {
		actor := verifierActorOf(r.t, o.Env)
		ok, note := pass(id)
		// the verifier's binding lets it comment on the task in Review, which no other agent may
		if _, err := r.store.SessionComment(tbg, "default", actor.Session, id, "ran the checks", actor); err != nil {
			r.t.Errorf("a verifier's comment on its task in Review: %v", err)
		}
		if _, err := r.store.VerifySessionTask(tbg, "default", actor.Session, id, ok, note, actor); err != nil {
			return nil, err
		}
		if !ok {
			// the retry's worker attaches its new commit: evidence from before the failure does not count
			art := tasks.ArtifactInput{Kind: "commit", Ref: "retry-commit-of-" + strconv.FormatInt(id, 10)}
			_, _ = r.store.AddArtifact(tbg, "default", actor.Session, id, art, operator)
		}
		return &monomind.TurnResult{SawDone: true, ResultText: "verdict recorded", StopReason: monomind.StopEndTurn, CostUSD: 0.01, HasCostUSD: true}, nil
	}
}

// reviewed runs the rig's task a to Review through a worker.
func (r *missionRig) toReview(ids ...int64) {
	r.t.Helper()
	// mission_next skips a task that still has a worker, and a verifier's run ends a moment
	// after its verdict sent the task back to Ready: wait for it, or the step may start nothing
	for _, id := range ids {
		id := id
		eventually(r.t, "no worker on the task", func() bool { return r.c.taskWorker(id) == nil })
	}
	r.next(r.c)
	r.ex.release(ids...)
	for _, id := range ids {
		r.waitStatus(id, tasks.StatusReview)
		eventually(r.t, "the worker to end", func() bool { return r.c.taskWorker(id) == nil })
	}
}

func (r *missionRig) verify(id int64) (VerifyView, error) {
	r.t.Helper()
	out, err := r.c.Handle(tbg, ToolMissionVerify, json.RawMessage(`{"task_id":`+strconv.FormatInt(id, 10)+`}`))
	var v VerifyView
	if err == nil {
		if e := json.Unmarshal([]byte(out), &v); e != nil {
			r.t.Fatal(e)
		}
	}
	return v, err
}

func TestMissionVerifyStartsADifferentAgentReadOnlyAndBound(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.verdicts(func(int64) (bool, string) { return true, "every criterion met" })
	r.toReview(a.ID)
	exMeta := r.ex.models[a.ID]
	v, err := r.verify(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the verdict", func() bool { return r.task(a.ID).Verified })
	runs := r.ex.vruns[a.ID]
	if len(runs) != 1 {
		t.Fatalf("verifier runs %d", len(runs))
	}
	o := runs[0]
	if o.Runtime+"/"+o.Model == exMeta || v.SameModel {
		t.Errorf("verifier on %s/%s, executor on %s: want another model", o.Runtime, o.Model, exMeta)
	}
	if o.Access == "full" || o.Access == "" {
		t.Errorf("a verifier is read only, got access %q", o.Access)
	}
	if o.Env["MONOAGENT_TASK_ROLE"] != "verifier" || o.Env["MONOAGENT_TASK_ID"] != strconv.FormatInt(a.ID, 10) || o.Env["MONOAGENT_ACTOR"] != "v1-t1" {
		t.Errorf("verifier env %v", o.Env)
	}
	if !strings.Contains(o.Prompt, "criteria of a") || !strings.Contains(o.Prompt, "report for task") || !strings.Contains(o.Prompt, "task verify") {
		t.Errorf("brief lacks acceptance, the executor's report or the verify command:\n%s", o.Prompt)
	}
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	var by string
	for _, e := range d.Events {
		if e.Kind == "verified" {
			by = e.Actor
		}
	}
	if by != "v1-t1" || r.task(a.ID).Status != tasks.StatusReview {
		t.Fatalf("verified by %q, status %s: a pass marks verified and the operator closes", by, r.task(a.ID).Status)
	}
}

func TestMissionVerifyFailRetriesThenBlocksAndReportsNotes(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.verdicts(func(int64) (bool, string) { return false, "criterion 2 not met: the flag is not parsed" })
	r.toReview(a.ID)
	for round := 1; round <= 3; round++ {
		if _, err := r.verify(a.ID); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if round < 3 {
			r.waitStatus(a.ID, tasks.StatusReady)
			if tk := r.task(a.ID); tk.Retries != round || tk.Blocked {
				t.Fatalf("round %d: %+v", round, tk)
			}
			r.toReview(a.ID) // the executor reworks it (its gate is open for good)
			continue
		}
		eventually(t, "blocked", func() bool { return r.task(a.ID).Blocked })
	}
	if tk := r.task(a.ID); tk.Status != tasks.StatusReview || !tk.Blocked || tk.Verified {
		t.Fatalf("after the retry cap: %+v", tk)
	}
	if _, err := r.verify(a.ID); err == nil {
		t.Fatal("a blocked task started another verifier")
	}
	// the lead sees the failure notes in status and in the report
	var rep ReportView
	if err := json.Unmarshal([]byte(r.call(r.c, ToolMissionReport, `{}`)), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Tasks) != 1 || !rep.Tasks[0].Blocked || rep.Tasks[0].Retries != 3 || len(rep.Tasks[0].FailureNotes) == 0 || !strings.Contains(strings.Join(rep.Tasks[0].FailureNotes, "|"), "flag is not parsed") {
		t.Fatalf("report: %+v", rep.Tasks)
	}
}

func TestMissionVerifierBindingCannotTouchOtherTasks(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	other := r.add(spec{title: "other", files: "o.go", exec: ExecLead})
	r.approve()
	var errsMu sync.Mutex
	var errs []error
	r.ex.verify = func(ctx context.Context, id int64, o monomind.ExecOptions) (*monomind.TurnResult, error) {
		act := verifierActorOf(t, o.Env)
		_, e1 := r.store.SessionClaim(tbg, "default", act.Session, other.ID, act, 0)
		_, e2 := r.store.SessionFinish(tbg, "default", act.Session, id, tasks.Outcome{Result: "x"}, act)
		_, e3 := r.store.VerifySessionTask(tbg, "default", act.Session, other.ID, true, "", act)
		_, e4 := r.store.SessionClaim(tbg, "default", act.Session, id, act, 0)
		errsMu.Lock()
		errs = []error{e1, e2, e3, e4}
		errsMu.Unlock()
		return &monomind.TurnResult{SawDone: true, ResultText: "tried", StopReason: monomind.StopEndTurn}, nil
	}
	r.next(r.c)
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the worker to end", func() bool { return r.c.taskWorker(a.ID) == nil })
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the verifier", func() bool { errsMu.Lock(); defer errsMu.Unlock(); return len(errs) == 4 })
	errsMu.Lock()
	got := errs
	errsMu.Unlock()
	for i, e := range got {
		if e == nil {
			t.Errorf("verifier action %d was allowed", i)
		}
	}
	if tk := r.task(other.ID); tk.Claim == nil || tk.Claim.By != "lead" || tk.Verified {
		t.Fatalf("the other task was touched: %+v", tk)
	}
	if tk := r.task(a.ID); tk.Status != tasks.StatusReview || tk.Claim != nil || tk.Verified {
		t.Fatalf("its own task moved without a verdict: %+v", tk)
	}
	// ended with no verdict: a comment says so and another verifier may start
	eventually(t, "the no-verdict comment", func() bool {
		d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
		for _, e := range d.Events {
			if e.Kind == "comment" && e.Actor == "v1-t1" && strings.Contains(e.Note, "without a verdict") {
				return true
			}
		}
		return false
	})
}

func TestMissionVerifyRefusals(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	b := r.add(spec{title: "b", files: "b.go"})
	r.approve()
	if _, err := r.verify(a.ID); err == nil {
		t.Error("a Ready task was verified")
	}
	if r.ex.vruns[a.ID] != nil {
		t.Error("a verifier ran for a task not in review")
	}
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	r.toReview(a.ID, b.ID)
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "verified", func() bool { return r.task(a.ID).Verified })
	if _, err := r.verify(a.ID); err == nil {
		t.Error("a verified task started another verifier")
	}
	if _, err := r.verify(9999); err == nil {
		t.Error("a task that does not exist")
	}
	if n := len(r.ex.vruns[b.ID]); n != 0 {
		t.Errorf("b was never verified but %d verifiers ran", n)
	}
}

func TestMissionAutoCloseVerifiedThroughTheTool(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	on := true
	if _, err := r.store.UpdateSession(tbg, "default", r.sid, tasks.SessionUpdate{AutoCloseVerified: &on}, operator); err != nil {
		t.Fatal(err)
	}
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	r.toReview(a.ID)
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(a.ID, tasks.StatusDone)
}

func TestMissionCodeTaskWithoutEvidenceIsGivenBack(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go", noEvidence: true})
	r.approve()
	r.next(r.c)
	r.ex.release(a.ID)
	eventually(t, "the worker to end", func() bool { return r.ex.runCount(a.ID) == 1 && r.c.taskWorker(a.ID) == nil })
	eventually(t, "the task given back", func() bool { tk := r.task(a.ID); return tk.Status == tasks.StatusReady && tk.Claim == nil })
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	var said bool
	for _, e := range d.Events {
		said = said || e.Kind == "comment" && strings.Contains(e.Note, "without evidence")
	}
	if !said || countNotices(r.em, "mission.evidence") != 1 {
		t.Fatalf("no comment/notice about the missing evidence: %+v", d.Events)
	}
	for _, e := range d.Events {
		if e.Kind == "result" {
			t.Fatalf("the task was handed to review: %+v", e)
		}
	}
}

func TestVerifierForPrefersAnotherReadyModel(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku}, Lead: opus}
	if m, same := VerifierFor(s, haiku); m.Model != "opus" || same {
		t.Errorf("executor haiku: %+v %v", m, same)
	}
	if m, same := VerifierFor(s, opus); m.Model != "haiku" || same {
		t.Errorf("executor opus: %+v %v", m, same)
	}
	if m, same := VerifierFor(s, Model{}); m.Model != "haiku" || same {
		t.Errorf("unknown executor counts as the lead: %+v %v", m, same)
	}
	stale := haiku
	stale.Stale = true
	if m, same := VerifierFor(&Staffer{Roster: []Model{opus, stale}, Lead: opus}, opus); m.Model != "opus" || !same {
		t.Errorf("only the executor's model is ready: %+v %v", m, same)
	}
}
