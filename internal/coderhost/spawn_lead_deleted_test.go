package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
)

// danglingChildren lists the child sessions whose parent conversation or master row is gone.
func danglingChildren(t *testing.T, st *Store) []string {
	t.Helper()
	rows, err := st.db.Query(`SELECT k.conversation_id FROM coder_sessions k WHERE k.role = 'child' AND (
		NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = k.parent_conversation_id AND c.profile_id = k.profile_id)
		OR NOT EXISTS (SELECT 1 FROM coder_sessions m WHERE m.conversation_id = k.parent_conversation_id AND m.profile_id = k.profile_id))`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

func conversationCount(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM ai_chat_conversations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestSpawn_LeadDeletedMidSpawn deletes the lead conversation (through the guarded delete the app uses) at
// every point of the spawn that reads the clock, one run per point: whichever step it lands between, the
// lead's delete either comes fully before the child (the spawn is refused and leaves nothing) or fully after
// it (the child is demoted to a solo session). Never a child of a parent that is gone.
func TestSpawn_LeadDeletedMidSpawn(t *testing.T) {
	fired := 0
	for k := 1; k <= 40; k++ {
		st := newStoreT(t)
		m, _ := master(t, st, "workspace")
		a, _ := ai.NewAIStore(st.db)
		base := conversationCount(t, st) // the lead
		calls, done := 0, false
		realNow := st.now
		st.now = func() time.Time {
			calls++
			if calls == k && !done {
				done = true
				err := a.DeleteConversationGuarded(m.ConversationID, "p", func(ctx context.Context, conn *sql.Conn) error { return nil })
				if err != nil {
					t.Fatalf("k=%d: deleting the lead: %v", k, err)
				}
			}
			return realNow()
		}
		out, err := st.Spawn(m, SpawnRequest{Message: "go"})
		if !done {
			break // the spawn reads the clock fewer than k times: every point has been tried
		}
		fired++
		label := fmt.Sprintf("clock read %d", k)
		if d := danglingChildren(t, st); len(d) > 0 {
			t.Fatalf("%s: child session(s) %v have a parent that is gone (spawn err: %v)", label, d, err)
		}
		if err != nil {
			// refused: no child conversation may be left behind
			if n := conversationCount(t, st); n != base-1 {
				t.Fatalf("%s: spawn refused (%v) but %d conversation(s) remain, want %d", label, err, n, base-1)
			}
			continue
		}
		// the child was made first, then the lead went: the child is a solo session now
		x, ok, _ := st.GetSession("p", out.ConversationID)
		if !ok || x.Role != "solo" || x.Parent != "" {
			t.Fatalf("%s: child after the lead's delete = %+v (found %v), want a solo session", label, x, ok)
		}
	}
	if fired == 0 {
		t.Fatal("the seam never fired")
	}
	t.Logf("lead deleted at %d points of the spawn", fired)
}

func TestSpawn_LeadDeletedAfterTheChildExistsDemotesIt(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "workspace")
	kid := spawn(t, st, m)
	a, _ := ai.NewAIStore(st.db)
	if err := a.DeleteConversation(m.ConversationID, "p"); err != nil {
		t.Fatal(err)
	}
	x, ok, _ := st.GetSession("p", kid)
	if !ok || x.Role != "solo" || x.Parent != "" {
		t.Fatalf("child = %+v (found %v), want solo", x, ok)
	}
	if d := danglingChildren(t, st); len(d) > 0 {
		t.Fatalf("dangling: %v", d)
	}
}

// SetRole is what links a child to its master; its own statement must refuse a master that went after the
// check before it (the later carve in a spawn would otherwise be the only thing that notices). The master is
// deleted at each clock read of SetRole in turn.
func TestSetRoleChild_LeadDeletedBetweenCheckAndWrite(t *testing.T) {
	for k := 1; k <= 4; k++ {
		st := newStoreT(t)
		m, _ := master(t, st, "workspace")
		a, _ := ai.NewAIStore(st.db)
		kid, err := a.CreateConversationAccess("p", "agent", "", "claude", "", "m1", "workspace", t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.EnsureSession("p", kid.ID); err != nil {
			t.Fatal(err)
		}
		realNow, calls, fired := st.now, 0, false
		st.now = func() time.Time {
			calls++
			if calls == k {
				fired = true
				if err := a.DeleteConversation(m.ConversationID, "p"); err != nil {
					t.Fatal(err)
				}
			}
			return realNow()
		}
		err = st.SetRole("p", kid.ID, "child", m.ConversationID)
		if !fired {
			break
		}
		if d := danglingChildren(t, st); len(d) > 0 {
			t.Fatalf("clock read %d: child %v has a parent that is gone (SetRole err: %v)", k, d, err)
		}
	}
}

func TestSweepOrphansDemotesAChildWhoseMasterIsGone(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "workspace")
	kid := spawn(t, st, m)
	// the master vanishes without the delete's own demotion (an older build, a hand edit)
	st.db.Exec("DEL"+"ETE FROM coder_sessions WHERE conversation_id = ?", m.ConversationID)
	st.db.Exec("DEL"+"ETE FROM ai_chat_conversations WHERE id = ?", m.ConversationID)
	if len(danglingChildren(t, st)) != 1 {
		t.Fatal("setup: expected a dangling child")
	}
	if _, err := st.SweepOrphans(); err != nil {
		t.Fatal(err)
	}
	x, ok, _ := st.GetSession("p", kid)
	if !ok || x.Role != "solo" || x.Parent != "" {
		t.Fatalf("child = %+v (found %v), want solo", x, ok)
	}
	// a child of a live master is left alone
	m2, _ := master(t, st, "workspace")
	kid2 := spawn(t, st, m2)
	if _, err := st.SweepOrphans(); err != nil {
		t.Fatal(err)
	}
	if y, _, _ := st.GetSession("p", kid2); y.Role != "child" || y.Parent != m2.ConversationID {
		t.Fatalf("live child = %+v", y)
	}
}

// A child whose lead has no session row but whose conversation and mission are still there is a live mission
// child: the sweep leaves it a child, so its carve keeps holding the mission's budget.
func TestSweepOrphansKeepsALiveMissionChildWhoseLeadSessionRowIsMissing(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 10 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.addTasks(t, 1)
	kid := f.linkedChild(t, 4)
	lead := f.lead.ConversationID
	f.st.db.Exec("DEL"+"ETE FROM coder_sessions WHERE conversation_id = ?", lead)
	if _, err := f.st.SweepOrphans(); err != nil {
		t.Fatal(err)
	}
	x, ok, _ := f.st.GetSession("p", kid)
	if !ok || x.Role != "child" || x.Parent != lead {
		t.Fatalf("live mission child = %+v (found %v), want it left a child of its lead", x, ok)
	}
	// the lead's session comes back; the first child's carve still holds the budget
	if _, err := f.st.EnsureSession("p", lead); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetRole("p", lead, "master", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[1], MarkOptions{BudgetUSD: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); !errors.Is(err, ErrChildrenFull) {
		t.Fatalf("a second child was admitted against budget the first child's carve holds: %v", err)
	}
}

// When the lead's conversation is gone too, the mission rows go with it and the child is a solo session.
func TestSweepOrphansDemotesAMissionChildWhoseLeadConversationIsGone(t *testing.T) {
	f := newMC(t, true)
	kid := f.linkedChild(t, 0)
	lead := f.lead.ConversationID
	f.st.db.Exec("DEL"+"ETE FROM coder_sessions WHERE conversation_id = ?", lead)
	f.st.db.Exec("DEL"+"ETE FROM ai_chat_conversations WHERE id = ?", lead)
	if _, err := f.st.SweepOrphans(); err != nil {
		t.Fatal(err)
	}
	if x, ok, _ := f.st.GetSession("p", kid); !ok || x.Role != "solo" || x.Parent != "" {
		t.Fatalf("child = %+v (found %v), want solo", x, ok)
	}
}

// Only a child with no master in its own profile is demoted: a master of the same conversation id in another
// profile does not count, and no other row is touched.
func TestSweepOrphansJudgesTheMasterInTheChildsOwnProfileOnly(t *testing.T) {
	st := newStoreT(t)
	a, _ := ai.NewAIStore(st.db)
	mk := func(profile string) string {
		c, err := a.CreateConversationAccess(profile, "agent", "", "claude", "", "m1", "workspace", t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.EnsureSession(profile, c.ID); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	orphan := mk("p")
	foreign := mk("q")
	if err := st.SetRole("q", foreign, "master", ""); err != nil {
		t.Fatal(err)
	}
	// the child of p names a conversation that is a master only in q
	if _, err := st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = ? WHERE profile_id = 'p' AND conversation_id = ?`, foreign, orphan); err != nil {
		t.Fatal(err)
	}
	solo := mk("p")
	m, _ := master(t, st, "workspace")
	kid := spawn(t, st, m)
	if _, err := st.SweepOrphans(); err != nil {
		t.Fatal(err)
	}
	want := map[string][3]string{ // profile/conversation -> role, parent
		"p/" + orphan:           {"p", orphan, "solo"},
		"q/" + foreign:          {"q", foreign, "master"},
		"p/" + solo:             {"p", solo, "solo"},
		"p/" + m.ConversationID: {"p", m.ConversationID, "master"},
		"p/" + kid:              {"p", kid, "child"},
	}
	for k, w := range want {
		x, ok, _ := st.GetSession(w[0], w[1])
		if !ok || string(x.Role) != w[2] {
			t.Fatalf("%s = %+v (found %v), want role %s", k, x, ok, w[2])
		}
	}
	if x, _, _ := st.GetSession("p", orphan); x.Parent != "" {
		t.Fatalf("the demoted child kept a parent: %+v", x)
	}
	if x, _, _ := st.GetSession("p", kid); x.Parent != m.ConversationID {
		t.Fatalf("the live child's parent changed: %+v", x)
	}
}

// missionRowFixture is an orphaned child session (its parent has no session row) in profile "p", and a
// helper that inserts one mission row in a chosen profile and state naming it. The row's lead conversation
// exists in its own profile, so the sweep's earlier statements leave the row as it is.
type missionRowFixture struct {
	st   *Store
	kid  string
	lead map[string]string // profile -> a conversation that exists there
}

func newMissionRowFixture(t *testing.T) *missionRowFixture {
	t.Helper()
	st := newStoreT(t)
	a, _ := ai.NewAIStore(st.db)
	mk := func(profile string) string {
		c, err := a.CreateConversationAccess(profile, "agent", "", "claude", "", "m1", "workspace", t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.EnsureSession(profile, c.ID); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	f := &missionRowFixture{st: st, kid: mk("p"), lead: map[string]string{"p": mk("p"), "q": mk("q")}}
	if _, err := st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = 'no-such-master' WHERE profile_id = 'p' AND conversation_id = ?`, f.kid); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *missionRowFixture) addRow(t *testing.T, profile, state, child, pending string) {
	t.Helper()
	if _, err := f.st.db.Exec(`INSERT INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, child_conversation_id, pending_conversation_id, origin, state, created_at, updated_at)
		VALUES (?, 1, 1, ?, ?, ?, 'lead', ?, 'x', 'x')`, profile, f.lead[profile], child, pending, state); err != nil {
		t.Fatal(err)
	}
}

func (f *missionRowFixture) role(t *testing.T) string {
	t.Helper()
	if _, err := f.st.SweepOrphans(); err != nil {
		t.Fatal(err)
	}
	x, ok, _ := f.st.GetSession("p", f.kid)
	if !ok {
		t.Fatal("the child session is gone")
	}
	return string(x.Role)
}

// Both live states, by either column, keep the child a child.
func TestSweepOrphansKeepsAChildAMissionRowInTheSameProfileNames(t *testing.T) {
	for _, state := range []string{"spawning", "spawned"} {
		for _, byPending := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pending=%v", state, byPending), func(t *testing.T) {
				f := newMissionRowFixture(t)
				if byPending {
					f.addRow(t, "p", state, "", f.kid)
				} else {
					f.addRow(t, "p", state, f.kid, "")
				}
				if r := f.role(t); r != "child" {
					t.Fatalf("role = %s, want child", r)
				}
			})
		}
	}
}

// A live row of another profile that names the same conversation id says nothing about this profile's child.
func TestSweepOrphansIgnoresALiveMissionRowOfAnotherProfile(t *testing.T) {
	for _, state := range []string{"spawning", "spawned"} {
		t.Run(state, func(t *testing.T) {
			f := newMissionRowFixture(t)
			f.addRow(t, "q", state, "", f.kid)
			if r := f.role(t); r != "solo" {
				t.Fatalf("role = %s, want solo", r)
			}
		})
	}
}

// A row that is not live (marked, or in any terminal state) does not keep the child a child.
func TestSweepOrphansIgnoresAMissionRowThatIsNotLive(t *testing.T) {
	for _, state := range []string{"marked", "ended", "failed", "gone", "released"} {
		for _, byPending := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pending=%v", state, byPending), func(t *testing.T) {
				f := newMissionRowFixture(t)
				if byPending {
					f.addRow(t, "p", state, "", f.kid)
				} else {
					f.addRow(t, "p", state, f.kid, "")
				}
				if r := f.role(t); r != "solo" {
					t.Fatalf("role = %s, want solo", r)
				}
			})
		}
	}
}
