package procbind

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A token id is unique across both token families: migration 148 keys the bindings (and, with the kind, the
// revocations) so that one id cannot be bound or revoked twice, and the ids are random 128-bit values, so the
// stores refuse an id the other family already holds.
func TestATokenIdIsUniqueAcrossBothFamilies(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	if err := InsertMissionToken(ctx, db, storeKey, mission("same", "p1", "owner")); err != nil {
		t.Fatal(err)
	}
	if err := InsertSessionToken(ctx, db, storeKey, session("same", "p2", "conv", "turn")); !errors.Is(err, ErrIDInUse) {
		t.Errorf("a session token with a mission token's id (another profile too): %v", err)
	}
	if err := InsertSessionToken(ctx, db, storeKey, session("other", "p1", "conv", "turn")); err != nil {
		t.Fatal(err)
	}
	if err := InsertMissionToken(ctx, db, storeKey, mission("other", "p1", "owner")); !errors.Is(err, ErrIDInUse) {
		t.Errorf("a mission token with a session token's id: %v", err)
	}
	// ids of one family stay unique as the table says
	if err := InsertMissionToken(ctx, db, storeKey, mission("same", "p1", "owner")); err == nil {
		t.Error("the same mission id was inserted twice")
	}
	// the refused insert wrote nothing
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM coder_session_tokens_v2 WHERE id = 'same'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused insert left %d row(s)", n)
	}
}

func TestNewTokenIdsAreRandom128BitHex(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id, err := NewTokenID()
		if err != nil || len(id) != 32 {
			t.Fatalf("id %q: %v", id, err)
		}
		for _, c := range id {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				t.Fatalf("not lowercase hex: %q", id)
			}
		}
		if seen[id] {
			t.Fatalf("a repeated id: %q", id)
		}
		seen[id] = true
	}
}

// A Drop delayed past the token's expiry names the OLD token: it must not end a newer token that reuses the id.
func TestADelayedDropOfAnOldTokenDoesNotEndANewOne(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	clk := &bclock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	b := NewBinder(clk.now)

	oldExp := clk.now().Add(time.Hour)
	m := mission("t1", "p1", "owner")
	m.CreatedAt, m.ExpiresAt = clk.now(), oldExp
	mustInsertMission(t, db, m)
	oldS := spec("t1")
	oldS.Kind, oldS.Owner, oldS.Session, oldS.Turn, oldS.Expires = KindMission, "owner", "", "", oldExp
	oldH := &cHandle{}
	oldGen, err := b.Register(oldS, oldH)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Activate("p1", "t1", oldGen); err != nil {
		t.Fatal(err)
	}
	refs, err := RevokeTx(ctx, db, keyFor(nil), Selector{Profile: "p1", TokenID: "t1"}, "ended", clk.now())
	if err != nil || len(refs) != 1 {
		t.Fatalf("%v %v", refs, err)
	}
	if refs[0].Expires.IsZero() || !refs[0].Expires.Equal(oldExp) {
		t.Fatalf("the ref does not carry the token's expiry: %+v", refs[0])
	}
	clk.add(2 * time.Hour) // the old token expired; its Drop is still on its way
	newExp := clk.now().Add(time.Hour)
	newS := oldS
	newS.Expires = newExp
	newH := &cHandle{}
	newGen, err := b.Register(newS, newH)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Activate("p1", "t1", newGen); err != nil {
		t.Fatal(err)
	}
	if n := Drop(b, refs); n != 0 {
		t.Fatalf("a delayed Drop of the old token ended %d newer entry(ies)", n)
	}
	if newH.timesClosed() != 0 {
		t.Fatal("the newer worker's handle was closed by a stale Drop")
	}
	if l, ok := b.Acquire("t1", "p1", "session:7"); !ok {
		t.Error("the newer entry stopped answering")
	} else {
		l.Release()
	}
	// a Drop of the right token still works, whatever precision its expiry was stored with
	ok := TokenRef{Kind: KindMission, TokenID: "t1", Profile: "p1", Expires: newExp.Add(300 * time.Microsecond)}
	if n := Drop(b, []TokenRef{ok}); n != 1 || newH.timesClosed() != 1 {
		t.Errorf("a Drop of the current token ended %d entries, handle closed %d times", n, newH.timesClosed())
	}
	// a ref with a generation names exactly that registration
	again := spec("t2")
	again.Expires = clk.now().Add(time.Hour)
	h2 := &cHandle{}
	g2, err := b.Register(again, h2)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Activate("p1", "t2", g2); err != nil {
		t.Fatal(err)
	}
	if n := Drop(b, []TokenRef{{Kind: KindSession, TokenID: "t2", Profile: "p1", Generation: Generation{g2.Epoch, g2.N + 1}}}); n != 0 || h2.timesClosed() != 0 {
		t.Errorf("a ref of another generation ended an entry: %d", n)
	}
	if n := Drop(b, []TokenRef{{Kind: KindSession, TokenID: "t2", Profile: "p1", Generation: g2}}); n != 1 {
		t.Errorf("a ref of the current generation ended %d entries", n)
	}
}

// A key function that returns one reused buffer must not make a later call overwrite an earlier profile's key.
func TestKeyBytesAreCopiedWhenTheCallbackReusesABuffer(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("a", "p1", "owner"))
	mustInsertMission(t, db, mission("b", "p2", "owner"))
	keyP1, keyP2 := bytesOf(11), bytesOf(22)
	buf := make([]byte, 32)
	reuse := func(profile string) ([]byte, error) {
		if profile == "p1" {
			copy(buf, keyP1)
		} else {
			copy(buf, keyP2)
		}
		return buf, nil
	}
	if _, err := RevokeTx(ctx, db, reuse, Selector{Owner: "owner"}, "x", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := GetRevocation(ctx, db, keyP1, KindMission, "p1", "a"); err != nil {
		t.Errorf("p1's revocation is not signed under p1's key: %v", err)
	}
	if _, err := GetRevocation(ctx, db, keyP2, KindMission, "p2", "b"); err != nil {
		t.Errorf("p2's revocation is not signed under p2's key: %v", err)
	}
}
