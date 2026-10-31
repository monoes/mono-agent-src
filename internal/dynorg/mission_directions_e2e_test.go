package dynorg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// directionSpawner stands in for session_spawn (owned by the spawn branch): it makes the task sessions that the host would
// make below the lead's, as drafts, and counts what it was asked. Nothing here starts a model or an agent.
type directionSpawner struct {
	r     *missionRig
	calls []string
}

func (f *directionSpawner) spawn(kind, title, reason string) tasks.Session {
	f.r.t.Helper()
	f.calls = append(f.calls, kind+":"+title+":"+reason)
	se, err := f.r.store.CreateSession(tbg, "default", tasks.SessionInput{Title: title, Goal: "g of " + title, ParentSessionID: f.r.sid, Kind: kind}, operator)
	if err != nil {
		f.r.t.Fatal(err)
	}
	return se
}

// A whole mission of directions with fakes: research sessions, an evaluation, directions for the picks only, a
// re-evaluation that proposes a drop, then the lead's own tasks, and a close that is only proposed.
func TestMissionOfDirectionsFromResearchToConvergence(t *testing.T) {
	r := newRig(t, rigOpts{conv: "c1"})
	own := r.add(spec{title: "write the synthesis", files: "SYNTHESIS.md"})
	it, err := r.store.AddSessionTask(tbg, "default", r.sid, tasks.SessionTaskInput{Title: integrationTitle, Notes: "final checks", Acceptance: defaultFinalChecks,
		Kind: tasks.KindCheck, DependsOn: []int64{own.ID}, SuggestedExecutor: ExecLead}, operator)
	if err != nil {
		t.Fatal(err)
	}
	r.approve()
	r.autoClose()
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	lead := r.leadOf("c1")
	lead.MaxDirections = 2
	c := r.conductorFor(Limits{MaxAgents: 20, MaxConcurrent: 5}, lead)
	sp := &directionSpawner{r: r}

	// RESEARCH: one question each, as read-only sessions of the lead
	for _, q := range []string{"what do users ask for", "what do competitors ship", "what does it cost to build"} {
		sp.spawn(tasks.SessionKindResearch, q, "parallel")
	}

	// EVALUATE: code ranks, the operator's K (2) holds
	cands := candidatesJSON(4)
	if _, err := r.evaluate(c, ToolMissionEvaluate, evalArgs(map[string]any{"k": 3}, cands)); err == nil {
		t.Fatal("k=3 passed the operator's 2")
	}
	v, err := r.evaluate(c, ToolMissionEvaluate, evalArgs(nil, cands))
	if err != nil || len(v.Picks) != 2 {
		t.Fatalf("%+v %v", v, err)
	}
	v, err = r.evaluate(c, ToolMissionEvaluate, evalArgs(map[string]any{"reviewer": map[string]string{"runtime": "r", "model": "m2", "verdict": "agree"}}, cands))
	if err != nil || v.Reviewed != "agree" {
		t.Fatalf("%+v %v", v, err)
	}

	// DIRECT: only the picks get a direction, each as a draft that waits for the operator
	var dirs []tasks.Session
	for _, title := range v.Picks {
		dirs = append(dirs, sp.spawn(tasks.SessionKindDirection, title, "parallel"))
	}
	all, _ := r.store.ListSessions(tbg, "default", tasks.SessionFilter{}, operator)
	var nDir, nRes int
	for _, s := range all {
		switch s.Kind {
		case tasks.SessionKindDirection:
			nDir++
			if s.State != tasks.SessionDraft || s.ApprovedBy != "" || s.ParentSessionID != r.sid {
				t.Errorf("direction %+v", s)
			}
		case tasks.SessionKindResearch:
			nRes++
		}
	}
	if nDir != 2 || nRes != 3 || len(sp.calls) != 5 {
		t.Fatalf("%d directions, %d research, calls %v", nDir, nRes, sp.calls)
	}

	// RUN: new evidence; the lead re-evaluates with only one slot, and the weaker direction is proposed for a stop
	cur := []map[string]any{{"title": dirs[0].Title, "session_id": dirs[0].ID}, {"title": dirs[1].Title, "session_id": dirs[1].ID}}
	v, err = r.evaluate(c, ToolMissionReevaluate, evalArgs(map[string]any{"reason": "new research landed", "k": 1, "current": cur}, cands))
	if err != nil {
		t.Fatal(err)
	}
	var kept, dropped int64
	for _, d := range v.Decisions {
		switch d.Action {
		case ActKeep:
			kept = d.SessionID
		case ActDrop:
			dropped = d.SessionID
		}
	}
	if kept != dirs[0].ID || dropped != dirs[1].ID {
		t.Fatalf("decisions %+v", v.Decisions)
	}
	if s, _ := r.store.GetSession(tbg, "default", dirs[1].ID, operator); s.State != tasks.SessionDraft || s.Archived {
		t.Fatalf("a proposal stopped the direction: %+v", s)
	}
	if len(sp.calls) != 5 {
		t.Fatalf("the re-evaluation spawned: %v", sp.calls)
	}

	// CONVERGE: the lead's own task, the integration check, and a close that only the operator makes
	r.work(own.ID)
	var fin FinishView
	finish := func() {
		t.Helper()
		fin = FinishView{}
		if err := json.Unmarshal([]byte(r.call(c, ToolMissionFinish, `{}`)), &fin); err != nil {
			t.Fatal(err)
		}
	}
	finish()
	if fin.ReadyToClose || len(fin.Unfinished) != 1 || fin.Unfinished[0].ID != it.ID {
		t.Fatalf("the integration check is open: %+v", fin)
	}
	r.next(c)
	leadActor := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName, Session: r.sid, Task: it.ID}
	if _, err := r.store.SessionFinish(tbg, "default", r.sid, it.ID, tasks.Outcome{Result: "checks pass"}, leadActor); err != nil {
		t.Fatal(err)
	}
	if _, err := r.verify(it.ID); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(it.ID, tasks.StatusDone)
	finish()
	if !fin.ReadyToClose || !fin.CloseProposed || fin.Session.State != "running" {
		t.Fatalf("%+v", fin)
	}
	if se := r.session(); se.State != tasks.SessionRunning {
		t.Fatalf("the mission closed itself: %s", se.State)
	}
	// every decision is in the history, in order
	notes := r.sessionNotes()
	if len(notes) != 3 || !strings.Contains(notes[2], `"a":"drop"`) {
		t.Fatalf("%d records: %v", len(notes), notes)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(notes[2], evaluationNote)), &rec); err != nil || rec["tool"] != ToolMissionReevaluate {
		t.Fatalf("%v %v", rec, err)
	}
}
