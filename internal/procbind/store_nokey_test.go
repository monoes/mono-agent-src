package procbind

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// A key function that says a profile has no row key (ErrNoRowKey) makes RevokeTx skip that profile's tokens (they
// are signed under a key that no longer exists) and revoke the others', without an error and without a row for it.
func TestRevokeTxSkipsAProfileWithoutARowKey(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("a", "p1", "owner"))
	mustInsertMission(t, db, mission("b", "p2", "owner"))
	keys := func(profile string) ([]byte, error) {
		if profile == "p2" {
			return nil, ErrNoRowKey
		}
		return storeKey, nil
	}
	refs, err := RevokeTx(ctx, db, keys, Selector{Owner: "owner"}, "x", t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].TokenID != "a" {
		t.Fatalf("revoked %+v, want only p1's token", refs)
	}
	if rv, _ := IsRevoked(ctx, db, KindMission, "b"); rv {
		t.Error("a revocation row was written for the profile without a key")
	}
	if rv, _ := IsRevoked(ctx, db, KindMission, "a"); !rv {
		t.Error("the other profile's token was not revoked")
	}
}

// Keys derives only for profiles within the scope that hold a not-yet-revoked token: another profile's key is not
// derived (asking for it fails closed), a revoked token needs none, and a profile without a vault row has no key.
func TestKeysAreDerivedOnlyWithinTheScope(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	for _, p := range []string{"p1", "p2", "p3"} {
		if _, err := RowKey(ctx, db, p); err != nil {
			t.Fatal(err)
		}
	}
	mustInsertMission(t, db, mission("a", "p1", "owner"))
	mustInsertMission(t, db, mission("b", "p2", "owner"))
	mustInsertMission(t, db, mission("c", "p3", "owner"))
	if _, err := RevokeTx(ctx, db, keyFor(nil), Selector{Profile: "p3", Owner: "owner"}, "x", t0); err != nil {
		t.Fatal(err)
	}
	keyOf, err := Revoker{DB: db}.Keys(ctx, []Kind{KindMission}, Scope{Profile: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if k, err := keyOf("p1"); err != nil || len(k) < MinKeyLen {
		t.Errorf("the scoped profile has no key: %v", err)
	}
	for _, other := range []string{"p2", "p3", "nobody"} {
		if k, err := keyOf(other); err == nil || errors.Is(err, ErrNoRowKey) || k != nil {
			t.Errorf("a key for %s outside the scope was derived or called keyless: %v", other, err)
		}
	}
	// every profile with something live, when the scope does not narrow; the revoked one needs none
	keyOf, err = Revoker{DB: db}.Keys(ctx, []Kind{KindMission}, Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keyOf("p1"); err != nil {
		t.Error(err)
	}
	if _, err := keyOf("p2"); err != nil {
		t.Error(err)
	}
	if _, err := keyOf("p3"); err == nil {
		t.Error("a profile whose only token is revoked had its key derived")
	}
}

// A profile that was keyless when the key function looked but has a key inside the revoking transaction is not
// skipped: RevokeTx refuses (ErrRowKeyAppeared) and writes nothing.
func TestRevokeTxDoesNotSkipAProfileThatGotAKey(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("a", "p1", "owner"))
	if _, err := RowKey(ctx, db, "p1"); err != nil { // the profile now has a key row
		t.Fatal(err)
	}
	stale := func(string) ([]byte, error) { return nil, ErrNoRowKey } // what the snapshot said before it did
	refs, err := RevokeTx(ctx, db, stale, Selector{Owner: "owner"}, "x", t0)
	if !errors.Is(err, ErrRowKeyAppeared) || refs != nil {
		t.Fatalf("a stale keyless answer for a profile with a key: %v %v", refs, err)
	}
	if rv, _ := IsRevoked(ctx, db, KindMission, "a"); rv {
		t.Error("a revocation row was written")
	}
}

// The race itself: between the key snapshot and the transaction the profile gets a key and a new token. The run
// starts over and revokes the new token too, with no error.
func TestARevocationRevokesATokenOfAProfileThatGotAKeyAfterTheSnapshot(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("old", "p9", "owner")) // signed with the test key; the profile has no data key
	hooked, calls := 0, 0
	r := Revoker{DB: db, Now: func() time.Time { return t0 }}
	r.afterKeys = func() {
		hooked++
		if hooked > 1 {
			return
		}
		key, err := RowKey(ctx, db, "p9")
		if err != nil {
			t.Error(err)
			return
		}
		if err := InsertMissionToken(ctx, db, key, mission("new", "p9", "owner")); err != nil {
			t.Error(err)
		}
	}
	err := r.Run(ctx, []Kind{KindMission}, Scope{Owner: "owner"}, func(conn *sql.Conn) ([]Selector, error) {
		calls++
		return []Selector{{Profile: "p9", Owner: "owner", Kind: KindMission}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if rv, _ := IsRevoked(ctx, db, KindMission, "new"); !rv {
		t.Error("the token made after the snapshot was left unrevoked")
	}
	if calls != 2 {
		t.Errorf("the legacy callback ran %d times, want 2 (the first attempt rolled back)", calls)
	}
}
