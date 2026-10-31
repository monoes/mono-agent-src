package coderhost

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

type fakeHandle struct{ closed atomic.Int32 }

func (h *fakeHandle) Alive() bool       { return h.closed.Load() == 0 }
func (h *fakeHandle) Pid() (int, error) { return 4242, nil }
func (h *fakeHandle) Close() error      { h.closed.Add(1); return nil }

// the times of every v2 token here: a registry entry for it carries the same expiry
var (
	v2Created = time.Now().UTC().Truncate(time.Millisecond)
	v2Expires = v2Created.Add(time.Hour)
)

// v2Session puts a v2 session token in the store and a live registry entry for it in b (when given).
func v2Session(t *testing.T, st *Store, b *procbind.Binder, id, conv, turn string) *fakeHandle {
	t.Helper()
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, st.db, "p")
	if err != nil {
		t.Fatal(err)
	}
	row := procbind.SessionTokenRow{ID: id, ProfileID: "p", TokenHash: "h-" + id, ConversationID: conv, TurnID: turn, CreatedAt: v2Created, ExpiresAt: v2Expires}
	if err := procbind.InsertSessionToken(ctx, st.db, key, row); err != nil {
		t.Fatal(err)
	}
	h := &fakeHandle{}
	if b != nil {
		gen, err := b.Register(procbind.EntrySpec{TokenID: id, Kind: procbind.KindSession, Profile: "p", Scope: "session:7", Session: conv, Turn: turn, Expires: v2Expires}, h)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Activate("p", id, gen); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func v2Revoked(t *testing.T, st *Store, id string) bool {
	t.Helper()
	rv, err := procbind.IsRevoked(context.Background(), st.db, procbind.KindSession, id)
	if err != nil {
		t.Fatal(err)
	}
	return rv
}

func legacyTokens(t *testing.T, st *Store, conv string) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_tokens WHERE conversation_id = ?`, conv).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Every path that deletes a session's legacy token revokes its v2 session tokens in the same transaction, ends
// their live entries after the commit, and touches no other session's.
func TestEveryLegacyTokenDeletionAlsoRevokesTheV2SessionTokens(t *testing.T) {
	for name, run := range map[string]func(st *Store, conv string) error{
		"RevokeSessionTokensErr": func(st *Store, conv string) error { return st.RevokeSessionTokensErr("p", conv) },
		"IssueSessionToken": func(st *Store, conv string) error {
			_, err := st.IssueSessionToken("p", conv, "turn-2")
			return err
		},
		"SweepOrphans": func(st *Store, conv string) error { _, err := st.SweepOrphans(); return err },
	} {
		_, _, st := newHost(t)
		b := procbind.NewBinder(time.Now)
		st.SetBinder(b)
		addConversation(t, st, "live", "/work/live")
		conv := "ghost" // no conversation row: SweepOrphans takes its tokens
		if name != "SweepOrphans" {
			conv = "live"
		}
		other := "other"
		if name == "SweepOrphans" {
			other = "live"
		} else {
			addConversation(t, st, "other", "/work/other")
		}
		if _, err := st.IssueSessionToken("p", conv, "turn-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.IssueSessionToken("p", other, "turn-1"); err != nil {
			t.Fatal(err)
		}
		h := v2Session(t, st, b, "v2-target", conv, "turn-1")
		hOther := v2Session(t, st, b, "v2-other", other, "turn-1")
		if err := run(st, conv); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !v2Revoked(t, st, "v2-target") {
			t.Errorf("%s: the v2 token of the session is still live", name)
		}
		if h.closed.Load() != 1 {
			t.Errorf("%s: the live entry was not ended after the commit", name)
		}
		if v2Revoked(t, st, "v2-other") || hOther.closed.Load() != 0 {
			t.Errorf("%s: another session's v2 token was touched", name)
		}
		if legacyTokens(t, st, other) != 1 {
			t.Errorf("%s: another session's legacy token was removed", name)
		}
	}
}

// In the flag-on world an orphaned session has a v2 token and no legacy one: the sweep still finds and revokes it.
func TestSweepOrphansRevokesAV2TokenThatHasNoLegacyToken(t *testing.T) {
	_, _, st := newHost(t)
	b := procbind.NewBinder(time.Now)
	st.SetBinder(b)
	addConversation(t, st, "live", "/work/live")
	h := v2Session(t, st, b, "v2-ghost", "ghost", "turn-1")
	hLive := v2Session(t, st, b, "v2-live", "live", "turn-1")
	if _, err := st.SweepOrphans(); err != nil {
		t.Fatal(err)
	}
	if !v2Revoked(t, st, "v2-ghost") || h.closed.Load() != 1 {
		t.Error("the orphaned session's v2 token is still live")
	}
	if v2Revoked(t, st, "v2-live") || hLive.closed.Load() != 0 {
		t.Error("a live session's v2 token was revoked")
	}
}

// If the v2 side cannot be written, the legacy delete is rolled back and the call fails (decision K1).
func TestAFailingV2RevocationKeepsTheLegacyTokenAndFailsTheCall(t *testing.T) {
	for name, run := range map[string]func(st *Store, conv string) error{
		"RevokeSessionTokensErr": func(st *Store, conv string) error { return st.RevokeSessionTokensErr("p", conv) },
		"IssueSessionToken": func(st *Store, conv string) error {
			_, err := st.IssueSessionToken("p", conv, "turn-2")
			return err
		},
		"SweepOrphans": func(st *Store, conv string) error { _, err := st.SweepOrphans(); return err },
	} {
		_, _, st := newHost(t)
		conv := "ghost"
		if name != "SweepOrphans" {
			addConversation(t, st, "live", "/work/live")
			conv = "live"
		}
		if _, err := st.IssueSessionToken("p", conv, "turn-1"); err != nil {
			t.Fatal(err)
		}
		v2Session(t, st, nil, "v2-target", conv, "turn-1")
		if _, err := st.db.Exec(`CREATE TRIGGER zz_refuse BEFORE INSERT ON token_revocations_v2 BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
			t.Fatal(err)
		}
		if err := run(st, conv); err == nil {
			t.Errorf("%s: the call succeeded although the v2 revocation failed", name)
		}
		if legacyTokens(t, st, conv) != 1 {
			t.Errorf("%s: the legacy token was deleted although the v2 revocation failed", name)
		}
		if v2Revoked(t, st, "v2-target") {
			t.Errorf("%s: a v2 revocation row exists", name)
		}
	}
}
