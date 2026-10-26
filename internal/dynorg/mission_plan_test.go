package dynorg

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

const missionProfile = "default"

var missionHuman = tasks.Actor{Kind: tasks.Human}

// openMissionDB is a migrated database holding the chats the mission tests plan in: the store
// links an agent's session only to a conversation of the profile that exists.
func openMissionDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testdb.Open(t).DB
	for _, id := range []string{"conv", "conv-1", "conv-2", "conv-9", "conv-e2e", "conv-i", "conv-p", "conv-s", "conv-other", "c1", "c2", "other"} {
		if _, err := db.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES (?, ?, 'test', '', '')`, id, missionProfile); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func missionStore(t *testing.T) *tasks.Store {
	t.Helper()
	return tasks.NewStore(openMissionDB(t))
}

func missionConductor(t *testing.T, st *tasks.Store, conv string, confirmed bool, staffer *Staffer) (*Conductor, *recEmitter, *MissionConfig) {
	t.Helper()
	em := &recEmitter{}
	if staffer == nil {
		staffer = &Staffer{Roster: []Model{opus, haiku}, Lead: opus}
	}
	m := &MissionConfig{Store: st, ProfileID: missionProfile, ConversationID: conv, Actor: "mission-lead", Access: "coding", Confirmed: confirmed}
	c := New(context.Background(), Config{Cwd: "/w", Staffer: staffer, Exec: (&execScript{}).exec, Emit: em, Mission: m})
	t.Cleanup(c.Close)
	return c, em, m
}

func call(t *testing.T, c *Conductor, tool string, args any) (map[string]any, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	out, err := c.Handle(context.Background(), tool, b)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%s result %q: %v", tool, out, err)
	}
	return m, nil
}

func goodPlan() map[string]any {
	return map[string]any{"title": "Add flag", "goal": "Add a --json flag with tests", "tasks": []PlanTask{
		{Ref: "e1", Title: "Flag", Kind: "epic", Acceptance: "the flag works end to end"},
		{Ref: "t1", Title: "Parse the flag", Parent: "e1", Acceptance: "`go test ./cmd -run TestFlag` exits 0", Files: []string{"cmd/flag.go"}, Size: "s"},
		{Ref: "t2", Title: "Print JSON", Parent: "e1", DependsOn: []string{"t1"}, Acceptance: "`app --json` output contains the key version", Files: []string{"cmd/print.go"}, Size: "m", Risk: "low"},
	}}
}

func TestMissionPlanWritesDraftSessionBoundToConversation(t *testing.T) {
	st := missionStore(t)
	c, em, _ := missionConductor(t, st, "conv-1", true, nil)
	res, err := call(t, c, ToolMissionPlan, goodPlan())
	if err != nil {
		t.Fatal(err)
	}
	id := int64(res["session_id"].(float64))
	se, err := st.GetSession(context.Background(), missionProfile, id, missionHuman)
	if err != nil {
		t.Fatal(err)
	}
	if se.ConversationID != "conv-1" || se.State != tasks.SessionDraft || se.CreatedBy != "mission-lead" {
		t.Fatalf("session %+v", se)
	}
	plan, _ := st.PlanOfSession(context.Background(), missionProfile, id, missionHuman)
	if len(plan.Tasks) != 3 {
		t.Fatalf("%d tasks", len(plan.Tasks))
	}
	byTitle := map[string]tasks.Task{}
	for _, tk := range plan.Tasks {
		byTitle[tk.Title] = tk
		if tk.Status != tasks.StatusInbox {
			t.Errorf("%s is %s in a draft", tk.Title, tk.Status)
		}
	}
	print := byTitle["Print JSON"]
	if len(print.DependsOn) != 1 || print.DependsOn[0] != byTitle["Parse the flag"].ID || print.ParentID != byTitle["Flag"].ID {
		t.Errorf("links lost: %+v", print)
	}
	if meta, ok := ParseTaskMeta(print.Notes); !ok || meta.Size != "m" || meta.Files[0] != "cmd/print.go" {
		t.Errorf("meta %+v", meta)
	}
	// The lead cannot approve (Phase 1) and the plan has a review brief.
	if _, err := st.ApproveSession(context.Background(), missionProfile, id, tasks.Actor{Kind: tasks.Agent, Name: "mission-lead", Session: id}); err == nil {
		t.Error("agent approved its own plan")
	}
	if res["review_rounds_left"].(float64) != 2 || !strings.Contains(res["review"].(map[string]any)["brief"].(string), "Print JSON") {
		t.Errorf("review: %+v", res)
	}
	cards := em.find(chatevents.EventMissionPlan)
	if len(cards) != 1 {
		t.Fatalf("%d plan cards", len(cards))
	}
	card := cards[0].(chatevents.MissionPlanPayload)
	if card.SessionID != id || card.TaskCount != 3 || card.Title != "Add flag" || card.EstimatedBudgetUSD <= 0 {
		t.Errorf("card %+v", card)
	}
}

func TestMissionPlanRejectedWritesNothing(t *testing.T) {
	st := missionStore(t)
	c, em, _ := missionConductor(t, st, "conv-1", true, nil)
	p := goodPlan()
	ts := p["tasks"].([]PlanTask)
	ts[1].Size = "l"
	ts[2].Acceptance = ""
	_, err := call(t, c, ToolMissionPlan, p)
	var rej *RejectedError
	if err == nil || !strings.Contains(err.Error(), "task t1: size l is too big") || !strings.Contains(err.Error(), "task t2: acceptance is missing") {
		t.Fatalf("err %v", err)
	}
	_ = rej
	list, _ := st.ListSessions(context.Background(), missionProfile, tasks.SessionFilter{}, missionHuman)
	if len(list) != 0 {
		t.Errorf("a rejected plan left %d sessions", len(list))
	}
	if len(em.find(chatevents.EventMissionPlan)) != 0 {
		t.Error("plan card for a rejected plan")
	}
}

func TestMissionOneActiveSessionPerConversation(t *testing.T) {
	st := missionStore(t)
	c, _, _ := missionConductor(t, st, "conv-1", true, nil)
	if _, err := call(t, c, ToolMissionPlan, goodPlan()); err != nil {
		t.Fatal(err)
	}
	different := goodPlan()
	different["title"] = "Another goal"
	if _, err := call(t, c, ToolMissionPlan, different); err == nil || !strings.Contains(err.Error(), "already has an open mission session") {
		t.Fatalf("second plan: %v", err)
	}
	list, _ := st.ListSessions(context.Background(), missionProfile, tasks.SessionFilter{}, missionHuman)
	if len(list) != 1 {
		t.Fatalf("%d sessions", len(list))
	}
	// Once the operator stops it, the conversation may plan another (many over its life).
	if _, err := st.StopSession(context.Background(), missionProfile, list[0].ID, missionHuman); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, c, ToolMissionPlan, goodPlan()); err != nil {
		t.Fatalf("plan after stop: %v", err)
	}
	if list, _ = st.ListSessions(context.Background(), missionProfile, tasks.SessionFilter{}, missionHuman); len(list) != 2 {
		t.Fatalf("%d sessions", len(list))
	}
}

func TestMissionToolsCannotTouchAnotherSession(t *testing.T) {
	st := missionStore(t)
	ctx := context.Background()
	// Another conversation's session, with a task.
	other, err := st.CreateSession(ctx, missionProfile, tasks.SessionInput{Title: "Other", ConversationID: "conv-2"}, missionHuman)
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := st.AddSessionTask(ctx, missionProfile, other.ID, tasks.SessionTaskInput{Title: "theirs", Acceptance: "`x` passes"}, missionHuman)
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := missionConductor(t, st, "conv-1", true, nil)
	if _, err := call(t, c, ToolMissionAddTasks, map[string]any{"tasks": []PlanTask{okTask("a")}}); err == nil {
		t.Fatal("add_tasks without a session of its own succeeded")
	}
	if _, err := call(t, c, ToolMissionPlan, goodPlan()); err != nil {
		t.Fatal(err)
	}
	// Depending on, splitting or parenting a task of the other session is refused.
	dep := okTask("x")
	dep.DependsOn = []string{"#" + idStr(otherTask.ID)}
	if _, err := call(t, c, ToolMissionAddTasks, map[string]any{"tasks": []PlanTask{dep}}); err == nil || !strings.Contains(err.Error(), "no such task in this session") {
		t.Errorf("dependency across sessions: %v", err)
	}
	if _, err := call(t, c, ToolMissionSplit, map[string]any{"task_id": otherTask.ID, "parts": []SplitPart{
		{Title: "a", Acceptance: "`go test` exits 0"}, {Title: "b", Acceptance: "`go vet` exits 0"}}}); err == nil {
		t.Error("split of another session's task succeeded")
	}
	plan, _ := st.PlanOfSession(ctx, missionProfile, other.ID, missionHuman)
	if len(plan.Tasks) != 1 || plan.Tasks[0].Title != "theirs" || plan.Tasks[0].Status != tasks.StatusInbox {
		t.Errorf("other session changed: %+v", plan.Tasks)
	}
	// And status shows only this conversation's session.
	res, _ := call(t, c, ToolMissionStatus, map[string]any{})
	if res["session"].(map[string]any)["title"] != "Add flag" {
		t.Errorf("status %+v", res["session"])
	}
}

func TestMissionAddTasksAndSplit(t *testing.T) {
	st := missionStore(t)
	ctx := context.Background()
	c, _, _ := missionConductor(t, st, "conv-1", true, nil)
	res, _ := call(t, c, ToolMissionPlan, goodPlan())
	id := int64(res["session_id"].(float64))
	ids := res["task_ids"].(map[string]any)
	t2 := int64(ids["t2"].(float64))
	// Add a task depending on an existing one.
	extra := okTask("n")
	extra.DependsOn = []string{"#" + idStr(t2)}
	r, err := call(t, c, ToolMissionAddTasks, map[string]any{"tasks": []PlanTask{extra}})
	if err != nil || r["task_count"].(float64) != 4 {
		t.Fatalf("%v %+v", err, r)
	}
	// Split t2 in two: a task that waited for it waits for both.
	if _, err := call(t, c, ToolMissionSplit, map[string]any{"task_id": t2, "parts": []SplitPart{
		{Title: "p1", Acceptance: "`go test ./a` exits 0"}, {Title: "p2", Acceptance: "`go test ./b` exits 0"}}}); err != nil {
		t.Fatal(err)
	}
	plan, _ := st.PlanOfSession(ctx, missionProfile, id, missionHuman)
	var n tasks.Task
	for _, tk := range plan.Tasks {
		if tk.Title == "Do n" {
			n = tk
		}
	}
	if len(n.DependsOn) != 2 {
		t.Errorf("dependent waits for %v", n.DependsOn)
	}
	// A bad split is rejected before the store.
	if _, err := call(t, c, ToolMissionSplit, map[string]any{"task_id": n.ID, "parts": []SplitPart{{Title: "only", Acceptance: "`x` exits 0"}}}); err == nil {
		t.Error("one-part split accepted")
	}
}

func TestMissionPlanSurvivesNewConductor(t *testing.T) {
	st := missionStore(t)
	c1, _, _ := missionConductor(t, st, "conv-1", true, nil)
	res, _ := call(t, c1, ToolMissionPlan, goodPlan())
	id := res["session_id"].(float64)
	c1.Close()

	c2, _, _ := missionConductor(t, st, "conv-1", true, nil)
	got, err := call(t, c2, ToolMissionStatus, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	se := got["session"].(map[string]any)
	if se["id"].(float64) != id || se["state"] != "draft" || len(got["tasks"].([]any)) != 3 {
		t.Fatalf("status after restart: %+v", got)
	}
	first := got["tasks"].([]any)[1].(map[string]any)
	if first["acceptance"] == "" || first["size"] == nil || first["files"] == nil {
		t.Errorf("task lost its plan data: %+v", first)
	}
	if !strings.Contains(got["next"].(string), "Do not execute") {
		t.Errorf("next: %v", got["next"])
	}
	// The new conductor continues the session instead of starting another.
	another := goodPlan()
	another["title"] = "Another goal"
	if _, err := call(t, c2, ToolMissionPlan, another); err == nil {
		t.Error("second plan after restart")
	}
	// A conversation without a session sees none.
	c3, _, _ := missionConductor(t, st, "conv-9", true, nil)
	if got, _ := call(t, c3, ToolMissionStatus, map[string]any{}); got["session"] != nil {
		t.Errorf("conv-9 sees %+v", got["session"])
	}
}

func TestMissionReplanIsBoundedToTwoRounds(t *testing.T) {
	st := missionStore(t)
	ctx := context.Background()
	c, em, m := missionConductor(t, st, "conv-1", true, nil)
	res, _ := call(t, c, ToolMissionPlan, goodPlan())
	id := int64(res["session_id"].(float64))
	for round := 1; round <= 2; round++ {
		n := okTask("r" + idStr(int64(round)))
		r, err := call(t, c, ToolMissionReplan, map[string]any{"note": "reviewer asked", "add": []PlanTask{n}})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if got := int(r["review_rounds_left"].(float64)); got != 2-round {
			t.Errorf("round %d: %d left", round, got)
		}
		if round == 2 && r["review"] != nil {
			t.Error("a reviewer brief after the last round")
		}
	}
	before, _ := st.PlanOfSession(ctx, missionProfile, id, missionHuman)
	if _, err := call(t, c, ToolMissionReplan, map[string]any{"note": "again", "add": []PlanTask{okTask("late")}}); err == nil || !strings.Contains(err.Error(), "review rounds") {
		t.Fatalf("third round: %v", err)
	}
	after, _ := st.PlanOfSession(ctx, missionProfile, id, missionHuman)
	if len(after.Tasks) != len(before.Tasks) || m.round(context.Background(), id) != 2 {
		t.Errorf("third round changed the plan: %d -> %d tasks, round %d", len(before.Tasks), len(after.Tasks), m.round(context.Background(), id))
	}
	cards := em.find(chatevents.EventMissionPlan)
	if last := cards[len(cards)-1].(chatevents.MissionPlanPayload); last.Round != 2 || last.TaskCount != 5 {
		t.Errorf("last card %+v", last)
	}
	// A replan needs a note and a change.
	if _, err := call(t, c, ToolMissionReplan, map[string]any{"add": []PlanTask{okTask("q")}}); err == nil {
		t.Error("replan without a note")
	}
}

func TestMissionReplanAfterApprovalIsNotARound(t *testing.T) {
	st := missionStore(t)
	ctx := context.Background()
	c, _, m := missionConductor(t, st, "conv-1", true, nil)
	res, _ := call(t, c, ToolMissionPlan, goodPlan())
	id := int64(res["session_id"].(float64))
	if _, err := st.ApproveSession(ctx, missionProfile, id, missionHuman); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxReviewRounds+2; i++ {
		if _, err := call(t, c, ToolMissionReplan, map[string]any{"note": "found more", "add": []PlanTask{okTask("m" + idStr(int64(i)))}}); err != nil {
			t.Fatalf("replan %d: %v", i, err)
		}
	}
	if m.round(context.Background(), id) != 0 {
		t.Errorf("rounds counted after approval: %d", m.round(context.Background(), id))
	}
	plan, _ := st.PlanOfSession(ctx, missionProfile, id, missionHuman)
	for _, tk := range plan.Tasks {
		if strings.HasPrefix(tk.Title, "Do m") && tk.Status != tasks.StatusReady {
			t.Errorf("%s is %s after approval", tk.Title, tk.Status)
		}
	}
}

func TestMissionToolsGatedByAccessAndConfirmation(t *testing.T) {
	st := missionStore(t)
	names := func(m *MissionConfig) string {
		var n []string
		for _, s := range MissionPlanToolSpecs(m) {
			n = append(n, s.Name)
		}
		return strings.Join(n, ",")
	}
	for _, level := range []string{"scoped", "read", "chat"} {
		if got := names(&MissionConfig{Store: st, Access: level, Confirmed: true}); got != "" {
			t.Errorf("access %q got tools %s", level, got)
		}
	}
	if got := names(&MissionConfig{Store: st, Access: "coding"}); got != ToolMissionPropose {
		t.Errorf("unconfirmed: %s", got)
	}
	for _, level := range []string{"", "coding", "workspace", "full"} {
		got := names(&MissionConfig{Store: st, Access: level, Confirmed: true})
		for _, want := range []string{ToolMissionPlan, ToolMissionAddTasks, ToolMissionSplit, ToolMissionStatus, ToolMissionReplan} {
			if !strings.Contains(got, want) {
				t.Errorf("access %q lacks %s: %s", level, want, got)
			}
		}
		if strings.Contains(got, "mission_next") || strings.Contains(got, "mission_assign") || strings.Contains(got, "mission_verify") || strings.Contains(got, "mission_report") {
			t.Errorf("phase 4/5 tools offered: %s", got)
		}
	}
	// An unconfirmed lead cannot plan even by calling the tool.
	c, _, _ := missionConductor(t, st, "conv-1", false, nil)
	if _, err := call(t, c, ToolMissionPlan, goodPlan()); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("plan unconfirmed: %v", err)
	}
	// A worker the lead let spawn never reaches the mission tools.
	if _, err := c.handle(context.Background(), &worker{}, ToolMissionStatus, nil); err == nil {
		t.Error("a worker called a mission tool")
	}
}

func TestMissionProposeEmitsEvent(t *testing.T) {
	st := missionStore(t)
	c, em, _ := missionConductor(t, st, "conv-1", false, nil)
	if _, err := call(t, c, ToolMissionPropose, map[string]any{"title": "Big", "goal": "rewrite it", "reason": "60 files"}); err != nil {
		t.Fatal(err)
	}
	ev := em.find(chatevents.EventMissionPropose)
	if len(ev) != 1 || ev[0].(chatevents.MissionProposePayload).Title != "Big" {
		t.Fatalf("events %+v", ev)
	}
	if _, err := call(t, c, ToolMissionPropose, map[string]any{"title": "", "goal": ""}); err == nil {
		t.Error("empty proposal accepted")
	}
	list, _ := st.ListSessions(context.Background(), missionProfile, tasks.SessionFilter{}, missionHuman)
	if len(list) != 0 {
		t.Error("propose created a session")
	}
	// A confirmed lead does not propose again.
	c2, _, _ := missionConductor(t, st, "conv-1", true, nil)
	if _, err := call(t, c2, ToolMissionPropose, map[string]any{"title": "x", "goal": "y"}); err == nil {
		t.Error("proposal after confirmation")
	}
}

func idStr(n int64) string { b, _ := json.Marshal(n); return string(b) }

// The review-round count lives in the store: a new Conductor (a restart) still knows it.
func TestMissionReviewRoundsSurviveARestart(t *testing.T) {
	st := missionStore(t)
	c, _, m := missionConductor(t, st, "conv-1", true, nil)
	res, _ := call(t, c, ToolMissionPlan, goodPlan())
	id := int64(res["session_id"].(float64))
	if _, err := call(t, c, ToolMissionReplan, map[string]any{"note": "one", "add": []PlanTask{okTask("r1")}}); err != nil {
		t.Fatal(err)
	}
	n, err := st.CountSessionEvents(context.Background(), missionProfile, id, tasks.EventPlanRevised, missionHuman)
	if err != nil || n != 1 {
		t.Fatalf("plan_revised events: %d %v", n, err)
	}
	c2, _, m2 := missionConductor(t, st, "conv-1", true, nil)
	if m2 == m || m2.round(context.Background(), id) != 1 {
		t.Fatalf("a new conductor sees %d rounds, want 1", m2.round(context.Background(), id))
	}
	if _, err := call(t, c2, ToolMissionReplan, map[string]any{"note": "two", "add": []PlanTask{okTask("r2")}}); err != nil {
		t.Fatal(err)
	}
	c3, _, _ := missionConductor(t, st, "conv-1", true, nil)
	if _, err := call(t, c3, ToolMissionReplan, map[string]any{"note": "three", "add": []PlanTask{okTask("r3")}}); err == nil || !strings.Contains(err.Error(), "review rounds") {
		t.Fatalf("a third round after two restarts: %v", err)
	}
}

func TestMissionSplitChildrenCarryTheirOwnFilesAndSize(t *testing.T) {
	st := missionStore(t)
	ctx := context.Background()
	c, _, _ := missionConductor(t, st, "conv-1", true, nil)
	res, _ := call(t, c, ToolMissionPlan, goodPlan())
	ids := res["task_ids"].(map[string]any)
	t2 := int64(ids["t2"].(float64))
	if _, err := call(t, c, ToolMissionSplit, map[string]any{"task_id": t2, "parts": []SplitPart{
		{Title: "p1", Acceptance: "`go test ./a` exits 0", Files: []string{"cmd/a.go"}, Size: "xs"},
		{Title: "p2", Acceptance: "`go test ./b` exits 0"}}}); err != nil {
		t.Fatal(err)
	}
	plan, _ := st.PlanOfSession(ctx, missionProfile, int64(res["session_id"].(float64)), missionHuman)
	got := map[string]TaskMeta{}
	for _, tk := range plan.Tasks {
		if tk.Title == "p1" || tk.Title == "p2" {
			m, ok := ParseTaskMeta(tk.Notes)
			if !ok {
				t.Fatalf("%s has no meta: %q", tk.Title, tk.Notes)
			}
			got[tk.Title] = m
		}
	}
	if m := got["p1"]; len(m.Files) != 1 || m.Files[0] != "cmd/a.go" || m.Size != "xs" || m.Risk != "low" {
		t.Errorf("p1 meta %+v: its own files and size, the parent's risk", m)
	}
	if m := got["p2"]; len(m.Files) != 1 || m.Files[0] != "cmd/print.go" || m.Size != "m" {
		t.Errorf("p2 meta %+v: the parent's", m)
	}
	// a bad size is rejected before the store
	if _, err := call(t, c, ToolMissionSplit, map[string]any{"task_id": t2, "parts": []SplitPart{
		{Title: "x", Acceptance: "`a` exits 0", Size: "l"}, {Title: "y", Acceptance: "`b` exits 0"}}}); err == nil {
		t.Error("size l accepted in a split")
	}
}
