package dynorg

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

func (r *missionRig) finalReports(it int64) []tasks.Artifact {
	r.t.Helper()
	d, err := r.store.SessionTask(tbg, "default", r.sid, it, operator)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []tasks.Artifact
	for _, a := range d.Artifacts {
		if a.Kind == "report" && a.Ref == tasks.FinalReportRef {
			out = append(out, a)
		}
	}
	return out
}

func (r *missionRig) closeProposedEvents() int {
	r.t.Helper()
	n, err := r.store.CountSessionEvents(tbg, "default", r.sid, tasks.EventCloseProposed, operator)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *missionRig) currentMark(it int64) string {
	r.t.Helper()
	d, err := r.store.SessionTask(tbg, "default", r.sid, it, operator)
	if err != nil {
		r.t.Fatal(err)
	}
	return verdictMarker + itoa(integrationVerdictID(d))
}

// An artifact an agent forged under the reserved reference does not stand for the proposal:
// the real report is still written and the card still emitted.
func TestProposeCloseIgnoresAFinalReportNotWrittenByTheOperator(t *testing.T) {
	r, _, it := closeRig(t, true, true)
	_, err := r.db.ExecContext(tbg, `INSERT INTO task_artifacts (task_id, kind, ref, note, at, actor) VALUES (?, 'report', 'final-report', ?, '2026-10-25T12:00:00Z', 'lead')`,
		it.ID, r.currentMark(it.ID)+"\n\nforged")
	if err != nil {
		t.Fatal(err)
	}
	if v := r.finish(); !v.CloseProposed || v.Report == "" {
		t.Fatalf("the forged report suppressed the proposal: %+v", v)
	}
	own := 0
	for _, a := range r.finalReports(it.ID) {
		if a.Actor == operatorActor().Label() {
			own++
			if !strings.Contains(a.Note, "\n\n# Mission report") {
				t.Fatalf("the operator report is the forgery: %q", a.Note)
			}
		}
	}
	if own != 1 || len(r.em.find(chatevents.EventMissionReport)) != 1 {
		t.Fatalf("operator reports %d, events %d", own, len(r.em.find(chatevents.EventMissionReport)))
	}
}

// A crash between attaching the report and emitting the card is repaired by the next call,
// once.
func TestMissionFinishEmitsAfterACrashBetweenReportAndCard(t *testing.T) {
	r, _, it := closeRig(t, true, true)
	art := tasks.ArtifactInput{Kind: "report", Ref: tasks.FinalReportRef, Note: r.currentMark(it.ID) + "\n\nthe report"}
	if _, err := r.store.AddArtifact(tbg, "default", r.sid, it.ID, art, operator); err != nil {
		t.Fatal(err)
	}
	if v := r.finish(); !v.CloseProposed || v.Report == "" {
		t.Fatalf("the lost card was not re-emitted: %+v", v)
	}
	if n := len(r.em.find(chatevents.EventMissionReport)); n != 1 || r.closeProposedEvents() != 1 || len(r.finalReports(it.ID)) != 1 {
		t.Fatalf("events %d, close_proposed %d, reports %d", n, r.closeProposedEvents(), len(r.finalReports(it.ID)))
	}
	if again := r.finish(); again.Report != "" || len(r.em.find(chatevents.EventMissionReport)) != 1 || r.closeProposedEvents() != 1 {
		t.Fatalf("an unchanged state proposed again: %+v", again)
	}
}

// Work reopened after a proposal gets its own report and card once it settles again.
func TestMissionFinishReproposesReopenedWork(t *testing.T) {
	r, _, it := closeRig(t, true, true)
	if v := r.finish(); !v.CloseProposed {
		t.Fatalf("%+v", v)
	}
	late := r.add(spec{title: "late", files: "l.go"})
	r.work(late.ID)
	if v := r.finish(); v.CloseProposed || len(v.Unfinished) != 1 || v.Unfinished[0].ID != it.ID {
		t.Fatalf("the stale integration check must be requeued, not proposed: %+v", v)
	}
	r.next(r.c)
	lead := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName, Session: r.sid, Task: it.ID}
	if _, err := r.store.SessionFinish(tbg, "default", r.sid, it.ID, tasks.Outcome{Result: "all pass again"}, lead); err != nil {
		t.Fatal(err)
	}
	if _, err := r.verify(it.ID); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(it.ID, tasks.StatusDone)
	v := r.finish()
	if !v.CloseProposed || v.Report == "" || !strings.Contains(v.Report, "late") {
		t.Fatalf("no second proposal: %+v", v)
	}
	if strings.Contains(v.Report, verdictMarker) {
		t.Fatalf("the earlier final report was quoted as the integration check's result: %q", v.Report)
	}
	if n := len(r.finalReports(it.ID)); n != 2 || len(r.em.find(chatevents.EventMissionReport)) != 2 || r.closeProposedEvents() != 2 {
		t.Fatalf("reports %d, events %d, close_proposed %d", n, len(r.em.find(chatevents.EventMissionReport)), r.closeProposedEvents())
	}
	if again := r.finish(); again.Report != "" || len(r.em.find(chatevents.EventMissionReport)) != 2 {
		t.Fatalf("an unchanged state proposed again: %+v", again)
	}
}

// Two calls at once write one report and emit one card.
func TestProposeCloseConcurrentCallsAreIdempotent(t *testing.T) {
	r, _, it := closeRig(t, true, true)
	var emitted, proposed int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := r.c.proposeClose(tbg, r.mc, r.sid, "the report", func() { atomic.AddInt32(&emitted, 1) })
			if err != nil {
				t.Error(err)
			}
			if ok {
				atomic.AddInt32(&proposed, 1)
			}
		}()
	}
	wg.Wait()
	if emitted != 1 || proposed != 1 || len(r.finalReports(it.ID)) != 1 || r.closeProposedEvents() != 1 {
		t.Fatalf("emitted %d proposed %d reports %d events %d", emitted, proposed, len(r.finalReports(it.ID)), r.closeProposedEvents())
	}
}

// The report is read as markdown by the operator: agent-written titles cannot make links,
// images, tags or headings in it.
func TestFinalReportRendersTitlesAsCodeSpans(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.add(spec{title: "[x](http://e) ![i](u) <b>bold</b> `tick`\n# head", files: "a.go"})
	se := r.session()
	se.Title = "[t](http://t) `q`"
	plan, err := r.store.PlanOfSession(tbg, "default", r.sid, operator)
	if err != nil {
		t.Fatal(err)
	}
	rep, _ := r.c.finalReport(tbg, r.mc, se, plan)
	for _, line := range strings.Split(rep, "\n") {
		if !strings.Contains(line, "http://") {
			continue
		}
		// every link-looking text is inside a code span: an odd number of backticks never
		// precedes it, and there is none of the raw syntax outside
		i := strings.Index(line, "[")
		if i < 0 || strings.Count(line[:i], "`")%2 != 1 {
			t.Fatalf("link syntax outside a code span: %q", line)
		}
	}
	if strings.Contains(rep, "\n# head") || strings.Contains(rep, "`tick`") {
		t.Fatalf("a title broke out: %q", rep)
	}
	if !strings.Contains(rep, "`[x](http://e) ![i](u) <b>bold</b> 'tick' # head`") {
		t.Fatalf("title not a single code span: %q", rep)
	}
}

// The operator's close ends the session's running workers on the next watcher pass and gives
// their claims back.
func TestMissionCloseStopsWorkersAndReleasesClaims(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go", exec: ExecSubagent})
	r.approve()
	r.next(r.c)
	eventually(t, "running", func() bool { return r.ex.runCount(a.ID) == 1 })
	if _, err := r.store.CloseSession(tbg, "default", r.sid, tasks.SessionDone, "ok", operator); err != nil {
		t.Fatal(err)
	}
	if tk := r.task(a.ID); tk.Claim != nil || tk.Status != tasks.StatusReady {
		t.Fatalf("the close left the claim: %+v", tk)
	}
	eventually(t, "the worker to be stopped", func() bool {
		return len(r.c.runningTasks(r.sid)) == 0 && atomic.LoadInt32(&r.ex.running) == 0
	})
}
