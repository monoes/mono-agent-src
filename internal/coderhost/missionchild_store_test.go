package coderhost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/tasks"
)

type mcFix struct {
	st   *Store
	lead Caller
	se   tasks.Session
	ts   *tasks.Store
	ids  []int64
}

func taskIDs(t *testing.T, st *Store, sid int64) []int64 {
	t.Helper()
	rows, err := st.db.Query(`SELECT id FROM tasks WHERE session_id = ? ORDER BY id`, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// newMC makes a lead master with a task session of two tasks (running when approve).
func newMC(t *testing.T, approve bool) *mcFix {
	t.Helper()
	st := newStoreT(t)
	lead, _ := master(t, st, "workspace")
	se, ts := missionFor(t, st, lead.ConversationID, approve)
	return &mcFix{st: st, lead: lead, se: se, ts: ts, ids: taskIDs(t, st, se.ID)}
}

func (f *mcFix) scope() LeadScope { return LeadScope{Caller: f.lead} }

func (f *mcFix) addTasks(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := f.ts.AddSessionTask(context.Background(), "p", f.se.ID, tasks.SessionTaskInput{Title: fmt.Sprintf("extra %d", i), Acceptance: "ok"}, opActor); err != nil {
			t.Fatal(err)
		}
	}
	f.ids = taskIDs(t, f.st, f.se.ID)
}

func (f *mcFix) mark(t *testing.T, i int) ChildRow {
	t.Helper()
	c, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[i], MarkOptions{})
	if err != nil {
		t.Fatalf("mark task %d: %v", i, err)
	}
	return c
}

func (f *mcFix) setPolicy(t *testing.T, sc CallerScope, p PolicyPatch) Policy {
	t.Helper()
	out, err := f.st.SetPolicy(sc, "p", f.se.ID, p)
	if err != nil {
		t.Fatalf("set policy: %v", err)
	}
	return out
}

func ip(n int) *int                                   { return &n }
func sp(s string) *string                             { return &s }
func fp(f float64) *float64                           { return &f }
func rowsOf(t *testing.T, st *Store, q string) string { return tableDump(t, st.db, q) }

// ---- who may mark ----

func TestMarkStoresARowAndAnAuditEventForTheLead(t *testing.T) {
	f := newMC(t, true)
	c := f.mark(t, 0)
	if c.State != ChildMarked || c.Origin != "lead" || c.Attempts != 0 || c.LeadConversationID != f.lead.ConversationID {
		t.Fatalf("row = %+v", c)
	}
	ev, _ := f.st.Events("p", f.se.ID, 10)
	if len(ev) != 1 || ev[0].Kind != "mark" || ev[0].Who != "lead:"+f.lead.ConversationID || ev[0].TaskID != f.ids[0] {
		t.Fatalf("events = %+v", ev)
	}
	again := f.mark(t, 0) // marking twice is one row
	if again.ID != c.ID {
		t.Fatalf("a second mark made a second row: %+v", again)
	}
}

func TestMarkRefusesNonLead(t *testing.T) {
	f := newMC(t, true)
	other, _ := master(t, f.st, "workspace") // a master that does not lead this mission
	for name, sc := range map[string]CallerScope{
		"another master":  LeadScope{Caller: other},
		"the operator":    OperatorScope{},
		"a zero caller":   LeadScope{},
		"a child session": LeadScope{Caller: Caller{ProfileID: "p", ConversationID: spawn(t, f.st, f.lead)}},
		"a master scope":  tokenMasterScope{conv: f.lead.ConversationID},
	} {
		_, err := f.st.Mark(sc, "p", f.se.ID, f.ids[0], MarkOptions{})
		if !errors.Is(err, ErrNotYours) {
			t.Errorf("%s: err = %v, want ErrNotYours", name, err)
		}
	}
	if n := rowsOf(t, f.st, `SELECT * FROM coder_mission_children`); n != "" {
		t.Fatalf("a refused mark left rows: %s", n)
	}
}

func TestMarkRefusesBoundWorker(t *testing.T) {
	f := newMC(t, true)
	// The worker is a descendant of the lead's turn, so it can present the lead's session token; the
	// binding it also holds is what tells it apart.
	_, err := f.st.Mark(LeadScope{Caller: f.lead, BoundToTask: true}, "p", f.se.ID, f.ids[0], MarkOptions{})
	if !errors.Is(err, ErrNotYours) {
		t.Fatalf("a task-bound caller marked: %v", err)
	}
	if _, err := f.st.SetPolicy(LeadScope{Caller: f.lead, BoundToTask: true}, "p", f.se.ID, PolicyPatch{MaxChildren: ip(3)}); !errors.Is(err, ErrNotYours) {
		t.Fatalf("a task-bound caller raised: %v", err)
	}
	if _, err := f.st.Reserve(LeadScope{Caller: f.lead, BoundToTask: true}, "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrNotYours) {
		t.Fatalf("a task-bound caller reserved: %v", err)
	}
}

func TestMarkCrossSessionRefused(t *testing.T) {
	f := newMC(t, true)
	other, _ := master(t, f.st, "workspace")
	se2, _ := missionFor(t, f.st, other.ConversationID, true)
	ids2 := taskIDs(t, f.st, se2.ID)
	refusals := map[string]error{}
	_, refusals["a task of another session"] = f.st.Mark(f.scope(), "p", f.se.ID, ids2[0], MarkOptions{})
	_, refusals["another session's id"] = f.st.Mark(f.scope(), "p", se2.ID, ids2[0], MarkOptions{})
	_, refusals["another session, own task id"] = f.st.Mark(f.scope(), "p", se2.ID, f.ids[0], MarkOptions{})
	_, refusals["no such session"] = f.st.Mark(f.scope(), "p", 99999, f.ids[0], MarkOptions{})
	_, refusals["no such task"] = f.st.Mark(f.scope(), "p", f.se.ID, 99999, MarkOptions{})
	var first string
	for name, err := range refusals {
		if !errors.Is(err, ErrNotYours) {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if first == "" {
			first = err.Error()
		} else if err.Error() != first {
			t.Errorf("%s: the refusal text differs (%q vs %q): it is an oracle", name, err.Error(), first)
		}
	}
	if n := rowsOf(t, f.st, `SELECT * FROM coder_mission_children`); n != "" {
		t.Fatalf("rows after refused marks: %s", n)
	}
}

func TestMarkCrossProfileRefused(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.db.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('q', 'Q')`); err != nil {
		t.Fatal(err)
	}
	// the same conversation id leads a session in another profile
	sq, err := tasks.NewStore(f.st.db).CreateSession(context.Background(), "q", tasks.SessionInput{Title: "t", ConversationID: f.lead.ConversationID}, opActor)
	if err != nil {
		t.Fatal(err)
	}
	tq, err := tasks.NewStore(f.st.db).AddSessionTask(context.Background(), "q", sq.ID, tasks.SessionTaskInput{Title: "x", Acceptance: "ok"}, opActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "q", sq.ID, tq.ID, MarkOptions{}); !errors.Is(err, ErrNotYours) {
		t.Errorf("a profile p caller marked in profile q: %v", err)
	}
	if _, err := f.st.Mark(f.scope(), "p", sq.ID, tq.ID, MarkOptions{}); !errors.Is(err, ErrNotYours) {
		t.Errorf("profile q's session id was accepted in profile p: %v", err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, tq.ID, MarkOptions{}); !errors.Is(err, ErrNotYours) {
		t.Errorf("profile q's task id was accepted in profile p: %v", err)
	}
	// the other profile's session is no mission of this profile for the policy or a reserve either
	if _, err := f.st.SetPolicy(f.scope(), "p", sq.ID, PolicyPatch{MaxChildren: ip(3)}); !errors.Is(err, ErrNotYours) {
		t.Errorf("a policy was set on profile q's session through profile p: %v", err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", sq.ID, tq.ID); !errors.Is(err, ErrNotYours) {
		t.Errorf("profile q's task was reserved through profile p: %v", err)
	}
	for _, tbl := range []string{"coder_mission_children", "coder_mission_policy", "coder_mission_events"} {
		if n := rowsOf(t, f.st, `SELECT * FROM `+tbl); n != "" {
			t.Fatalf("%s after refused calls: %s", tbl, n)
		}
	}
}

func TestMarkNeedsAnOpenMissionAndAnOpenTask(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.ts.Move(context.Background(), "p", f.ids[1], tasks.StatusDone, tasks.Placement{}, opActor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[1], MarkOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a done task was marked: %v", err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{Size: "huge"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad size accepted: %v", err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: -1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative budget accepted: %v", err)
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = 'stopped' WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a stopped mission accepted a mark: %v", err)
	}
}

func TestMarkRefusesDraftApprovalBypass(t *testing.T) {
	f := newMC(t, false) // a draft: the plan is not approved
	before := protectedState(t, f.st.db)
	c := f.mark(t, 0)
	if c.State != ChildMarked {
		t.Fatalf("row = %+v", c)
	}
	for _, q := range []string{`SELECT * FROM coder_session_commands`, `SELECT conversation_id FROM coder_sessions WHERE role = 'child'`} {
		if n := rowsOf(t, f.st, q); n != "" {
			t.Fatalf("a mark in a draft created work (%s): %s", q, n)
		}
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a draft mission reserved a task: %v", err)
	}
	if got, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); got.State != ChildMarked || got.Attempts != 0 {
		t.Fatalf("the refused reserve changed the row: %+v", got)
	}
	after := protectedState(t, f.st.db)
	for k, b := range before {
		if k == "coder_mission_children" || k == "coder_mission_policy" || k == "coder_mission_events" {
			continue
		}
		if after[k] != b {
			t.Errorf("%s changed in a draft mission", k)
		}
	}
	// approving the plan is the person's act; only then does a reserve go through
	if _, err := f.ts.ApproveSession(context.Background(), "p", f.se.ID, opActor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatalf("reserve after approval: %v", err)
	}
}

// ---- policy: mode, ceilings ----

func TestModeAllOperatorOnly(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{Mode: sp(ModeAll)}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("a lead turned on mode all: %v", err)
	}
	if p, _ := f.st.GetPolicy("p", f.se.ID); p.Mode != ModeMarked {
		t.Fatalf("mode = %s after a refused change", p.Mode)
	}
	p := f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeAll)})
	if p.Mode != ModeAll {
		t.Fatalf("the operator could not set mode all: %+v", p)
	}
	ev, _ := f.st.Events("p", f.se.ID, 10)
	if len(ev) != 1 || ev[0].Kind != "mode" || ev[0].Who != "operator" {
		t.Fatalf("events = %+v", ev)
	}
	// a value that is not a mode is refused for everyone
	if _, err := f.st.SetPolicy(OperatorScope{}, "p", f.se.ID, PolicyPatch{Mode: sp("everything")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad mode: %v", err)
	}
}

func TestLeadCanLowerNotRaiseMode(t *testing.T) {
	f := newMC(t, true)
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeAll)})
	if p := f.setPolicy(t, f.scope(), PolicyPatch{Mode: sp(ModeMarked)}); p.Mode != ModeMarked {
		t.Fatalf("the lead could not lower all -> marked: %+v", p)
	}
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{Mode: sp(ModeAll)}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("the lead raised marked -> all: %v", err)
	}
	if p := f.setPolicy(t, f.scope(), PolicyPatch{Mode: sp(ModeOff)}); p.Mode != ModeOff {
		t.Fatalf("the lead could not lower marked -> off: %+v", p)
	}
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{Mode: sp(ModeMarked)}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("the lead raised off -> marked: %v", err)
	}
	// with the mode off nothing is marked or reserved
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a mark with mode off: %v", err)
	}
}

func TestRaiseCapsAtOperatorMaxChildren(t *testing.T) {
	f := newMC(t, true)
	// the lead session's max_children setting is the operator's: default 4
	if p := f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(4)}); p.MaxChildren != 4 || p.RaisedBy != "lead:"+f.lead.ConversationID {
		t.Fatalf("raise to the operator's limit: %+v", p)
	}
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{MaxChildren: ip(5)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("a raise above the operator's limit was accepted: %v", err)
	}
	s, _ := f.st.GetSettings("p", f.lead.ConversationID)
	s.MaxChildren = 3
	if err := f.st.SaveSettings("p", f.lead.ConversationID, s); err != nil { // the operator lowers its limit
		t.Fatal(err)
	}
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(1)})
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{MaxChildren: ip(4)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("a raise above the lowered limit was accepted: %v", err)
	}
	if p := f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(3)}); p.MaxChildren != 3 {
		t.Fatalf("raise to the lowered limit: %+v", p)
	}
	// the share has a hard maximum for everyone
	for _, sc := range []CallerScope{f.scope(), OperatorScope{}} {
		if _, err := f.st.SetPolicy(sc, "p", f.se.ID, PolicyPatch{SharePct: ip(ShareHardMax + 1)}); !errors.Is(err, ErrLimit) {
			t.Fatalf("share above ShareHardMax accepted for %T: %v", sc, err)
		}
	}
	if p := f.setPolicy(t, f.scope(), PolicyPatch{SharePct: ip(ShareHardMax)}); p.SharePct != ShareHardMax {
		t.Fatalf("share at the hard maximum: %+v", p)
	}
	ev, _ := f.st.Events("p", f.se.ID, 20)
	raises := 0
	for _, e := range ev {
		if e.Kind == "raise" && e.Who == "lead:"+f.lead.ConversationID {
			raises++
		}
	}
	if raises < 3 {
		t.Fatalf("raises were not audited with who: %+v", ev)
	}
}

func TestRaiseNeverTouchesSettings(t *testing.T) {
	f := newMC(t, true)
	const dump = `SELECT profile_id, conversation_id, settings, unattended, budget_usd, role, parent_conversation_id FROM coder_sessions`
	if err := f.st.SaveSettings("p", f.lead.ConversationID, DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	before := rowsOf(t, f.st, dump)
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(4), SharePct: ip(70), DefaultBudgetUSD: fp(2)})
	f.setPolicy(t, f.scope(), PolicyPatch{Mode: sp(ModeOff)})
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeAll), MaxChildren: ip(10)})
	f.mark(t, 0)
	if after := rowsOf(t, f.st, dump); after != before {
		t.Fatalf("the policy changed a session's settings or tree:\nbefore: %s\nafter:  %s", before, after)
	}
	if s, _ := f.st.GetSettings("p", f.lead.ConversationID); s.MaxChildren != DefaultSettings().MaxChildren {
		t.Fatalf("settings.max_children = %d", s.MaxChildren)
	}
}

func TestSetPolicyRefusesAClosedMissionForTheLead(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{MaxChildren: ip(3)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a finished mission accepted a ceiling: %v", err)
	}
}

// ---- reserve, link, end ----

func TestReserveIsIdempotentUnderRace(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	c, _, _ := f.st.Child("p", f.se.ID, f.ids[0])
	if wins != 1 || c.Attempts != 1 || c.State != ChildSpawning {
		t.Fatalf("wins=%d row=%+v: want one winner and one attempt", wins, c)
	}
	if n := rowsOf(t, f.st, `SELECT * FROM coder_mission_children`); len(n) == 0 {
		t.Fatal("no row")
	}
}

func TestReserveHoldsTheCeilingInTheStatement(t *testing.T) {
	f := newMC(t, true)
	f.addTasks(t, 4)
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(1)})
	for i := range f.ids {
		f.mark(t, i)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); !errors.Is(err, ErrLimit) {
		t.Fatalf("the second reserve passed a ceiling of 1: %v", err)
	}
	// the statement holds the ceiling on its own, whatever the room check saw
	if won, err := f.st.takeReservation("p", f.se.ID, f.ids[2], f.lead.ConversationID, 1, f.st.stamp()); err != nil || won {
		t.Fatalf("the statement passed a ceiling of 1 with one live: won=%v err=%v", won, err)
	}
	if won, err := f.st.takeReservation("p", f.se.ID, f.ids[2], f.lead.ConversationID, 2, f.st.stamp()); err != nil || !won {
		t.Fatalf("the statement refused a second slot under a ceiling of 2: won=%v err=%v", won, err)
	}
	// racing reservations of different tasks for the last slot: one wins
	f2 := newMC(t, true)
	f2.addTasks(t, 6)
	f2.setPolicy(t, f2.scope(), PolicyPatch{MaxChildren: ip(1)})
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range f2.ids {
		f2.mark(t, i)
	}
	for i := range f2.ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := f2.st.Reserve(f2.scope(), "p", f2.se.ID, f2.ids[i]); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if c, _ := f2.st.ChildCounts("p", f2.se.ID); wins != 1 || c.Live != 1 {
		t.Fatalf("wins=%d live=%d under a ceiling of 1", wins, c.Live)
	}
}

func TestReserveNeedsAMarkInMarkedMode(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unmarked task was reserved in marked mode: %v", err)
	}
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeAll)})
	c, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil || c.Origin != "rule" || c.State != ChildSpawning {
		t.Fatalf("mode all: %+v %v", c, err)
	}
}

func TestReserveStopsAfterTheAttemptLimit(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	for i := 1; i <= MaxChildAttempts; i++ {
		if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		child := spawn(t, f.st, f.lead)
		if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], child); err != nil {
			t.Fatalf("link %d: %v", i, err)
		}
		if ok, err := f.st.End("p", child, EndNoFinish); err != nil || !ok {
			t.Fatalf("end %d: %v %v", i, ok, err)
		}
		f.mark(t, 0) // the lead queues another try; attempts are kept
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrLimit) {
		t.Fatalf("a fourth attempt: %v", err)
	}
}

func TestLinkEndAndCounts(t *testing.T) {
	f := newMC(t, true)
	f.addTasks(t, 1)
	f.mark(t, 0)
	f.mark(t, 1)
	f.mark(t, 2)
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); err != nil {
		t.Fatal(err)
	}
	stranger, _ := master(t, f.st, "workspace")
	strangerChild := spawn(t, f.st, stranger)
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], strangerChild); !errors.Is(err, ErrNotYours) {
		t.Fatalf("linked another master's child: %v", err)
	}
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], f.lead.ConversationID); !errors.Is(err, ErrNotYours) {
		t.Fatalf("linked the lead to itself: %v", err)
	}
	a, b := spawn(t, f.st, f.lead), spawn(t, f.st, f.lead)
	if c, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], a); err != nil || c.State != ChildSpawned || c.ChildConversationID != a || c.SpawnedAt == "" {
		t.Fatalf("link: %+v %v", c, err)
	}
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[1], a); err == nil {
		t.Fatal("one conversation was linked to two tasks")
	}
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[2], b); !errors.Is(err, ErrInvalid) {
		t.Fatalf("linked a task that was not reserved: %v", err)
	}
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[1], b); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.st.ChildCounts("p", f.se.ID); c != (ChildCounts{Live: 2, Queued: 1}) {
		t.Fatalf("counts = %+v", c)
	}
	if ok, err := f.st.End("p", a, EndNoFinish); !ok || err != nil {
		t.Fatalf("end: %v %v", ok, err)
	}
	if ok, _ := f.st.End("p", a, EndDone); ok {
		t.Fatal("a second end changed the row")
	}
	if ok, _ := f.st.End("p", b, "because"); ok {
		t.Fatal("an unknown reason ended a row")
	}
	if ok, err := f.st.End("p", b, EndDone); !ok || err != nil {
		t.Fatalf("end b: %v %v", ok, err)
	}
	if c, _ := f.st.ChildCounts("p", f.se.ID); c != (ChildCounts{Queued: 1, Failed: 1}) {
		t.Fatalf("counts after = %+v", c)
	}
	ca, _, _ := f.st.Child("p", f.se.ID, f.ids[0])
	if ca.State != ChildFailed || ca.EndReason != EndNoFinish || ca.EndedAt == "" {
		t.Fatalf("row a = %+v", ca)
	}
	list, _ := f.st.ChildrenOfLead("p", f.lead.ConversationID)
	if len(list) != 3 {
		t.Fatalf("ChildrenOfLead = %d rows", len(list))
	}
}

func TestResetStaleSpawningOnlyResetsUnlinkedRows(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.st.ResetStaleSpawning(5 * time.Minute); n != 0 {
		t.Fatalf("a fresh reservation was reset")
	}
	f.st.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	if n, _ := f.st.ResetStaleSpawning(5 * time.Minute); n != 1 {
		t.Fatalf("a stale reservation was not reset")
	}
	if c, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); c.State != ChildMarked {
		t.Fatalf("row = %+v", c)
	}
}

// ---- room ----

func TestRoomForChildrenCeiling(t *testing.T) {
	f := newMC(t, true)
	f.addTasks(t, 2)
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(2)})
	for i := 0; i < 2; i++ {
		f.mark(t, i)
		if r, err := f.st.RoomFor("p", f.se.ID, 0); err != nil || !r.OK {
			t.Fatalf("room with %d live: %+v %v", i, r, err)
		}
		if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := f.st.RoomFor("p", f.se.ID, 0)
	if r.OK || r.Live != 2 || r.Ceiling != 2 {
		t.Fatalf("room at the ceiling: %+v", r)
	}
	// the operator's setting caps the policy
	s, _ := f.st.GetSettings("p", f.lead.ConversationID)
	s.MaxChildren = 1
	if err := f.st.SaveSettings("p", f.lead.ConversationID, s); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.st.RoomFor("p", f.se.ID, 0); r.Ceiling != 1 {
		t.Fatalf("ceiling = %d, want min(policy 2, setting 1)", r.Ceiling)
	}
}

func TestRoomForMaxConcurrent(t *testing.T) {
	f := newMC(t, true)
	a, b := spawn(t, f.st, f.lead), spawn(t, f.st, f.lead)
	run := func(id string) {
		if err := f.st.SaveSession(Session{ProfileID: "p", ConversationID: id, State: StateRunning, TurnID: "t", TurnPID: 0}); err != nil {
			t.Fatal(err)
		}
	}
	run(a)
	if r, _ := f.st.RoomFor("p", f.se.ID, 0); !r.OK || r.Running != 1 {
		t.Fatalf("one running of two: %+v", r)
	}
	run(b)
	r, _ := f.st.RoomFor("p", f.se.ID, 0)
	if r.OK || r.Running != 2 || r.MaxConcurrent != 2 {
		t.Fatalf("two running of two: %+v", r)
	}
}

func TestRoomForShareOfTheLeadsRemainingBudget(t *testing.T) {
	f := newMC(t, true)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	room := func(usd float64) bool {
		r, err := f.st.RoomFor("p", f.se.ID, usd)
		if err != nil {
			t.Fatal(err)
		}
		return r.OK
	}
	if !room(5) || room(5.01) || room(0) {
		t.Fatalf("share 50%% of $10: 5=%v 5.01=%v 0=%v", room(5), room(5.01), room(0))
	}
	if _, err := f.st.Spawn(f.lead, SpawnRequest{BudgetUSD: 3}); err != nil {
		t.Fatal(err)
	}
	if !room(2) || room(2.5) {
		t.Fatalf("with $3 carved: 2=%v 2.5=%v", room(2), room(2.5))
	}
	// what the lead spent shrinks the base: a started turn with no usage report counts as its whole cap
	if err := f.st.RecordTurnCap("p", f.lead.ConversationID, "t1", 4); err != nil {
		t.Fatal(err)
	}
	f.st.MarkTurnCapStarted("p", f.lead.ConversationID, "t1")
	if room(2) { // 50% of (10 - 4) = 3, carved 3
		t.Fatal("the lead's spend did not shrink the share")
	}
	f.setPolicy(t, f.scope(), PolicyPatch{SharePct: ip(80)}) // 80% of 6 = 4.8; 3 carved
	if !room(1.8) || room(1.9) {
		t.Fatalf("share 80%%: 1.8=%v 1.9=%v", room(1.8), room(1.9))
	}
}

func TestRoomForALeadWithNoCapHasNoShare(t *testing.T) {
	f := newMC(t, true)
	for _, usd := range []float64{0, 1, 1000} {
		if r, err := f.st.RoomFor("p", f.se.ID, usd); err != nil || !r.OK {
			t.Fatalf("a lead with no cap, carve %v: %+v %v", usd, r, err)
		}
	}
	f.setPolicy(t, f.scope(), PolicyPatch{SharePct: ip(1)})
	if r, _ := f.st.RoomFor("p", f.se.ID, 1000); !r.OK {
		t.Fatalf("share applied to a lead with no cap: %+v", r)
	}
	// the count ceiling still holds without a cap
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(0)})
	if r, _ := f.st.RoomFor("p", f.se.ID, 0); r.OK {
		t.Fatal("max_children 0 left room")
	}
}

func TestDefaultChildBudgetDerivesFromTheShare(t *testing.T) {
	f := newMC(t, true)
	if v, _ := f.st.DefaultChildBudget("p", f.se.ID); v != 0 {
		t.Fatalf("a lead with no cap: default carve %v", v)
	}
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.st.DefaultChildBudget("p", f.se.ID); v != 2.5 { // 10 x 50% / 2
		t.Fatalf("default carve = %v, want 2.5", v)
	}
	if err := f.st.SetBudget("p", f.lead.ConversationID, 1); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.st.DefaultChildBudget("p", f.se.ID); v != 0.5 { // the floor
		t.Fatalf("default carve = %v, want the 0.50 floor", v)
	}
	f.setPolicy(t, f.scope(), PolicyPatch{DefaultBudgetUSD: fp(0.75)})
	if v, _ := f.st.DefaultChildBudget("p", f.se.ID); v != 0.75 {
		t.Fatalf("default carve = %v, want the policy's 0.75", v)
	}
}

// ---- carve return ----

// linkedChild makes a child of the lead with a carve, linked to task 0 of the mission.
func (f *mcFix) linkedChild(t *testing.T, carve float64) string {
	t.Helper()
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: carve}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil { // the room check comes before the carve
		t.Fatal(err)
	}
	out, err := f.st.Spawn(f.lead, SpawnRequest{BudgetUSD: carve})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], out.ConversationID); err != nil {
		t.Fatal(err)
	}
	return out.ConversationID
}

func (f *mcFix) spend(t *testing.T, conv string, usd float64) {
	t.Helper()
	if err := f.st.RecordTurnCap("p", conv, "t1", usd); err != nil {
		t.Fatal(err)
	}
	f.st.MarkTurnCapStarted("p", conv, "t1") // no usage report: the whole cap counts as spent
}

func TestReleaseCarveNeverRaisesNeverZero(t *testing.T) {
	budgetOf := func(f *mcFix, conv string) float64 { b, _ := f.st.Budget("p", conv); return b }
	t.Run("returns the unspent part to the lead", func(t *testing.T) {
		f := newMC(t, true)
		if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
			t.Fatal(err)
		}
		child := f.linkedChild(t, 4)
		f.spend(t, child, 1.5)
		if ok, err := f.st.End("p", child, EndDone); !ok || err != nil {
			t.Fatal(ok, err)
		}
		if got := budgetOf(f, child); got != 1.5 {
			t.Fatalf("child cap = %v, want what it spent (1.5)", got)
		}
		if c, _ := f.st.Carved("p", f.lead.ConversationID); c != 1.5 {
			t.Fatalf("carved = %v, want 1.5: the lead got $2.50 back", c)
		}
	})
	t.Run("never raises a cap", func(t *testing.T) {
		f := newMC(t, true)
		child := f.linkedChild(t, 1)
		f.spend(t, child, 3) // spent more than its cap
		if err := f.st.releaseCarve("p", child); err != nil {
			t.Fatal(err)
		}
		if got := budgetOf(f, child); got != 1 {
			t.Fatalf("child cap = %v after spending past it, want it left at 1", got)
		}
	})
	t.Run("never sets zero", func(t *testing.T) {
		f := newMC(t, true)
		child := f.linkedChild(t, 4)
		if err := f.st.releaseCarve("p", child); err != nil { // spent nothing
			t.Fatal(err)
		}
		if got := budgetOf(f, child); got <= 0 || got > minCap*2 {
			t.Fatalf("child cap = %v for a child that spent nothing, want the floor %v (0 would mean uncapped)", got, minCap)
		}
	})
	t.Run("leaves an uncapped child alone", func(t *testing.T) {
		f := newMC(t, true)
		child := f.linkedChild(t, 0)
		f.spend(t, child, 2)
		if err := f.st.releaseCarve("p", child); err != nil {
			t.Fatal(err)
		}
		if got := budgetOf(f, child); got != 0 {
			t.Fatalf("an uncapped child got a cap of %v", got)
		}
	})
	t.Run("touches no session that is not a mission child of its lead", func(t *testing.T) {
		f := newMC(t, true)
		if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
			t.Fatal(err)
		}
		plain, err := f.st.Spawn(f.lead, SpawnRequest{BudgetUSD: 4})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.releaseCarve("p", plain.ConversationID); err != nil {
			t.Fatal(err)
		}
		if got := budgetOf(f, plain.ConversationID); got != 4 {
			t.Fatalf("a plain child's cap = %v, want 4", got)
		}
	})
	t.Run("twice changes nothing more", func(t *testing.T) {
		f := newMC(t, true)
		child := f.linkedChild(t, 4)
		f.spend(t, child, 1)
		_ = f.st.releaseCarve("p", child)
		first := budgetOf(f, child)
		_ = f.st.releaseCarve("p", child)
		if budgetOf(f, child) != first {
			t.Fatal("the second release changed the cap")
		}
	})
}

func TestDeletedEndDoesNotReleaseButMarksGone(t *testing.T) {
	f := newMC(t, true)
	child := f.linkedChild(t, 2)
	if ok, err := f.st.MarkGone("p", child); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if c, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); c.State != ChildGone || c.EndReason != EndDeleted {
		t.Fatalf("row = %+v", c)
	}
}

// ---- display ----

func TestChildDisplayBubbleOrChart(t *testing.T) {
	cases := []struct {
		size           string
		carve          float64
		failed, asking bool
		want           string
	}{
		{"large", 0.5, false, false, "bubble"},
		{"small", 9, false, false, "chart"}, // an explicit size wins over the carve
		{"", LargeChildBudgetUSD, false, false, "bubble"},
		{"", LargeChildBudgetUSD - 0.01, false, false, "chart"},
		{"small", 0.5, true, false, "bubble"}, // failed
		{"small", 0.5, false, true, "bubble"}, // an open question
		{"", 0, false, false, "chart"},
	}
	for _, c := range cases {
		if got := ChildDisplay(c.size, c.carve, c.failed, c.asking); got != c.want {
			t.Errorf("%+v = %s", c, got)
		}
	}
	f := newMC(t, true)
	child := f.linkedChild(t, 1)
	row, _, _ := f.st.Child("p", f.se.ID, f.ids[0])
	if d, _ := f.st.DisplayOf(row); d != "chart" {
		t.Fatalf("display = %s", d)
	}
	if _, err := f.st.Park("p", child, "risky_action", "which one?", []string{"a", "b"}, "", false); err != nil {
		t.Fatalf("park: %v", err)
	}
	if d, _ := f.st.DisplayOf(row); d != "bubble" {
		t.Fatalf("a child with an open question displays as %s", d)
	}
}

// ---- deletion ----

func TestConversationDeleteRemovesTheLeadsRowsAndMarksTheChildGone(t *testing.T) {
	f := newMC(t, true)
	child := f.linkedChild(t, 0)
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(3)})
	a, _ := ai.NewAIStore(f.st.db)
	// deleting the child's conversation: its task goes to gone, the lead keeps its row
	if err := a.DeleteConversation(child, "p"); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); c.State != ChildGone || c.EndReason != EndDeleted {
		t.Fatalf("row after the child's deletion = %+v", c)
	}
	if n := rowsOf(t, f.st, `SELECT * FROM coder_mission_policy`); n == "" {
		t.Fatal("the child's deletion removed the lead's policy")
	}
	// deleting the lead's conversation removes policy, rows and audit trail
	if err := a.DeleteConversation(f.lead.ConversationID, "p"); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"coder_mission_policy", "coder_mission_children", "coder_mission_events"} {
		if n := rowsOf(t, f.st, `SELECT * FROM `+tbl); n != "" {
			t.Errorf("%s survived the lead's deletion: %s", tbl, n)
		}
	}
}

func TestConversationDeleteKeepsOtherLeadsRows(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	other, _ := master(t, f.st, "workspace")
	se2, _ := missionFor(t, f.st, other.ConversationID, true)
	ids2 := taskIDs(t, f.st, se2.ID)
	if _, err := f.st.Mark(LeadScope{Caller: other}, "p", se2.ID, ids2[0], MarkOptions{}); err != nil {
		t.Fatal(err)
	}
	a, _ := ai.NewAIStore(f.st.db)
	if err := a.DeleteConversation(other.ConversationID, "p"); err != nil {
		t.Fatal(err)
	}
	if c, ok, _ := f.st.Child("p", f.se.ID, f.ids[0]); !ok || c.State != ChildMarked {
		t.Fatalf("another lead's row was removed: %+v", c)
	}
	if ev, _ := f.st.Events("p", f.se.ID, 10); len(ev) != 1 {
		t.Fatalf("another lead's audit rows were removed: %+v", ev)
	}
	if ev, _ := f.st.Events("p", se2.ID, 10); len(ev) != 0 {
		t.Fatalf("the deleted lead's audit rows survived: %+v", ev)
	}
}

func TestSweepOrphansRemovesAMissionWhoseLeadIsGone(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	ex := func(q string, args ...any) {
		t.Helper()
		if _, err := f.st.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// rows of a lead whose conversation no longer exists (what a late host write can leave)
	ex(`INSERT INTO coder_mission_policy (profile_id, session_id, lead_conversation_id, updated_at) VALUES ('p', 900, 'ghost-lead', 'n')`)
	ex(`INSERT INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, origin, state, created_at, updated_at) VALUES ('p', 900, 1, 'ghost-lead', 'lead', 'marked', 'n', 'n')`)
	ex(`INSERT INTO coder_mission_events (profile_id, session_id, task_id, kind, who, at) VALUES ('p', 900, 1, 'mark', 'x', 'n')`)
	// a live row whose child conversation is gone goes to gone, not away
	ex(`INSERT INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, child_conversation_id, origin, state, created_at, updated_at) VALUES ('p', ?, 777, ?, 'ghost-child', 'lead', 'spawned', 'n', 'n')`, f.se.ID, f.lead.ConversationID)
	// a reserved row with no conversation yet is not an orphan
	ex(`INSERT INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, origin, state, created_at, updated_at) VALUES ('p', ?, 778, ?, 'lead', 'spawning', 'n', 'n')`, f.se.ID, f.lead.ConversationID)

	if _, err := f.st.SweepOrphans(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT * FROM coder_mission_policy WHERE session_id = 900`,
		`SELECT * FROM coder_mission_children WHERE session_id = 900`,
		`SELECT * FROM coder_mission_events WHERE session_id = 900`,
	} {
		if n := rowsOf(t, f.st, q); n != "" {
			t.Errorf("%s: %s survived", q, n)
		}
	}
	if st := rowsOf(t, f.st, `SELECT state FROM coder_mission_children WHERE task_id = 777`); st != "gone" {
		t.Errorf("a live row of a deleted child = %q, want gone", st)
	}
	if st := rowsOf(t, f.st, `SELECT state FROM coder_mission_children WHERE task_id = 778`); st != "spawning" {
		t.Errorf("a reserved row = %q, want spawning", st)
	}
	if c, ok, _ := f.st.Child("p", f.se.ID, f.ids[0]); !ok || c.State != ChildMarked {
		t.Fatalf("the live mission's row was swept: %+v", c)
	}
	if ev, _ := f.st.Events("p", f.se.ID, 10); len(ev) != 1 {
		t.Fatalf("the live mission's audit rows were swept: %+v", ev)
	}
}

func TestEventsKeepOnlyTheNewest(t *testing.T) {
	f := newMC(t, true)
	for i := 0; i < MaxMissionEvents+25; i++ {
		if err := addMissionEvent(f.st.db, "p", f.se.ID, 0, "mark", "x", fmt.Sprint(i), "n"); err != nil {
			t.Fatal(err)
		}
	}
	ev, _ := f.st.Events("p", f.se.ID, 0)
	if len(ev) != MaxMissionEvents || ev[0].Note != fmt.Sprint(MaxMissionEvents+24) {
		t.Fatalf("%d events, newest %q", len(ev), ev[0].Note)
	}
}

// ---- nothing but the mission tables moves ----

func TestMissionStoreCallsMoveOnlyTheirOwnTables(t *testing.T) {
	f := newMC(t, true)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	child, err := f.st.Spawn(f.lead, SpawnRequest{BudgetUSD: 2})
	if err != nil {
		t.Fatal(err)
	}
	before := protectedState(t, f.st.db)
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(3), SharePct: ip(60)})
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeAll)})
	f.mark(t, 0)
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], child.ConversationID); err != nil {
		t.Fatal(err)
	}
	f.st.RoomFor("p", f.se.ID, 1)
	f.st.RecordTurnCap("p", child.ConversationID, "t", 0.5)
	f.st.MarkTurnCapStarted("p", child.ConversationID, "t")
	if _, err := f.st.End("p", child.ConversationID, EndDone); err != nil {
		t.Fatal(err)
	}
	after := protectedState(t, f.st.db)
	for k, b := range before {
		switch k {
		case "coder_mission_policy", "coder_mission_children", "coder_mission_events":
			if after[k] == b {
				t.Errorf("%s did not change: the test exercised nothing", k)
			}
		case "coder_sessions(protected)":
			// only the child's cap went down
			if after[k] == b {
				t.Errorf("the carve was not released")
			}
		default:
			if after[k] != b {
				t.Errorf("%s changed:\nbefore: %.200s\nafter:  %.200s", k, b, after[k])
			}
		}
	}
	if n := rowsOf(t, f.st, `SELECT * FROM coder_session_commands`); n != "" {
		t.Errorf("the store queued a command: %s", n)
	}
}
