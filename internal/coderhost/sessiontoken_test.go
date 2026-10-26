package coderhost

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// running marks a session as running turn id, as the host does before it starts one.
func running(t *testing.T, st *Store, conv, turn string) {
	t.Helper()
	x, _, _ := st.GetSession("p", conv)
	x.State, x.TurnID = StateRunning, turn
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
}

// mint issues the token of a turn and binds it to this test process, standing in for the host's turn.
func mint(t *testing.T, st *Store, conv, turn string) string {
	t.Helper()
	tok, err := st.IssueSessionToken("p", conv, turn)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindSessionToken("p", conv, turn, os.Getpid(), ProcStamp(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestATokenNamesTheSessionItWasMintedFor(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a, b := spawn(t, st, m), spawn(t, st, m)
	running(t, st, a, "t-a")
	running(t, st, b, "t-b")
	ta := mint(t, st, a, "t-a")
	tb := mint(t, st, b, "t-b")
	if !strings.HasPrefix(ta, SessionTokenPrefix) || ta == tb {
		t.Fatalf("tokens %q %q", ta, tb)
	}
	ca, err := st.VerifySessionToken(ta)
	if err != nil || ca.ConversationID != a || ca.ProfileID != "p" || ca.TurnID != "t-a" {
		t.Fatalf("caller = %+v %v", ca, err)
	}
	// A's token acts as A: it cannot reach B (a peer), whatever B's id is passed.
	if _, err := st.SendToChild(ca, b, "hi"); !errors.Is(err, ErrNotYours) {
		t.Fatalf("A's token controlled B: %v", err)
	}
	if _, err := st.StatusOf(ca, b); err == nil {
		t.Fatal("A's token read B")
	}
	cb, _ := st.VerifySessionToken(tb)
	if cb.ConversationID != b {
		t.Fatalf("B's token names %+v", cb)
	}
}

func TestForgedAbsentExpiredAndStaleTokensIdentifyNoOne(t *testing.T) {
	st := newStoreT(t)
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	m, _ := master(t, st, "coding")
	running(t, st, m.ConversationID, "t1")
	tok := mint(t, st, m.ConversationID, "t1")

	for _, bad := range []string{"", "mst-", "mst-forged", tok + "x", tok[:len(tok)-1], strings.Replace(tok, "mst-", "mat-", 1), "t1", strings.Repeat("mst-a", 100)} {
		if c, err := st.VerifySessionToken(bad); !errors.Is(err, ErrSessionToken) || c != (Caller{}) {
			t.Errorf("%q identified %+v %v", bad, c, err)
		}
	}
	// a caller with no identity controls nothing
	a := spawn(t, st, m)
	if _, err := st.SendToChild(Caller{}, a, "hi"); err == nil {
		t.Fatal("an anonymous send was queued")
	}

	// the token is dead once its turn is no longer the session's current turn
	running(t, st, m.ConversationID, "t2")
	if _, err := st.VerifySessionToken(tok); err == nil {
		t.Fatal("the token of an old turn still works")
	}
	running(t, st, m.ConversationID, "t1")
	if _, err := st.VerifySessionToken(tok); err != nil {
		t.Fatal(err)
	}
	now = now.Add(SessionTokenTTL + time.Second)
	if _, err := st.VerifySessionToken(tok); err == nil {
		t.Fatal("an expired token works")
	}
	now = now.Add(-SessionTokenTTL)
	x, _, _ := st.GetSession("p", m.ConversationID)
	x.State, x.TurnID = StateIdle, ""
	st.SaveSession(x)
	if _, err := st.VerifySessionToken(tok); err == nil {
		t.Fatal("a token works while its session runs no turn")
	}
}

func TestOnlyTheHashOfATokenIsStoredAndANewTurnReplacesTheOldToken(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	running(t, st, m.ConversationID, "t1")
	tok1 := mint(t, st, m.ConversationID, "t1")
	var stored string
	if err := st.db.QueryRow(`SELECT token_hash FROM coder_session_tokens`).Scan(&stored); err != nil || stored == tok1 || strings.Contains(stored, tok1) {
		t.Fatalf("stored %q for %q: %v", stored, tok1, err)
	}
	tok2 := mint(t, st, m.ConversationID, "t1")
	if tok1 == tok2 {
		t.Fatal("tokens repeat")
	}
	if _, err := st.VerifySessionToken(tok1); err == nil {
		t.Fatal("the replaced token still works")
	}
	st.RevokeSessionTokens("p", m.ConversationID)
	if _, err := st.VerifySessionToken(tok2); err == nil {
		t.Fatal("a revoked token works")
	}
	if _, err := st.IssueSessionToken("p", "", "t"); err == nil {
		t.Fatal("a token without a session was minted")
	}
}
