package coderhost

import (
	"context"
	"database/sql"
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
