package doomsday

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

type bossRig struct {
	db      *storage.Database
	store   *ai.AIStore
	gateErr error
}

func newBossRig(t *testing.T) *bossRig {
	t.Helper()
	db, err := newMigratedDatabase(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p1', 'P1')`); err != nil {
		t.Fatal(err)
	}
	// The open mode's availability is the seam T3 fills in; a chat can only be made open while it says yes.
	was := coderhost.OpenModeAvailability
	coderhost.OpenModeAvailability = func() (bool, string) { return true, "" }
	t.Cleanup(func() { coderhost.OpenModeAvailability = was })
	st, err := ai.NewAIStore(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	return &bossRig{db: db, store: st}
}

// chat is a chat in the open mode, the only one a boss stands behind.
func (r *bossRig) chat(t *testing.T, access string) ai.Conversation {
	t.Helper()
	return r.chatIn(t, access, coderhost.ChatModeOpen)
}

func (r *bossRig) chatIn(t *testing.T, access, mode string) ai.Conversation {
	t.Helper()
	cwd := ""
	if access != ai.AccessScoped {
		cwd = t.TempDir()
	}
	c, err := r.store.CreateConversationAccess("p1", "agent", "general", "claude", "", "sonnet", access, cwd, "high")
	if err != nil {
		t.Fatal(err)
	}
	if mode != coderhost.ChatModeOff && access != ai.AccessScoped {
		if err := coderhost.NewStore(r.db.DB).SetChatMode("p1", c.ID, mode); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func (r *bossRig) run(args ...string) (string, error) {
	cmd := BossCommand(BossDeps{
		Open:         func() (*sql.DB, func(), error) { return r.db.DB, func() {}, nil },
		Profile:      func() string { return "p1" },
		OperatorGate: func(string) error { return r.gateErr },
		JSON:         func() bool { return true },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func (r *bossRig) newBoss(t *testing.T, front string) BossRef {
	t.Helper()
	out, err := r.run("new", "--conversation", front, "--goal", "reach the goal")
	if err != nil {
		t.Fatalf("boss new: %v %s", err, out)
	}
	var ref BossRef
	if err := json.Unmarshal([]byte(out), &ref); err != nil {
		t.Fatalf("json %q: %v", out, err)
	}
	return ref
}

// The boss is made as a sibling of the chat: a top-level mission session in a conversation of its own that copies the
// chat's runtime, folder and access, named for the chat by its client id. The same call again answers with it.
func TestBossNewMakesASiblingSessionInItsOwnConversation(t *testing.T) {
	r := newBossRig(t)
	front := r.chat(t, ai.AccessFull)
	ref := r.newBoss(t, front.ID)
	if ref.SessionID == 0 || ref.ConversationID == "" || ref.ConversationID == front.ID || ref.FrontDesk != front.ID || ref.Reused {
		t.Fatalf("ref: %+v", ref)
	}
	se, err := tasks.NewStore(r.db.DB).GetSession(context.Background(), "p1", ref.SessionID, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		t.Fatal(err)
	}
	if se.Kind != tasks.SessionKindMission || se.ParentSessionID != 0 || se.ConversationID != ref.ConversationID || se.ClientID != tasks.BossClientID(front.ID) || se.Boss {
		t.Fatalf("session: %+v", se)
	}
	bc, err := r.store.GetConversation(ref.ConversationID, "p1")
	if err != nil || bc.Access != front.Access || bc.Cwd != front.Cwd || bc.RuntimeID != front.RuntimeID || bc.Model != front.Model || bc.Effort != front.Effort {
		t.Fatalf("boss conversation: %+v %v", bc, err)
	}
	again := r.newBoss(t, front.ID)
	if again.SessionID != ref.SessionID || again.ConversationID != ref.ConversationID || !again.Reused {
		t.Fatalf("a repeat made another boss: %+v", again)
	}
	var n int
	_ = r.db.DB.QueryRow(`SELECT COUNT(*) FROM ai_chat_conversations`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d conversations after a repeat, want 2", n)
	}
}

// The session `start --boss-session` records is the one made here: the stamp accepts it.
func TestTheSessionBossNewMakesCanBeStampedAsTheBoss(t *testing.T) {
	r := newBossRig(t)
	ref := r.newBoss(t, r.chat(t, ai.AccessCoding).ID)
	w := Wiring{DB: r.db.DB, Profile: "p1"}
	if err := w.designateBoss(context.Background(), ref.SessionID, Entry{CharterID: "c1", Generation: 1}); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	se, _ := tasks.NewStore(r.db.DB).GetSession(context.Background(), "p1", ref.SessionID, tasks.Actor{Kind: tasks.Human})
	if !se.Boss {
		t.Fatalf("not marked as the boss: %+v", se)
	}
}

// What it refuses: a chat that does not exist or is a scoped one, a second boss while another chat's is open, a key an
// agent took for its own chat's session, and every caller that is not the operator. Nothing is created by a refusal.
func TestBossNewRefusals(t *testing.T) {
	r := newBossRig(t)
	count := func() (int, int) {
		var c, s int
		_ = r.db.DB.QueryRow(`SELECT COUNT(*) FROM ai_chat_conversations`).Scan(&c)
		_ = r.db.DB.QueryRow(`SELECT COUNT(*) FROM task_sessions`).Scan(&s)
		return c, s
	}
	scoped, coder := r.chat(t, ai.AccessScoped), r.chat(t, ai.AccessFull)
	for name, front := range map[string]string{"missing": "no-such-chat", "scoped": scoped.ID} {
		c0, s0 := count()
		if out, err := r.run("new", "--conversation", front); err == nil {
			t.Fatalf("%s chat: %s", name, out)
		}
		if c, s := count(); c != c0 || s != s0 {
			t.Fatalf("%s chat created rows", name)
		}
	}
	// a key taken by a session of the chat itself (an agent may link a session to its own chat)
	if _, err := tasks.NewStore(r.db.DB).CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "mine", Kind: tasks.SessionKindMission, ConversationID: coder.ID, ClientID: tasks.BossClientID(coder.ID)}, tasks.Actor{Kind: tasks.Human}); err != nil {
		t.Fatal(err)
	}
	if out, err := r.run("new", "--conversation", coder.ID); err == nil {
		t.Fatalf("took a session of the chat itself for the boss: %s", out)
	}
	other := r.chat(t, ai.AccessFull)
	r.newBoss(t, other.ID)
	third := r.chat(t, ai.AccessFull)
	if out, err := r.run("new", "--conversation", third.ID); err == nil {
		t.Fatalf("a second open boss: %s", out)
	}
	r.gateErr = errors.New("operator_only")
	c0, s0 := count()
	if _, err := r.run("new", "--conversation", third.ID); err == nil || !strings.Contains(err.Error(), "operator_only") {
		t.Fatalf("not the operator: %v", err)
	}
	if c, s := count(); c != c0 || s != s0 {
		t.Fatal("a refused caller created rows")
	}
}

// A boss stands behind a chat whose mode is open, chosen before its first message: an off chat, a mission chat and a
// chat that does not exist get a clear refusal and nothing is created.
func TestBossNewNeedsAnOpenChat(t *testing.T) {
	r := newBossRig(t)
	count := func() (c, se int) {
		_ = r.db.DB.QueryRow(`SELECT COUNT(*) FROM ai_chat_conversations`).Scan(&c)
		_ = r.db.DB.QueryRow(`SELECT COUNT(*) FROM task_sessions`).Scan(&se)
		return
	}
	for _, mode := range []string{coderhost.ChatModeOff, coderhost.ChatModeMission} {
		front := r.chatIn(t, ai.AccessFull, mode)
		c0, s0 := count()
		out, err := r.run("new", "--conversation", front.ID)
		if err == nil || !strings.Contains(err.Error(), "mode is "+mode) || !strings.Contains(err.Error(), "open") {
			t.Fatalf("%s chat: %v %s", mode, err, out)
		}
		if c, s := count(); c != c0 || s != s0 {
			t.Fatalf("%s chat created rows", mode)
		}
	}
	if _, err := r.run("new", "--conversation", "no-such-chat"); err == nil || !strings.Contains(err.Error(), "no chat") {
		t.Fatalf("missing chat: %v", err)
	}
	open := r.chatIn(t, ai.AccessFull, coderhost.ChatModeOpen)
	if ref := r.newBoss(t, open.ID); ref.SessionID == 0 {
		t.Fatalf("open chat: %+v", ref)
	}
}
