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
	if se.Kind != tasks.SessionKindMission || se.ParentSessionID != 0 || se.ConversationID != ref.ConversationID || se.ClientID != tasks.BossClientID(front.ID, 1) || se.Boss {
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
	scoped := r.chat(t, ai.AccessScoped)
	for name, front := range map[string]string{"missing": "no-such-chat", "scoped": scoped.ID} {
		c0, s0 := count()
		if out, err := r.run("new", "--conversation", front); err == nil {
			t.Fatalf("%s chat: %s", name, out)
		}
		if c, s := count(); c != c0 || s != s0 {
			t.Fatalf("%s chat created rows", name)
		}
	}
	// a boss that is made but not stamped (an abandoned draft) holds nothing for the other chats
	other := r.chat(t, ai.AccessFull)
	otherRef := r.newBoss(t, other.ID)
	third := r.chat(t, ai.AccessFull)
	if ref := r.newBoss(t, third.ID); ref.SessionID == 0 || ref.SessionID == otherRef.SessionID {
		t.Fatalf("an abandoned draft blocked another chat: %+v", ref)
	}
	// a boss whose run is going does: one run at a time per profile
	if _, err := r.db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p1', 1, 'c1', 'running', x'00')`); err != nil {
		t.Fatal(err)
	}
	if err := (Wiring{DB: r.db.DB, Profile: "p1"}).designateBoss(context.Background(), otherRef.SessionID, Entry{CharterID: "c1", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	fourth := r.chat(t, ai.AccessFull)
	if out, err := r.run("new", "--conversation", fourth.ID); err == nil {
		t.Fatalf("a second run's boss while one is going: %s", out)
	}
	third = fourth
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

func (r *bossRig) conversations() (n int) {
	_ = r.db.DB.QueryRow(`SELECT COUNT(*) FROM ai_chat_conversations`).Scan(&n)
	return
}

// The agent's side of a squat: a session an agent made with the key the operator's boss would take.
func (r *bossRig) squat(t *testing.T, front ai.Conversation, n int, kind string) tasks.Session {
	t.Helper()
	se, err := tasks.NewStore(r.db.DB).CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "squat", Kind: kind, ConversationID: front.ID, ClientID: tasks.BossClientID(front.ID, n)},
		tasks.Actor{Kind: tasks.Agent, Name: "x", Conversation: front.ID})
	if err != nil {
		t.Fatal(err)
	}
	return se
}

// An agent that creates a session with the key the boss is about to take, between the operator's check and the creation,
// is never handed to the operator as her boss: the creation sees the dedupe, refuses, and makes no conversation. The next
// call takes the next key.
func TestBossNewRefusesASessionAnAgentSlippedInBetween(t *testing.T) {
	r := newBossRig(t)
	front := r.chat(t, ai.AccessFull)
	slipped := false
	was := beforeBossCreate
	beforeBossCreate = func() {
		if !slipped {
			slipped = true
			r.squat(t, front, 1, tasks.SessionKindTask)
		}
	}
	t.Cleanup(func() { beforeBossCreate = was })
	before := r.conversations()
	if out, err := r.run("new", "--conversation", front.ID); err == nil {
		t.Fatalf("the agent's session was returned as the boss: %s", out)
	}
	if r.conversations() != before {
		t.Fatal("the refused creation left a conversation behind")
	}
	ref := r.newBoss(t, front.ID)
	se, _ := tasks.NewStore(r.db.DB).GetSession(context.Background(), "p1", ref.SessionID, tasks.Actor{Kind: tasks.Human})
	if se.ClientID != tasks.BossClientID(front.ID, 2) || se.Kind != tasks.SessionKindMission {
		t.Fatalf("the retry: %+v", se)
	}
}

// A key somebody squatted (even archived, which keeps its unique client id) costs one attempt number, not the chat.
func TestBossNewSkipsKeysThatAreTaken(t *testing.T) {
	r := newBossRig(t)
	front := r.chat(t, ai.AccessFull)
	sq := r.squat(t, front, 1, tasks.SessionKindTask)
	if _, err := r.db.DB.Exec(`UPDATE task_sessions SET archived_at = '2026-11-06T00:00:00Z' WHERE id = ?`, sq.ID); err != nil {
		t.Fatal(err)
	}
	ref := r.newBoss(t, front.ID)
	if ref.SessionID == sq.ID || ref.Reused {
		t.Fatalf("the squatter's session was taken: %+v", ref)
	}
}

// A boss that already ran (stamped, its run over) or was archived is never reused: the chat's next run gets a new boss in a
// new conversation. A boss that is made but not yet stamped is the one a repeat call answers with, and a boss whose run is
// going is not made again.
func TestBossNewDoesNotReuseAFinishedBoss(t *testing.T) {
	r := newBossRig(t)
	front := r.chat(t, ai.AccessFull)
	first := r.newBoss(t, front.ID)
	if again := r.newBoss(t, front.ID); !again.Reused || again.SessionID != first.SessionID {
		t.Fatalf("an unstamped boss is not answered again: %+v", again)
	}
	if _, err := r.db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p1', 1, 'c1', 'running', x'00')`); err != nil {
		t.Fatal(err)
	}
	w := Wiring{DB: r.db.DB, Profile: "p1"}
	if err := w.designateBoss(context.Background(), first.SessionID, Entry{CharterID: "c1", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	if out, err := r.run("new", "--conversation", front.ID); err == nil || !strings.Contains(err.Error(), "run is going") {
		t.Fatalf("a second boss while the run is going: %v %s", err, out)
	}
	for _, state := range []string{"stop-requested", "authority-revoked", "cleanup-pending", "quiescent"} {
		if _, err := r.db.DB.Exec(`UPDATE doomsday_runs SET state = ? WHERE profile = 'p1'`, state); err != nil {
			t.Fatal(err)
		}
	}
	second := r.newBoss(t, front.ID)
	if second.Reused || second.SessionID == first.SessionID || second.ConversationID == first.ConversationID {
		t.Fatalf("the finished boss came back: %+v", second)
	}
	se, _ := tasks.NewStore(r.db.DB).GetSession(context.Background(), "p1", second.SessionID, tasks.Actor{Kind: tasks.Human})
	if se.ClientID != tasks.BossClientID(front.ID, 2) || se.Boss {
		t.Fatalf("second boss: %+v", se)
	}
}

// The boss's session goal says what operator steering is, so the statement is in front of the boss from its first turn.
func TestBossNewGoalSaysSteeringIsUntrusted(t *testing.T) {
	r := newBossRig(t)
	ref := r.newBoss(t, r.chat(t, ai.AccessFull).ID)
	se, _ := tasks.NewStore(r.db.DB).GetSession(context.Background(), "p1", ref.SessionID, tasks.Actor{Kind: tasks.Human})
	for _, want := range []string{"reach the goal", "comes from the person's charter", "untrusted", "not an answer", "not an approval", "cannot lift", "charter wins"} {
		if !strings.Contains(se.Goal, want) {
			t.Fatalf("the goal lacks %q:\n%s", want, se.Goal)
		}
	}
}

// A session under the chat's key is the operator's unstamped boss only if she made it (created_by), at the top level
// (no parent), as a mission in a conversation of its own: any other is left alone and the boss gets the next key.
func TestBossNewReusesOnlyTheOperatorsTopLevelMission(t *testing.T) {
	for name, set := range map[string]string{
		"made by an agent": `created_by = 'agent-x'`,
		"below a parent":   `parent_session_id = (SELECT MIN(id) FROM task_sessions WHERE id <> task_sessions.id)`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newBossRig(t)
			front := r.chat(t, ai.AccessFull)
			other := r.chat(t, ai.AccessFull)
			parent, err := tasks.NewStore(r.db.DB).CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "parent", Kind: tasks.SessionKindMission, ConversationID: other.ID}, bossActor)
			if err != nil {
				t.Fatal(err)
			}
			first := r.newBoss(t, front.ID)
			set = strings.ReplaceAll(set, "(SELECT MIN(id) FROM task_sessions WHERE id <> task_sessions.id)", "?")
			args := []any{first.SessionID}
			if strings.Contains(set, "?") {
				args = []any{parent.ID, first.SessionID}
			}
			if _, err := r.db.DB.Exec(`UPDATE task_sessions SET `+set+` WHERE id = ?`, args...); err != nil {
				t.Fatal(err)
			}
			again := r.newBoss(t, front.ID)
			if again.Reused || again.SessionID == first.SessionID {
				t.Fatalf("a session that is not the operator's top-level boss was answered again: %+v", again)
			}
		})
	}
}

// An agent that takes keys cannot lock a chat out: a key with a huge number is not a boss key, a taken small number is
// skipped, and the first free number is used (so the key always fits a client id).
func TestBossNewSurvivesSquattedAndHugeKeys(t *testing.T) {
	r := newBossRig(t)
	front := r.chat(t, ai.AccessFull)
	r.squat(t, front, 1, tasks.SessionKindTask)
	r.squat(t, front, tasks.MaxBossAttempt, tasks.SessionKindTask)
	if _, err := tasks.NewStore(r.db.DB).CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "huge", Kind: tasks.SessionKindTask, ConversationID: front.ID,
		ClientID: tasks.BossClientIDPrefix + front.ID[:20] + "-123456789012345678"}, tasks.Actor{Kind: tasks.Agent, Name: "x", Conversation: front.ID}); err != nil {
		t.Fatal(err)
	}
	ref := r.newBoss(t, front.ID)
	se, _ := tasks.NewStore(r.db.DB).GetSession(context.Background(), "p1", ref.SessionID, tasks.Actor{Kind: tasks.Human})
	if se.ClientID != tasks.BossClientID(front.ID, 2) {
		t.Fatalf("the key: %s", se.ClientID)
	}
}

// The predicate `--boss-session` stamps by is one exported statement (BossCandidateSQL), so a check made before the start
// (the entry screen's) asks exactly what the stamp asks.
func TestBossCandidateSQLIsWhatTheStampAsks(t *testing.T) {
	r := newBossRig(t)
	st := tasks.NewStore(r.db.DB)
	op := tasks.Actor{Kind: tasks.Human, Name: "operator"}
	mk := func(kind string, parent int64) int64 {
		se, err := st.CreateSession(context.Background(), "p1", tasks.SessionInput{Title: kind, Kind: kind, ConversationID: "c-" + kind, ParentSessionID: parent}, op)
		if err != nil {
			t.Fatal(err)
		}
		return se.ID
	}
	mission := mk(tasks.SessionKindMission, 0)
	task := mk(tasks.SessionKindTask, 0)
	child := mk(tasks.SessionKindDirection, mission)
	archived := mk(tasks.SessionKindResearch, 0)
	if _, err := r.db.DB.Exec(`UPDATE task_sessions SET archived_at = 'x' WHERE id = ?`, archived); err != nil {
		t.Fatal(err)
	}
	var got []int64
	rows, err := r.db.DB.Query(`SELECT id FROM task_sessions WHERE profile_id = 'p1' AND ` + BossCandidateSQL + ` ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		got = append(got, id)
	}
	if len(got) != 1 || got[0] != mission {
		t.Fatalf("candidates %v (mission %d, task %d, child %d, archived %d)", got, mission, task, child, archived)
	}
}
