package coderhost

import (
	"testing"
	"time"
)

// An ask's timestamps are compared as TEXT in SQL, so they must sort like the instants they name. RFC3339Nano
// drops a zero fraction ("12:00:00Z" sorts AFTER "12:00:00.000000001Z"), which let an ask made on a whole second
// stay open past AskTTL and an ask made just after a whole second expire early.
func askAtSecond(t *testing.T, f *askTree, at time.Time) int64 {
	t.Helper()
	f.now = at
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	return got.AskID
}

func TestAnAskMadeOnAWholeSecondExpiresAtAskTTLPlusOneNanosecond(t *testing.T) {
	f := newAskTree(t)
	created := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	id := askAtSecond(t, f, created)
	f.now = created.Add(AskTTL) // exactly at the limit: still open
	if a, err := f.st.GetAsk("p", id); err != nil || a.Status != askOpen {
		t.Fatalf("at AskTTL: %+v %v", a, err)
	}
	f.now = created.Add(AskTTL + time.Nanosecond)
	if a, err := f.st.GetAsk("p", id); err != nil || a.Status != askExpired {
		t.Errorf("GetAsk one ns past AskTTL read %q, want expired (%v)", a.Status, err)
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", id, "late"); err == nil {
		t.Fatal("ReplyAsk accepted an answer one ns past AskTTL")
	}
}

func TestAnAskMadeJustAfterAWholeSecondDoesNotExpireEarly(t *testing.T) {
	f := newAskTree(t)
	created := time.Date(2026, 10, 31, 12, 0, 0, 500_000_000, time.UTC)
	id := askAtSecond(t, f, created)
	f.now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC).Add(AskTTL) // cutoff = 12:00:00, before created
	if a, err := f.st.GetAsk("p", id); err != nil || a.Status != askOpen {
		t.Fatalf("expired early: %+v %v", a, err)
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", id, "on time"); err != nil {
		t.Fatalf("an on-time reply was refused: %v", err)
	}
}

func TestTheSweepUsesTheSameFixedWidthStamps(t *testing.T) {
	f := newAskTree(t)
	created := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	id := askAtSecond(t, f, created)
	status := func() string {
		var s string
		if err := f.st.db.QueryRow(`SELECT status FROM coder_session_asks WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	f.st.expireAsks("p", created.Add(AskTTL))
	if got := status(); got != askOpen {
		t.Fatalf("the sweep closed an ask exactly at AskTTL: %s", got)
	}
	f.st.expireAsks("p", created.Add(AskTTL+time.Nanosecond))
	if got := status(); got != askExpired {
		t.Fatalf("the sweep left an ask open one ns past AskTTL: %s", got)
	}
}
