package missionpursuit

import (
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

func TestProposeByAgentIsProposedNeverConfirmed(t *testing.T) {
	f := newMission(t)
	for _, by := range []tasks.Actor{lead, {Kind: tasks.Agent, Name: "mission-lead", MissionPlanner: true, Session: f.session}, human} {
		g := mustPropose(t, f, goodCriteria(), by)
		if g.State != StateProposed || g.ConfirmedBy != "" || g.ConfirmedAt != "" {
			t.Errorf("%s: a proposal came back %+v", by.Label(), g)
		}
		if g.ProposedBy != by.Label() || len(g.Criteria) != 2 || g.Criteria[0].ID != "c1" || g.Criteria[1].ID != "c2" {
			t.Errorf("%s: proposal %+v", by.Label(), g)
		}
		if g.Hash != Hash(goodCriteria()) {
			t.Errorf("hash %s, want %s", g.Hash, Hash(goodCriteria()))
		}
	}
	if st := count(t, f.db, `SELECT COUNT(*) FROM mission_goals WHERE state <> 'proposed'`); st != 0 {
		t.Errorf("%d goal rows are past proposed", st)
	}
}

func TestProposeRefusesWhoIsNotTheMissionsAgent(t *testing.T) {
	f := newMission(t)
	// a second mission, in a chat of its own, with a proposal the first mission's agents must not touch
	mustExec(t, f.db, `INSERT INTO coder_sessions (profile_id, conversation_id, chat_mode, created_at, updated_at) VALUES ('default', 'chat2', 'mission', 'x', 'x')`)
	other := addSession(t, f.db, "default", tasks.SessionKindMission, "chat2", "running")
	theirs, err := f.store.Propose(bg, "default", other, otherCriteria(), tasks.Actor{Kind: tasks.Agent, Name: "lead2", Conversation: "chat2"})
	if err != nil {
		t.Fatal(err)
	}
	for name, by := range map[string]tasks.Actor{
		"an unbound agent":                 agent("anyone"),
		"an agent of another session":      {Kind: tasks.Agent, Name: "w", Session: f.session + 99},
		"an agent of another chat":         {Kind: tasks.Agent, Name: "l", Conversation: "chat9"},
		"a worker bound elsewhere in chat": {Kind: tasks.Agent, Name: "w", Session: other, Conversation: "chat1"},
		"a capture":                        {Kind: tasks.Capture, Name: "chrome"},
		"the zero actor":                   {},
	} {
		if _, err := f.store.Propose(bg, "default", f.session, goodCriteria(), by); !errors.Is(err, tasks.ErrOperatorOnly) {
			t.Errorf("%s, session of its own mission: %v", name, err)
		}
	}
	// the lead of the first chat cannot reach the second mission
	if _, err := f.store.Propose(bg, "default", other, goodCriteria(), lead); !errors.Is(err, tasks.ErrOperatorOnly) {
		t.Errorf("the lead of chat1 proposing to chat2's mission: %v", err)
	}
	if got, _ := f.store.Get(bg, "default", other); got.Hash != theirs.Hash || got.ProposedBy != "lead2" {
		t.Errorf("another mission's proposal was overwritten: %+v", got)
	}
	if count(t, f.db, `SELECT COUNT(*) FROM mission_goals WHERE session_id = ?`, f.session) != 0 {
		t.Error("a refused proposal left a row")
	}
	// the right lead is accepted, with its chat or bound to the session
	if _, err := f.store.Propose(bg, "default", f.session, goodCriteria(), lead); err != nil {
		t.Errorf("the lead of the mission: %v", err)
	}
	if _, err := f.store.Propose(bg, "default", f.session, goodCriteria(), tasks.Actor{Kind: tasks.Agent, Name: "w", Session: f.session}); err != nil {
		t.Errorf("an agent bound to the session: %v", err)
	}
}

func TestAgentCannotSetConfirmOrLock(t *testing.T) {
	f := newMission(t)
	g := mustPropose(t, f, goodCriteria(), lead)
	for _, by := range []tasks.Actor{lead, {Kind: tasks.Agent, Name: "mission-lead", MissionPlanner: true, Session: f.session}, {Kind: tasks.Capture, Name: "os"}, {}} {
		_, err := f.store.Set(bg, "default", f.session, otherCriteria(), by)
		wantIs(t, err, tasks.ErrOperatorOnly, "set by "+by.Label())
		_, err = f.store.Confirm(bg, "default", f.session, g.Hash, by)
		wantIs(t, err, tasks.ErrOperatorOnly, "confirm by "+by.Label())
		_, err = f.store.Lock(bg, "default", f.session, by)
		wantIs(t, err, tasks.ErrOperatorOnly, "lock by "+by.Label())
	}
	got, err := f.store.Get(bg, "default", f.session)
	if err != nil || got.State != StateProposed || got.Hash != g.Hash {
		t.Errorf("after the refusals: %+v %v", got, err)
	}
}

func TestConfirmNeedsTheHashThePersonSaw(t *testing.T) {
	f := newMission(t)
	seen := mustPropose(t, f, goodCriteria(), lead)
	// the lead revises the proposal after the person read it: no expiry, revisable until confirmed
	revised := mustPropose(t, f, otherCriteria(), lead)
	if revised.Hash == seen.Hash || len(revised.Criteria) != 2 || revised.Criteria[0].Kind != "file" {
		t.Fatalf("revision %+v", revised)
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_goal_criteria`); n != 2 {
		t.Errorf("the revision left %d criteria rows, want 2", n)
	}
	_, err := f.store.Confirm(bg, "default", f.session, seen.Hash, human)
	wantIs(t, err, ErrStale, "confirming the hash that was seen, after a revision")
	if g, _ := f.store.Get(bg, "default", f.session); g.State != StateProposed {
		t.Errorf("a stale confirm moved the goal to %s", g.State)
	}
	for _, bad := range []string{"", "abc", strings.ToUpper(revised.Hash[:10])} {
		if _, err := f.store.Confirm(bg, "default", f.session, bad, human); err == nil {
			t.Errorf("confirm with %q succeeded", bad)
		}
	}
	g, err := f.store.Confirm(bg, "default", f.session, revised.Hash, human)
	if err != nil || g.State != StateConfirmed || g.ConfirmedBy != "you" || g.ConfirmedAt == "" {
		t.Fatalf("confirm: %+v %v", g, err)
	}
	_, err = f.store.Confirm(bg, "default", f.session, revised.Hash, human)
	wantIs(t, err, ErrGoalLocked, "confirming twice")
	_, err = f.store.Propose(bg, "default", f.session, goodCriteria(), lead)
	wantIs(t, err, ErrGoalLocked, "a proposal after confirmation: a changed goal means a new mission")
}

func TestConfirmWithoutAProposalIsRefused(t *testing.T) {
	f := newMission(t)
	_, err := f.store.Confirm(bg, "default", f.session, Hash(goodCriteria()), human)
	wantIs(t, err, tasks.ErrNotFound, "confirm with no proposal")
	_, err = f.store.Get(bg, "default", f.session)
	wantIs(t, err, tasks.ErrNotFound, "get with no goal")
}

func TestSetWritesAndConfirmsAsThePerson(t *testing.T) {
	f := newMission(t)
	mustPropose(t, f, goodCriteria(), lead)
	g, err := f.store.Set(bg, "default", f.session, otherCriteria(), human)
	if err != nil || g.State != StateConfirmed || g.ConfirmedBy != "you" || g.Hash != Hash(otherCriteria()) {
		t.Fatalf("set over a proposal: %+v %v", g, err)
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_goal_criteria WHERE session_id = ?`, f.session); n != 2 {
		t.Errorf("criteria rows = %d", n)
	}
	_, err = f.store.Set(bg, "default", f.session, goodCriteria(), human)
	wantIs(t, err, ErrGoalLocked, "set over a confirmed goal")
	g2, _ := f.store.Get(bg, "default", f.session)
	if g2.Hash != g.Hash {
		t.Error("a refused set changed the goal")
	}
}

func TestSetOnAFreshMissionAndRubricRule(t *testing.T) {
	f := newMission(t)
	_, err := f.store.Set(bg, "default", f.session, []Criterion{{Kind: "command", Spec: "true"}}, human)
	wantIs(t, err, tasks.ErrInvalid, "a goal with no rubric")
	if count(t, f.db, `SELECT COUNT(*) FROM mission_goals`) != 0 {
		t.Error("a refused set left a row")
	}
	if g, err := f.store.Set(bg, "default", f.session, goodCriteria(), human); err != nil || g.State != StateConfirmed {
		t.Fatalf("set: %+v %v", g, err)
	}
}

func TestLockedIsTerminal(t *testing.T) {
	f := newMission(t)
	g := mustPropose(t, f, goodCriteria(), lead)
	_, err := f.store.Lock(bg, "default", f.session, human)
	if err == nil || strings.Contains(err.Error(), "final") || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("locking a goal that is only proposed must say it is not confirmed: %v", err)
	}
	wantIs(t, err, tasks.ErrInvalid, "locking a goal that is only proposed")
	if _, err := f.store.Confirm(bg, "default", f.session, g.Hash, human); err != nil {
		t.Fatal(err)
	}
	l, err := f.store.Lock(bg, "default", f.session, human)
	if err != nil || l.State != StateLocked || l.LockedAt == "" || l.Hash != g.Hash {
		t.Fatalf("lock: %+v %v", l, err)
	}
	if _, err := f.store.Lock(bg, "default", f.session, human); err == nil {
		t.Error("locking twice succeeded")
	}
	if _, err := f.store.Confirm(bg, "default", f.session, g.Hash, human); err == nil {
		t.Error("confirming a locked goal succeeded")
	}
	if _, err := f.store.Set(bg, "default", f.session, otherCriteria(), human); err == nil {
		t.Error("setting a locked goal succeeded")
	}
	if _, err := f.store.Propose(bg, "default", f.session, otherCriteria(), lead); err == nil {
		t.Error("proposing over a locked goal succeeded")
	}
	if got, _ := f.store.Get(bg, "default", f.session); got.State != StateLocked || got.Hash != g.Hash {
		t.Errorf("the locked goal moved: %+v", got)
	}
}

func TestRefusesWhenChatModeNotMission(t *testing.T) {
	for _, mode := range []string{"off", "open", "no row"} {
		t.Run("mode "+mode, func(t *testing.T) {
			var f fixture
			if mode == "no row" { // no coder_sessions row at all
				f = newFixture(t, "", tasks.SessionKindMission)
			} else {
				f = newFixture(t, mode, tasks.SessionKindMission)
			}
			if _, err := f.store.Propose(bg, "default", f.session, goodCriteria(), lead); err == nil || !strings.Contains(err.Error(), "mission") {
				t.Errorf("propose: %v", err)
			}
			if _, err := f.store.Set(bg, "default", f.session, goodCriteria(), human); err == nil {
				t.Error("set succeeded")
			}
			if count(t, f.db, `SELECT COUNT(*) FROM mission_goals`) != 0 {
				t.Error("a refused write left a row")
			}
		})
	}
	t.Run("an unknown mode word fails closed", func(t *testing.T) {
		f := newMission(t)
		if _, err := f.store.WithModes(fixedModes("")).Propose(bg, "default", f.session, goodCriteria(), lead); err == nil {
			t.Error("propose succeeded with an empty mode")
		}
	})
	t.Run("reader error fails closed", func(t *testing.T) {
		f := newMission(t)
		bad := f.store.WithModes(failModes{})
		if _, err := bad.Propose(bg, "default", f.session, goodCriteria(), lead); err == nil {
			t.Error("propose succeeded with an unreadable mode")
		}
		if _, err := bad.Set(bg, "default", f.session, goodCriteria(), human); err == nil {
			t.Error("set succeeded with an unreadable mode")
		}
	})
	t.Run("an injected reader decides", func(t *testing.T) {
		f := newFixture(t, "off", tasks.SessionKindMission)
		if _, err := f.store.WithModes(fixedModes("mission")).Propose(bg, "default", f.session, goodCriteria(), lead); err != nil {
			t.Errorf("a reader that says mission: %v", err)
		}
	})
	t.Run("confirm and lock read the mode too", func(t *testing.T) {
		f := newMission(t)
		g := mustPropose(t, f, goodCriteria(), lead)
		if _, err := f.store.WithModes(fixedModes("off")).Confirm(bg, "default", f.session, g.Hash, human); err == nil {
			t.Error("confirm succeeded in a chat that is not a mission")
		}
	})
}

func TestRefusesOnNonMissionSessionKind(t *testing.T) {
	for _, kind := range []string{tasks.SessionKindBoard, tasks.SessionKindDirection} {
		f := newFixture(t, "mission", kind)
		if _, err := f.store.Propose(bg, "default", f.session, goodCriteria(), lead); err == nil {
			t.Errorf("kind %s: propose succeeded", kind)
		}
	}
	f := newMission(t)
	if _, err := f.store.Propose(bg, "default", f.session+50, goodCriteria(), lead); err == nil {
		t.Error("an unknown session accepted a proposal")
	}
	mustExec(t, f.db, `INSERT INTO profiles (id, name) VALUES ('other', 'other')`)
	if _, err := f.store.Propose(bg, "other", f.session, goodCriteria(), lead); err == nil {
		t.Error("another profile's call reached the session")
	}
	mustExec(t, f.db, `UPDATE task_sessions SET state = 'stopped' WHERE id = ?`, f.session)
	if _, err := f.store.Propose(bg, "default", f.session, goodCriteria(), lead); err == nil {
		t.Error("a stopped session accepted a proposal")
	}
	mustExec(t, f.db, `UPDATE task_sessions SET state = 'running', conversation_id = NULL WHERE id = ?`, f.session)
	if _, err := f.store.Propose(bg, "default", f.session, goodCriteria(), lead); err == nil {
		t.Error("a session with no chat accepted a proposal")
	}
}

func TestSessionDeleteCascadesEvenWhenLocked(t *testing.T) {
	f := newMission(t)
	g := mustPropose(t, f, goodCriteria(), lead)
	if _, err := f.store.Confirm(bg, "default", f.session, g.Hash, human); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Lock(bg, "default", f.session, human); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `DELETE FROM task_sessions WHERE id = ?`, f.session)
	if n := count(t, f.db, `SELECT (SELECT COUNT(*) FROM mission_goals) + (SELECT COUNT(*) FROM mission_goal_criteria)`); n != 0 {
		t.Errorf("%d goal rows survive the deletion of their session", n)
	}
}

func TestHashDependsOnKindSpecExpectedAndOrder(t *testing.T) {
	a := goodCriteria()
	b := []Criterion{a[1], a[0]}
	c := goodCriteria()
	c[0].Expected = "exit:0,1"
	seen := map[string]bool{}
	for _, cs := range [][]Criterion{a, b, c, otherCriteria()} {
		h := Hash(cs)
		if len(h) != 64 || seen[h] {
			t.Errorf("hash %q repeats or is not 64 hex", h)
		}
		seen[h] = true
	}
	if Hash(a) != Hash(goodCriteria()) {
		t.Error("the hash is not stable")
	}
	// a field boundary moved is a different criterion
	x := []Criterion{{Kind: "rubric", Spec: "ab", Expected: "c"}}
	y := []Criterion{{Kind: "rubric", Spec: "a", Expected: "bc"}}
	if Hash(x) == Hash(y) {
		t.Error("moving a field boundary kept the hash")
	}
}

// A proposal whose rows no longer match its hash is not readable, but it can be revised by proposing again; a confirmed goal
// in that state is refused for good.
func TestDamagedProposalCanBeReplacedByProposingAgain(t *testing.T) {
	f := newMission(t)
	g := mustPropose(t, f, goodCriteria(), lead)
	mustExec(t, f.db, `UPDATE mission_goal_criteria SET spec = 'tampered' WHERE session_id = ? AND pos = 1`, f.session)
	_, err := f.store.Get(bg, "default", f.session)
	if err == nil || !strings.Contains(err.Error(), "propose the criteria again") {
		t.Fatalf("a damaged proposal: %v", err)
	}
	if _, err := f.store.Confirm(bg, "default", f.session, g.Hash, human); err == nil {
		t.Error("a damaged proposal was confirmed")
	}
	fixed, err := f.store.Propose(bg, "default", f.session, otherCriteria(), lead)
	if err != nil || fixed.Hash != Hash(otherCriteria()) {
		t.Fatalf("proposing again: %+v %v", fixed, err)
	}
	if _, err := f.store.Confirm(bg, "default", f.session, fixed.Hash, human); err != nil {
		t.Errorf("confirming the replacement: %v", err)
	}
}
