package procbind

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A binding written as one kind of token does not verify as the other (the table has no kind column).
func TestABindingIsBoundToItsTokenKind(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	b := BindingRow{Kind: KindSession, TokenID: "x", ProfileID: "p1", Scope: "sc", WorkerPID: 4242, WorkerStamp: "st", HostGeneration: "g", CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
	if err := InsertBinding(ctx, db, storeKey, b); err != nil {
		t.Fatal(err)
	}
	if got, err := GetBinding(ctx, db, storeKey, KindSession, "p1", "x"); err != nil || got != b {
		t.Fatalf("read as its own kind: %+v %v", got, err)
	}
	if _, err := GetBinding(ctx, db, storeKey, KindMission, "p1", "x"); !errors.Is(err, ErrRowTampered) {
		t.Errorf("read as the other kind: %v", err)
	}
	noKind := b
	noKind.Kind = ""
	noKind.TokenID = "y"
	if err := InsertBinding(ctx, db, storeKey, noKind); err == nil {
		t.Error("a binding without a kind was accepted")
	}
}

func TestSelectorsThatWouldSelectNothingSilentlyAreRefused(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("m1", "p1", "owner"))
	for name, sel := range map[string]Selector{
		"an unknown kind":                 {Profile: "p1", All: true, Kind: "x"},
		"a session with the mission kind": {Profile: "p1", Session: "conv", Kind: KindMission},
		"a turn with the mission kind":    {Profile: "p1", TokenID: "m1", Turn: "t", Kind: KindMission},
		"a turn without its session":      {Profile: "p1", All: true, Turn: "t"},
		"a registry generation":           {Profile: "p1", All: true, Generation: Generation{1, 1}},
	} {
		if refs, err := RevokeTx(ctx, db, keyFor(nil), sel, "x", t0); !errors.Is(err, ErrBadSelector) || refs != nil {
			t.Errorf("%s: %v %v", name, refs, err)
		}
	}
}

// A revocation row that names another profile (a misplaced or forged one) still makes the token count as revoked
// and RevokeTx does not try to write the real one: the table's key is the kind and the id.
func TestARevocationRowOfAnotherProfileStillDenies(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("m1", "p1", "owner"))
	mustExec(t, db, `INSERT INTO token_revocations_v2 (token_kind, token_id, profile_id, reason, created_at, mac) VALUES ('mission', 'm1', 'p2', 'x', ?, 'misplaced')`, t0.Format(tsLayout))
	refs, err := RevokeTx(ctx, db, keyFor(nil), Selector{Profile: "p1", Owner: "owner"}, "x", t0)
	if err != nil || len(refs) != 0 {
		t.Fatalf("the token is already revoked by a row that exists: %v %v", refs, err)
	}
	if rv, err := IsRevoked(ctx, db, KindMission, "m1"); err != nil || !rv {
		t.Errorf("a revocation row of another profile did not deny: %v %v", rv, err)
	}
}

// If the key of a later profile cannot be had, nothing is written, not even for the earlier profiles.
func TestAFailingKeyWritesNothingEvenOutsideATransaction(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("a", "p1", "owner"))
	mustInsertMission(t, db, mission("b", "p2", "owner"))
	keys := func(profile string) ([]byte, error) {
		if profile == "p2" {
			return nil, errors.New("no key for p2")
		}
		return storeKey, nil
	}
	if _, err := RevokeTx(ctx, db, keys, Selector{Owner: "owner"}, "x", t0); err == nil {
		t.Fatal("the revoke succeeded without p2's key")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM token_revocations_v2`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d revocation rows were written before the failure (%v)", n, err)
	}
}

// An owner is matched exactly or as a prefix before a slash: SQL wildcards in it mean nothing.
func TestOwnerMatchingIgnoresSQLWildcards(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("lit", "p1", "a%b"))
	mustInsertMission(t, db, mission("other", "p1", "aXb"))
	mustInsertMission(t, db, mission("under", "p1", "a_c"))
	mustInsertMission(t, db, mission("abc", "p1", "abc"))
	refs, err := RevokeTx(ctx, db, keyFor(nil), Selector{Profile: "p1", Owner: "a%b"}, "x", t0)
	if err != nil || len(refs) != 1 || refs[0].TokenID != "lit" {
		t.Fatalf("a%%b: %v %v", refs, err)
	}
	refs, err = RevokeTx(ctx, db, keyFor(nil), Selector{Profile: "p1", Owner: "a_c"}, "x", t0)
	if err != nil || len(refs) != 1 || refs[0].TokenID != "under" {
		t.Fatalf("a_c: %v %v", refs, err)
	}
}

// A host row returns the precision it is stored with, so what Insert returns equals what is read back.
func TestInsertHostReturnsTheStoredPrecision(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 30, 12, 0, 0, 123456789, time.UTC)
	row, err := InsertHost(ctx, db, storeKey, "p1", "gen", at)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CurrentHost(ctx, db, storeKey, "p1")
	if err != nil || got != row {
		t.Errorf("inserted %+v, read back %+v (%v)", row, got, err)
	}
}

// A getter returns no data with an error, even when the row was readable but not genuine.
func TestGettersReturnNothingWithAnError(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("m1", "p1", "owner"))
	mustInsertSession(t, db, session("s1", "p1", "conv", "turn"))
	if err := InsertBinding(ctx, db, storeKey, BindingRow{Kind: KindMission, TokenID: "m1", ProfileID: "p1", Scope: "sc", WorkerPID: 4242, WorkerStamp: "st", HostGeneration: "g", CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	other := bytesOf(9)
	if r, err := GetMissionToken(ctx, db, other, "p1", "m1"); err == nil || r != (MissionTokenRow{}) {
		t.Errorf("mission: %+v %v", r, err)
	}
	if r, err := GetSessionToken(ctx, db, other, "p1", "s1"); err == nil || r != (SessionTokenRow{}) {
		t.Errorf("session: %+v %v", r, err)
	}
	if r, err := GetBinding(ctx, db, other, KindMission, "p1", "m1"); err == nil || r != (BindingRow{}) {
		t.Errorf("binding: %+v %v", r, err)
	}
	if r, err := GetMissionToken(ctx, db, other, "p1", "absent"); err == nil || r != (MissionTokenRow{}) {
		t.Errorf("absent mission: %+v %v", r, err)
	}
}
