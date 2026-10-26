package dynorg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// End to end: a whole mission run with fakes only (a fake agent exec, a scratch database):
// propose, confirm, plan, approve, execute in dependency order with evidence, verify (one
// failure, one retry), integrate, report and close. Nothing here starts a model, an agent
// or a platform call.

const e2eConv = "conv-e2e"

type e2eRig struct {
	*missionRig
	mu     sync.Mutex
	order  []int64 // tasks in the order their workers started
	probes []error // what a worker's attempts on other tasks answered
	other  tasks.Task
	otherS int64
	execs  int
	// repo, when set, makes every lead an isolated-writers conductor in that repository (a
	// turn of its own each), so writers get worktrees and branches and may run in parallel.
	repo  string
	turns int
}

// exec is the fake exec of every run: a worker attaches the commit evidence of its code task
// as its own bound actor (and tries what it must not), a verifier runs r.verify.
func (e *e2eRig) exec(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
	e.mu.Lock()
	e.execs++
	e.mu.Unlock()
	if verifyRE.MatchString(o.Prompt + o.SystemPrompt) {
		return e.ex.exec(ctx, o, on)
	}
	if m := taskRE.FindStringSubmatch(o.Prompt + o.SystemPrompt); m != nil {
		act := verifierActorOf(e.t, o.Env) // the binding of the worker's environment
		d, err := e.store.SessionTask(tbg, "default", act.Session, act.Task, act)
		if err != nil {
			e.t.Errorf("a worker reading its own task: %v", err)
		}
		e.mu.Lock()
		e.order = append(e.order, act.Task)
		first := len(e.probes) == 0
		e.mu.Unlock()
		if _, err := e.store.AddArtifact(tbg, "default", act.Session, act.Task, tasks.ArtifactInput{Kind: "commit", Ref: "commit-" + act.Label() + "-" + time.Now().Format("150405.000000")}, act); err != nil {
			e.t.Errorf("a worker attaching its evidence: %v", err)
		}
		if strings.Contains(o.Cwd, WorktreeDirName) {
			// an isolated writer edits its own worktree; the conductor commits it on the worker's branch
			if err := os.WriteFile(filepath.Join(o.Cwd, fmt.Sprintf("task-%d.txt", act.Task)), []byte("work of "+act.Label()+"\n"), 0o644); err != nil {
				e.t.Errorf("a worker writing in its worktree: %v", err)
			}
		}
		if first {
			// a worker may finish only its own task: neither a task of its session nor of another
			// session, with its own binding or by naming another session
			sibling := d.Task.ID + 1
			_, e1 := e.store.SessionFinish(tbg, "default", act.Session, sibling, tasks.Outcome{Result: "x"}, act)
			_, e2 := e.store.SessionFinish(tbg, "default", e.otherS, e.other.ID, tasks.Outcome{Result: "x"}, act)
			_, e3 := e.store.SessionClaim(tbg, "default", act.Session, sibling, act, 0)
			wide := act
			wide.Session = e.otherS
			_, e4 := e.store.SessionFinish(tbg, "default", e.otherS, e.other.ID, tasks.Outcome{Result: "x"}, wide)
			e.mu.Lock()
			e.probes = []error{e1, e2, e3, e4}
			e.mu.Unlock()
		}
	}
	return e.ex.exec(ctx, o, on)
}

func (e *e2eRig) conductorOf(m *MissionConfig) *Conductor {
	cfg := Config{Cwd: "/w", Limits: Limits{MaxAgents: 20, MaxConcurrent: 5}, Staffer: &Staffer{Roster: []Model{opus, haiku}, Lead: opus},
		ReadAccess: true, Exec: e.exec, Emit: e.em, Missions: e.mc, Mission: m}
	if e.repo != "" {
		e.turns++
		cfg.Cwd, cfg.Writers, cfg.TurnID = e.repo, WritersIsolated, fmt.Sprintf("t%d", e.turns)
	}
	c := New(tbg, cfg)
	e.t.Cleanup(c.Close)
	return c
}

func newE2E(t *testing.T) *e2eRig {
	t.Helper()
	// nothing may leave the scratch: a home of its own, a dead platform address, and every
	// run goes through the fake exec (counted by the tests)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MONOES_BASE_URL", "http://127.0.0.1:9")
	store := tasks.NewStore(openMissionDB(t))
	r := &missionRig{t: t, store: store, clk: &missionClock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}, ex: newGateExec(), em: &recEmitter{}}
	store.SetClock(r.clk.now)
	r.mc = &MissionRun{Store: store, ProfileID: "default", Poll: 10 * time.Millisecond, Lease: time.Minute}
	e := &e2eRig{missionRig: r}
	// another conversation's mission, with a task ready to take
	se, err := store.CreateSession(tbg, "default", tasks.SessionInput{Title: "someone else's", Goal: "g", ConversationID: "conv-other"}, operator)
	if err != nil {
		t.Fatal(err)
	}
	e.otherS = se.ID
	other, err := store.AddSessionTask(tbg, "default", se.ID, tasks.SessionTaskInput{Title: "foreign", Notes: "n", Acceptance: "rubric: x", SuggestedExecutor: ExecLead}, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveSession(tbg, "default", se.ID, operator); err != nil {
		t.Fatal(err)
	}
	e.other = e.task(other.ID)
	return e
}

func e2ePlan() map[string]any {
	return map[string]any{"title": "Add the json flag", "goal": "Add a --json flag with tests", "tasks": []PlanTask{
		{Ref: "e1", Title: "Flag", Kind: "epic", Acceptance: "the flag works end to end"},
		{Ref: "t1", Title: "Parse the flag", Parent: "e1", Acceptance: "`go test ./cmd -run TestFlag` exits 0", Files: []string{"cmd/flag.go"}, Size: "s", Executor: ExecWorker},
		{Ref: "t2", Title: "Print JSON", Parent: "e1", DependsOn: []string{"t1"}, Acceptance: "`app --json` output contains the key version", Files: []string{"cmd/print.go"}, Size: "m", Executor: ExecWorker},
		{Ref: "t3", Title: "Document it", Parent: "e1", DependsOn: []string{"t2"}, Acceptance: "the file docs/json.md exists", Files: []string{"docs/json.md"}, Size: "xs", Executor: ExecWorker},
		{Ref: "chk", Title: "Integration check", Kind: "check", DependsOn: []string{"t1", "t2", "t3"}, Acceptance: "`go build ./...` and `go test ./...` exit 0", Files: []string{"./"}, Size: "xs", Executor: ExecLead},
	}}
}

// planned drives the lead through propose, confirm and plan, and has the operator approve
// (auto_close_verified on).
func (e *e2eRig) planned() (lead *Conductor, ids map[string]int64) {
	return e.plannedWith(e2ePlan(), 5, true)
}

// plannedWith is planned for a plan of n tasks; auto says whether the operator turns
// auto_close_verified on.
func (e *e2eRig) plannedWith(plan map[string]any, n int, auto bool) (lead *Conductor, ids map[string]int64) {
	t := e.t
	unconfirmed := e.conductorOf(&MissionConfig{Store: e.store, ProfileID: "default", ConversationID: e2eConv, Actor: "mission-lead", Access: "coding"})
	if _, err := unconfirmed.Handle(tbg, ToolMissionPlan, mustJSON(plan)); err == nil {
		t.Fatal("a plan before the user confirmed")
	}
	e.call(unconfirmed, ToolMissionPropose, `{"title":"Add the json flag","goal":"Add a --json flag with tests"}`)
	if n := len(e.em.find(chatevents.EventMissionPropose)); n != 1 {
		t.Fatalf("%d proposals", n)
	}
	if specs := MissionRunToolSpecs(tbg, unconfirmed.cfg.Mission); len(specs) != 0 {
		t.Fatalf("run tools before a confirmed plan: %v", specs)
	}

	lead = e.conductorOf(e.leadOf(e2eConv)) // the user confirmed: missionConfirmed
	var planned struct {
		SessionID int64 `json:"session_id"`
		TaskIDs   []int64
	}
	if err := json.Unmarshal([]byte(e.call(lead, ToolMissionPlan, string(mustJSON(plan)))), &planned); err != nil {
		t.Fatal(err)
	}
	e.sid = planned.SessionID
	e.c = lead
	ids = map[string]int64{}
	stored, err := e.store.PlanOfSession(tbg, "default", e.sid, operator)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Tasks) != n || e.session().State != tasks.SessionDraft {
		t.Fatalf("plan: %d tasks, state %s", len(stored.Tasks), e.session().State)
	}
	for _, tk := range stored.Tasks {
		ids[tk.Title] = tk.ID
	}

	// a draft starts nothing, and the lead cannot approve its own plan
	if res := e.next(lead); len(res.Started) != 0 || e.ex.runCount(ids["Parse the flag"]) != 0 {
		t.Fatalf("a draft started work: %+v", res)
	}
	leadActor := tasks.Actor{Kind: tasks.Agent, Name: "mission-lead", Session: e.sid}
	if _, err := e.store.ApproveSession(tbg, "default", e.sid, leadActor); err == nil {
		t.Fatal("the lead approved its own plan")
	}
	e.approve() // the operator, as Human
	if auto {
		e.autoClose()
	}
	return lead, ids
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// runToVerdict runs one scheduler step and the worker's hand-back, then verifies it.
func (e *e2eRig) runToVerdict(c *Conductor, id int64) {
	e.t.Helper()
	// a verifier's run ends a moment after its verdict: mission_next skips a task with a worker
	eventually(e.t, "no worker on the task", func() bool { return c.taskWorker(id) == nil })
	res := e.next(c)
	if got := e.startedIDs(res); len(got) != 1 || got[0] != id {
		e.t.Fatalf("mission_next started %v, want only #%d: %+v", got, id, res)
	}
	e.waitStatus(id, tasks.StatusReview)
	eventually(e.t, "the worker to end", func() bool { return c.taskWorker(id) == nil })
	if _, err := c.Handle(tbg, ToolMissionVerify, mustJSON(map[string]any{"task_id": id})); err != nil {
		e.t.Fatal(err)
	}
}

func (e *e2eRig) closeOut(c *Conductor, ids map[string]int64) {
	t := e.t
	// every code task is done; the planned integration check is the one thing left
	v := e.finishOf(c)
	if v.Finished || len(v.Unfinished) != 1 || v.Unfinished[0].ID != ids["Integration check"] || v.CreatedTask {
		t.Fatalf("only the planned integration check is left: %+v", v)
	}
	chk := ids["Integration check"]
	res := e.next(c)
	if len(res.Started) != 1 || res.Started[0].TaskID != chk || res.Started[0].Executor != ExecLead || res.Started[0].Brief == "" {
		t.Fatalf("the integration check is the lead's own: %+v", res)
	}
	lead := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName, Session: e.sid, Task: chk}
	if _, err := e.store.SessionFinish(tbg, "default", e.sid, chk, tasks.Outcome{Result: "go build and go test pass"}, lead); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Handle(tbg, ToolMissionVerify, mustJSON(map[string]any{"task_id": chk})); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(chk, tasks.StatusDone)
	v = e.finishOf(c)
	if !v.Finished || e.session().State != tasks.SessionDone || len(v.MergesRemaining) != 0 {
		t.Fatalf("the mission closes: %+v", v)
	}
	if n := len(e.em.find(chatevents.EventMissionReport)); n != 1 {
		t.Fatalf("%d mission.report events", n)
	}
	d, _ := e.store.SessionTask(tbg, "default", e.sid, chk, operator)
	var report bool
	for _, a := range d.Artifacts {
		report = report || a.Kind == "report" && a.Ref == "final-report" && strings.Contains(a.Note, "Mission report")
	}
	if !report {
		t.Fatalf("no final report on the integration task: %+v", d.Artifacts)
	}
	plan, _ := e.store.PlanOfSession(tbg, "default", e.sid, operator)
	n := 0
	for _, tk := range plan.Tasks {
		if tk.Title == integrationTitle {
			n++
		}
		if tk.Kind != tasks.KindEpic && tk.Status != tasks.StatusDone {
			t.Errorf("task #%d %s is %s", tk.ID, tk.Title, tk.Status)
		}
	}
	if n != 1 || e.session().SpentUSD <= 0 {
		t.Fatalf("%d integration tasks, spent %v", n, e.session().SpentUSD)
	}
	if _, err := c.Handle(tbg, ToolMissionFinish, mustJSON(map[string]any{"session": e.otherS})); err == nil {
		t.Fatal("the lead named another conversation's session")
	}
}

func (e *e2eRig) finishOf(c *Conductor) FinishView {
	e.t.Helper()
	var v FinishView
	if err := json.Unmarshal([]byte(e.call(c, ToolMissionFinish, `{}`)), &v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

// untouched asserts the other conversation's mission is exactly as it was.
func (e *e2eRig) untouched() {
	t := e.t
	if tk := e.task(e.other.ID); tk.Status != e.other.Status || tk.Claim != nil || tk.Verified {
		t.Fatalf("another session's task changed: %+v", tk)
	}
	if e.ex.runCount(e.other.ID) != 0 {
		t.Fatal("a worker ran another session's task")
	}
	if _, err := e.c.Handle(tbg, ToolMissionVerify, mustJSON(map[string]any{"task_id": e.other.ID})); err == nil {
		t.Fatal("the lead verified another session's task")
	}
	if _, err := e.c.Handle(tbg, ToolMissionNext, mustJSON(map[string]any{"session": e.otherS})); err == nil {
		t.Fatal("the lead started another session's tasks")
	}
	bound := tasks.Actor{Kind: tasks.Agent, Name: "mission-lead", Session: e.sid}
	if _, err := e.store.SessionClaim(tbg, "default", e.otherS, e.other.ID, bound, 0); err == nil {
		t.Fatal("the lead's binding claimed a task of another session")
	}
}

func TestMissionEndToEnd(t *testing.T) {
	e := newE2E(t)
	lead, ids := e.planned()
	t1, t2, t3 := ids["Parse the flag"], ids["Print JSON"], ids["Document it"]
	e.ex.release(t1, t2, t3)
	fails := 0
	e.verdicts(func(id int64) (bool, string) {
		if id == t2 && fails == 0 {
			fails++
			return false, "criterion 1 not met: the output has no version key"
		}
		return true, "every criterion met"
	})

	e.runToVerdict(lead, t1)
	e.waitStatus(t1, tasks.StatusDone)
	e.runToVerdict(lead, t2) // fails verification once
	e.waitStatus(t2, tasks.StatusReady)
	if tk := e.task(t2); tk.Retries != 1 || tk.Blocked {
		t.Fatalf("after one failure: %+v", tk)
	}
	e.runToVerdict(lead, t2) // retried, then passes
	e.waitStatus(t2, tasks.StatusDone)
	e.runToVerdict(lead, t3)
	e.waitStatus(t3, tasks.StatusDone)

	e.mu.Lock()
	order := append([]int64(nil), e.order...)
	probes := append([]error(nil), e.probes...)
	e.mu.Unlock()
	want := []int64{t1, t2, t2, t3}
	if len(order) != len(want) {
		t.Fatalf("worker order %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("worker order %v, want %v", order, want)
		}
	}
	if len(probes) != 4 {
		t.Fatalf("worker probes %v", probes)
	}
	for i, err := range probes {
		if err == nil {
			t.Errorf("a worker's attempt %d on another task was allowed", i)
		}
	}
	// the retried task carries its failure and the verifier of the second round
	d, _ := e.store.SessionTask(tbg, "default", e.sid, t2, operator)
	if !strings.Contains(strings.Join(failureNotes(d), "|"), "no version key") {
		t.Fatalf("the failure note was lost: %v", failureNotes(d))
	}

	e.closeOut(lead, ids)
	e.untouched()
	e.mu.Lock()
	execs := e.execs
	e.mu.Unlock()
	if execs != 9 { // 4 workers and 5 verifiers, all fakes: no other run was started
		t.Fatalf("%d runs through the fake exec, want 9", execs)
	}
}

func TestMissionEndToEndResumesAfterTheLeadIsDropped(t *testing.T) {
	e := newE2E(t)
	lead, ids := e.planned()
	t1, t2, t3 := ids["Parse the flag"], ids["Print JSON"], ids["Document it"]
	e.verdicts(func(int64) (bool, string) { return true, "ok" })
	e.ex.release(t1, t3) // t2's worker hangs until the turn is killed

	e.runToVerdict(lead, t1)
	e.waitStatus(t1, tasks.StatusDone)
	if res := e.next(lead); len(e.startedIDs(res)) != 1 || e.startedIDs(res)[0] != t2 {
		t.Fatalf("t2 should be running: %+v", res)
	}
	eventually(t, "t2 held", func() bool { tk := e.task(t2); return tk.Status == tasks.StatusInProgress && tk.Claim != nil })

	lead.Close() // the turn is killed mid-run: its worker is cut, its claim given back
	eventually(t, "t2 given back", func() bool { tk := e.task(t2); return tk.Status == tasks.StatusReady && tk.Claim == nil })

	// a new turn: a new Conductor, a new lead of the same conversation, nothing in memory
	e.ex.release(t2)
	lead2 := e.conductorOf(e.leadOf(e2eConv))
	e.c = lead2
	var st struct {
		Session struct{ State string }
		Tasks   []struct {
			ID     int64
			Status string
		}
	}
	if err := json.Unmarshal([]byte(e.call(lead2, ToolMissionStatus, `{}`)), &st); err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, tk := range st.Tasks {
		got[tk.ID] = tk.Status
	}
	if st.Session.State != "running" || got[t1] != "done" || got[t2] != "ready" || got[t3] != "ready" {
		t.Fatalf("status after the restart: %+v", st)
	}
	e.runToVerdict(lead2, t2)
	e.waitStatus(t2, tasks.StatusDone)
	e.runToVerdict(lead2, t3)
	e.waitStatus(t3, tasks.StatusDone)
	e.closeOut(lead2, ids)
	e.untouched()
	if n := e.ex.runCount(t1); n != 1 {
		t.Fatalf("t1 ran %d times: a finished task was redone", n)
	}
}
