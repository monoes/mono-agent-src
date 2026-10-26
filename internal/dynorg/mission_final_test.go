package dynorg

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

func (r *missionRig) finish() FinishView {
	r.t.Helper()
	var v FinishView
	if err := json.Unmarshal([]byte(r.call(r.c, ToolMissionFinish, `{}`)), &v); err != nil {
		r.t.Fatal(err)
	}
	return v
}

// autoClose has a verified task close at once, as the operator may set.
func (r *missionRig) autoClose() {
	r.t.Helper()
	on := true
	if _, err := r.store.UpdateSession(tbg, "default", r.sid, tasks.SessionUpdate{AutoCloseVerified: &on}, operator); err != nil {
		r.t.Fatal(err)
	}
}

// work runs a ready task through a worker and a passing verifier to Done (auto-close is on).
func (r *missionRig) work(id int64) {
	r.t.Helper()
	r.next(r.c)
	r.ex.release(id)
	r.waitStatus(id, tasks.StatusReview)
	eventually(r.t, "the worker to end", func() bool { return r.c.taskWorker(id) == nil })
	if _, err := r.verify(id); err != nil {
		r.t.Fatal(err)
	}
	r.waitStatus(id, tasks.StatusDone)
}

func taskByTitle(t *testing.T, r *missionRig, title string) (tasks.Task, bool) {
	t.Helper()
	plan, err := r.store.PlanOfSession(tbg, "default", r.sid, operator)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range plan.Tasks {
		if tk.Title == title {
			return tk, true
		}
	}
	return tasks.Task{}, false
}

func TestMissionFinishIntegratesReportsAndCloses(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	b := r.add(spec{title: "b", files: "b.go", deps: []int64{a.ID}})
	r.approve()
	r.autoClose()
	r.verdicts(func(int64) (bool, string) { return true, "ok" })

	// b is not done: nothing is created, nothing closes
	r.work(a.ID)
	v := r.finish()
	if v.Finished || len(v.Unfinished) != 1 || v.Unfinished[0].ID != b.ID {
		t.Fatalf("with b open: %+v", v)
	}
	if _, ok := taskByTitle(t, r, integrationTitle); ok || r.session().State != tasks.SessionRunning {
		t.Fatal("an integration task was created, or the session closed, with a task open")
	}

	r.work(b.ID)
	v = r.finish()
	it, ok := taskByTitle(t, r, integrationTitle)
	if !ok || !v.CreatedTask || v.Finished || v.IntegrationTask != it.ID {
		t.Fatalf("every task is done: the integration task is created: %+v", v)
	}
	if it.Kind != tasks.KindCheck || len(it.DependsOn) != 2 || it.SuggestedExecutor != ExecLead || !strings.Contains(it.Acceptance, "rubric:") {
		t.Fatalf("integration task %+v", it)
	}
	if again := r.finish(); again.CreatedTask || len(again.Unfinished) != 1 || again.Unfinished[0].ID != it.ID {
		t.Fatalf("a second call neither creates another nor closes: %+v", again)
	}
	if got, _ := r.store.PlanOfSession(tbg, "default", r.sid, operator); got.Session.Counts.Total != 3 {
		t.Fatalf("%d tasks", got.Session.Counts.Total)
	}

	// the lead runs the checks itself
	res := r.next(r.c)
	if len(res.Started) != 1 || res.Started[0].TaskID != it.ID || res.Started[0].Executor != ExecLead {
		t.Fatalf("the integration task goes to the lead: %+v", res)
	}
	lead := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName, Session: r.sid, Task: it.ID}
	if _, err := r.store.SessionFinish(tbg, "default", r.sid, it.ID, tasks.Outcome{Result: "go build, go test, go vet: all pass"}, lead); err != nil {
		t.Fatal(err)
	}
	if _, err := r.verify(it.ID); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(it.ID, tasks.StatusDone)

	v = r.finish()
	if !v.Finished || v.Session.State != "done" || !strings.Contains(v.Report, "Mission report") {
		t.Fatalf("the mission closes: %+v", v)
	}
	if se := r.session(); se.State != tasks.SessionDone {
		t.Fatalf("session %s", se.State)
	}
	cards := r.em.find(chatevents.EventMissionReport)
	if len(cards) != 1 {
		t.Fatalf("%d mission.report events", len(cards))
	}
	rp := cards[0].(chatevents.MissionReportPayload)
	if rp.SessionID != r.sid || rp.State != "done" || rp.TaskCount != 3 || rp.Done != 3 || len(rp.Unfinished) != 0 || rp.Artifacts < 2 || !strings.Contains(rp.Report, "#"+itoa(a.ID)) {
		t.Fatalf("report payload %+v", rp)
	}
	d, _ := r.store.SessionTask(tbg, "default", r.sid, it.ID, operator)
	var found bool
	for _, art := range d.Artifacts {
		found = found || art.Kind == "report" && art.Ref == "final-report" && strings.Contains(art.Note, "Mission report")
	}
	if !found {
		t.Fatalf("no final-report artifact on the integration task: %+v", d.Artifacts)
	}
	// closing again is a no-op: no second report
	if again := r.finish(); !again.Finished || len(r.em.find(chatevents.EventMissionReport)) != 1 {
		t.Fatalf("a closed mission re-reported: %+v", again)
	}
}

func TestMissionFinishWithAPlannedIntegrationTaskCreatesNone(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	chk, err := r.store.AddSessionTask(tbg, "default", r.sid, tasks.SessionTaskInput{Title: "Integration check", Kind: tasks.KindCheck,
		Acceptance: "`go test ./...` exits 0", DependsOn: []int64{a.ID}}, operator)
	if err != nil {
		t.Fatal(err)
	}
	r.approve()
	r.autoClose()
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	r.work(a.ID)
	v := r.finish()
	if v.CreatedTask || v.Finished || len(v.Unfinished) != 1 || v.Unfinished[0].ID != chk.ID {
		t.Fatalf("the planned integration task is the one to run: %+v", v)
	}
	if got, _ := r.store.PlanOfSession(tbg, "default", r.sid, operator); got.Session.Counts.Total != 2 {
		t.Fatalf("a second integration task was created: %d tasks", got.Session.Counts.Total)
	}
}

func TestMissionFinishDoesNotCloseOverABlockedTask(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.verdicts(func(int64) (bool, string) { return false, "tests fail: TestFlag" })
	r.toReview(a.ID)
	for i := 0; i < 3; i++ {
		if _, err := r.verify(a.ID); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			r.waitStatus(a.ID, tasks.StatusReady)
			// the verifier's run ends a moment after its verdict: the next one may not start before
			eventually(t, "the verifier to end", func() bool { return !r.c.verifierLive(a.ID) })
			r.toReview(a.ID)
		}
	}
	eventually(t, "blocked", func() bool { return r.task(a.ID).Blocked })
	v := r.finish()
	if v.Finished || len(v.Unfinished) != 1 || !v.Unfinished[0].Blocked || !strings.Contains(strings.Join(v.Unfinished[0].FailureNotes, "|"), "TestFlag") {
		t.Fatalf("a blocked task keeps the mission open and shows why: %+v", v)
	}
	if _, ok := taskByTitle(t, r, integrationTitle); ok || r.session().State != tasks.SessionRunning || len(r.em.find(chatevents.EventMissionReport)) != 0 {
		t.Fatal("the mission moved on over a blocked task")
	}
}

func TestMissionFinishListsWriterBranchesNotMerged(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.autoClose()
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	r.work(a.ID)
	if v := r.finish(); !v.CreatedTask {
		t.Fatalf("%+v", v)
	}
	it, _ := taskByTitle(t, r, integrationTitle)
	// the writer that produced a has a branch the lead has not merged
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	var wid string
	for _, art := range d.Artifacts {
		if art.Kind == "report" {
			wid = strings.TrimPrefix(art.Ref, "report:")
		}
	}
	r.c.mu.Lock()
	w := r.c.workers[wid]
	w.branch = "monoagent/turn1-w1"
	r.c.mu.Unlock()
	r.c.recordBranch(tbg, r.mc, w) // what the worker's hand-back records for an isolated writer
	r.next(r.c)
	lead := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName, Session: r.sid, Task: it.ID}
	if _, err := r.store.SessionFinish(tbg, "default", r.sid, it.ID, tasks.Outcome{Result: "checks pass"}, lead); err != nil {
		t.Fatal(err)
	}
	if _, err := r.verify(it.ID); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(it.ID, tasks.StatusDone)
	v := r.finish()
	if v.Finished || len(v.MergesRemaining) != 1 || !strings.Contains(v.MergesRemaining[0], "monoagent/turn1-w1") || !strings.Contains(v.Next, "org_merge") {
		t.Fatalf("an unmerged writer branch keeps the mission open: %+v", v)
	}
	if r.session().State != tasks.SessionRunning {
		t.Fatal("closed with an unmerged branch")
	}
	r.c.recordMerge(tbg, w, nil, w.id) // what org_merge records
	if got := r.task(it.ID); got.Status != tasks.StatusReady || got.Verified {
		t.Fatalf("the merge must invalidate checks of the old tree: %+v", got)
	}
	if v := r.finish(); v.Finished || r.session().State != tasks.SessionRunning {
		t.Fatalf("closed without checking the merged tree: %+v", v)
	}
	r.next(r.c)
	if _, err := r.store.SessionFinish(tbg, "default", r.sid, it.ID, tasks.Outcome{Result: "merged-tree checks pass"}, lead); err != nil {
		t.Fatal(err)
	}
	if _, err := r.verify(it.ID); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(it.ID, tasks.StatusDone)
	if v := r.finish(); !v.Finished || r.session().State != tasks.SessionDone {
		t.Fatalf("after merged-tree checks it closes: %+v", v)
	}
}

func TestMissionRunToolsAreOfferedToAConfirmedLeadWithASession(t *testing.T) {
	st := missionStore(t)
	names := func(m *MissionConfig) string {
		var out []string
		for _, s := range MissionRunToolSpecs(context.Background(), m) {
			out = append(out, s.Name)
		}
		return strings.Join(out, ",")
	}
	conf := &MissionConfig{Store: st, ProfileID: missionProfile, ConversationID: "c1", Actor: "mission-lead", Access: "coding", Confirmed: true}
	if got := names(conf); got != "" {
		t.Errorf("no session yet: %q", got)
	}
	c, _, _ := missionConductor(t, st, "c1", true, nil)
	if _, err := call(t, c, ToolMissionPlan, goodPlan()); err != nil {
		t.Fatal(err)
	}
	want := "mission_next,mission_assign,mission_report,mission_verify,mission_finish"
	if got := names(conf); got != want {
		t.Errorf("confirmed with a session: %q", got)
	}
	if got := names(&MissionConfig{Store: st, ProfileID: missionProfile, ConversationID: "c1", Access: "coding"}); got != "" {
		t.Errorf("unconfirmed: %q", got)
	}
	if got := names(&MissionConfig{Store: st, ProfileID: missionProfile, ConversationID: "c1", Access: "scoped", Confirmed: true}); got != "" {
		t.Errorf("scoped access: %q", got)
	}
	if got := names(&MissionConfig{Store: st, ProfileID: missionProfile, ConversationID: "other", Access: "coding", Confirmed: true}); got != "" {
		t.Errorf("another conversation has no session: %q", got)
	}
	for _, s := range ToolSpecs(true) {
		if strings.HasPrefix(s.Name, "mission_") {
			t.Errorf("%s is in the base tool set of every lead", s.Name)
		}
	}
}

func TestMissionVerifyAndFinishRefuseAnUnconfirmedLead(t *testing.T) {
	st := missionStore(t)
	em := &recEmitter{}
	m := &MissionConfig{Store: st, ProfileID: missionProfile, ConversationID: "c1", Actor: "mission-lead", Access: "coding"}
	c := New(context.Background(), Config{Cwd: "/w", Staffer: &Staffer{Roster: []Model{opus, haiku}, Lead: opus}, Exec: newGateExec().exec, Emit: em,
		Mission: m, Missions: &MissionRun{Store: st, ProfileID: missionProfile}})
	t.Cleanup(c.Close)
	for _, tool := range []string{ToolMissionVerify, ToolMissionFinish} {
		if _, err := c.Handle(context.Background(), tool, json.RawMessage(`{"task_id":1}`)); err == nil || !strings.Contains(err.Error(), "not confirmed") {
			t.Errorf("%s by an unconfirmed lead: %v", tool, err)
		}
	}
}
