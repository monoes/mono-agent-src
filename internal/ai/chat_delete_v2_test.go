package ai

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

type v2Handle struct{ closed atomic.Int32 }

func (h *v2Handle) Alive() bool       { return h.closed.Load() == 0 }
func (h *v2Handle) Pid() (int, error) { return 4242, nil }
func (h *v2Handle) Close() error      { h.closed.Add(1); return nil }

var (
	v2Created = time.Now().UTC().Truncate(time.Millisecond)
	v2Expires = v2Created.Add(time.Hour)
)

func v2SessionToken(t *testing.T, db *sql.DB, b *procbind.Binder, id, conv string) *v2Handle {
	t.Helper()
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, db, "p1")
	if err != nil {
		t.Fatal(err)
	}
	row := procbind.SessionTokenRow{ID: id, ProfileID: "p1", TokenHash: "h-" + id, ConversationID: conv, TurnID: "turn-1", CreatedAt: v2Created, ExpiresAt: v2Expires}
	if err := procbind.InsertSessionToken(ctx, db, key, row); err != nil {
		t.Fatal(err)
	}
	h := &v2Handle{}
	if b != nil {
		gen, err := b.Register(procbind.EntrySpec{TokenID: id, Kind: procbind.KindSession, Profile: "p1", Scope: "session:7", Session: conv, Turn: "turn-1", Expires: v2Expires}, h)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Activate("p1", id, gen); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func v2SessionRevoked(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	rv, err := procbind.IsRevoked(context.Background(), db, procbind.KindSession, id)
	if err != nil {
		t.Fatal(err)
	}
	return rv
}

// Deleting a conversation (the plain form, no guard) revokes its v2 session tokens in the delete's transaction and
// ends their live entries after the commit; another conversation's are untouched.
func TestDeleteConversationRevokesItsV2SessionTokens(t *testing.T) {
	s, db := newChatEventStore(t)
	b := procbind.NewBinder(time.Now)
	s.SetBinder(b)
	gone, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	kept, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	hGone := v2SessionToken(t, db, b, "v2-gone", gone.ID)
	hKept := v2SessionToken(t, db, b, "v2-kept", kept.ID)
	if err := s.DeleteConversation(gone.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	if !v2SessionRevoked(t, db, "v2-gone") || hGone.closed.Load() != 1 {
		t.Error("the deleted conversation's v2 session token is still live")
	}
	if v2SessionRevoked(t, db, "v2-kept") || hKept.closed.Load() != 0 {
		t.Error("another conversation's v2 session token was touched")
	}
}

// If the v2 revocation cannot be written the delete is rolled back: the conversation stays and its token is not revoked.
func TestDeleteConversationRollsBackWhenTheV2RevocationFails(t *testing.T) {
	s, db := newChatEventStore(t)
	c, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	v2SessionToken(t, db, nil, "v2-c", c.ID)
	if _, err := db.Exec(`CREATE TRIGGER zz_refuse BEFORE INSERT ON token_revocations_v2 BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConversation(c.ID, "p1"); err == nil {
		t.Fatal("the delete succeeded although the v2 revocation failed")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_chat_conversations WHERE id = ?`, c.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("the conversation was deleted anyway (%d rows, %v)", n, err)
	}
	if v2SessionRevoked(t, db, "v2-c") {
		t.Error("a v2 revocation row exists")
	}
}

func v2MissionToken(t *testing.T, db *sql.DB, b *procbind.Binder, id, owner string) *v2Handle {
	t.Helper()
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, db, "p1")
	if err != nil {
		t.Fatal(err)
	}
	row := procbind.MissionTokenRow{ID: id, ProfileID: "p1", TokenHash: "h-" + id, Owner: owner, Session: 7, Task: 3, Role: "worker", Actor: "a", CreatedAt: v2Created, ExpiresAt: v2Expires}
	if err := procbind.InsertMissionToken(ctx, db, key, row); err != nil {
		t.Fatal(err)
	}
	h := &v2Handle{}
	gen, err := b.Register(procbind.EntrySpec{TokenID: id, Kind: procbind.KindMission, Profile: "p1", Scope: "mission:7", Owner: owner, Expires: v2Expires}, h)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Activate("p1", id, gen); err != nil {
		t.Fatal(err)
	}
	return h
}

// Every form of delete runs the same body, so each revokes the conversation's v2 session tokens AND the v2 mission
// tokens its runs minted (the owner and owner/run), and no other conversation's.
func TestEveryFormOfDeleteRevokesTheV2TokensOfTheConversation(t *testing.T) {
	forms := map[string]func(s *AIStore, id string) error{
		"plain": func(s *AIStore, id string) error { return s.DeleteConversation(id, "p1") },
		"guarded": func(s *AIStore, id string) error {
			return s.DeleteConversationGuarded(id, "p1", func(context.Context, *sql.Conn) error { return nil })
		},
		"if unowned (spawn cleanup)": func(s *AIStore, id string) error {
			return s.DeleteConversationIfUnowned(id, "p1", func(context.Context, *sql.Conn) (bool, error) { return true, nil })
		},
	}
	for name, del := range forms {
		s, db := newChatEventStore(t)
		b := procbind.NewBinder(time.Now)
		s.SetBinder(b)
		gone, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
		kept, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
		sess := v2SessionToken(t, db, b, "v2-s", gone.ID)
		own := v2MissionToken(t, db, b, "v2-own", ConversationOwner("p1", gone.ID))
		run := v2MissionToken(t, db, b, "v2-run", ConversationOwner("p1", gone.ID)+"/run1")
		otherM := v2MissionToken(t, db, b, "v2-other", ConversationOwner("p1", kept.ID))
		if err := del(s, gone.ID); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !v2SessionRevoked(t, db, "v2-s") || sess.closed.Load() != 1 {
			t.Errorf("%s: the session token is still live", name)
		}
		for id, h := range map[string]*v2Handle{"v2-own": own, "v2-run": run} {
			rv, err := procbind.IsRevoked(context.Background(), db, procbind.KindMission, id)
			if err != nil || !rv || h.closed.Load() != 1 {
				t.Errorf("%s: the mission token %s is still live (%v)", name, id, err)
			}
		}
		if rv, _ := procbind.IsRevoked(context.Background(), db, procbind.KindMission, "v2-other"); rv || otherM.closed.Load() != 0 {
			t.Errorf("%s: another conversation's mission token was revoked", name)
		}
	}
}

// A failing mission-token revocation also rolls the delete back.
func TestDeleteRollsBackWhenTheV2MissionRevocationFails(t *testing.T) {
	s, db := newChatEventStore(t)
	b := procbind.NewBinder(time.Now)
	c, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	v2MissionToken(t, db, b, "v2-m", ConversationOwner("p1", c.ID))
	if _, err := db.Exec(`CREATE TRIGGER zz_refuse BEFORE INSERT ON token_revocations_v2 BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConversationGuarded(c.ID, "p1", func(context.Context, *sql.Conn) error { return nil }); err == nil {
		t.Fatal("the delete succeeded although the v2 revocation failed")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_chat_conversations WHERE id = ?`, c.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("the conversation was deleted anyway (%d rows, %v)", n, err)
	}
}
