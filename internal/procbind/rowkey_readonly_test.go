package procbind

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
)

func vaultRowsOf(t *testing.T, db *sql.DB, profile string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vault_keys WHERE profile_id = ?`, profile).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// deleteProfileKeys is what a profile delete does to its key: the vault row goes with the profile's rows and the
// wrapping key is removed from the keychain (which also drops the cached unwrapped key).
func deleteProfileKeys(t *testing.T, db *sql.DB, profile string) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM vault_keys WHERE profile_id = ?`, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.DeleteProfileKEK(profile); err != nil {
		t.Fatal(err)
	}
}

// RowKeyIfExists reads the key and creates nothing: a profile without a data key is ErrNoRowKey, with no vault row,
// and a live profile gets the same key RowKey gives.
func TestRowKeyIfExistsNeverCreatesAKey(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	if _, err := RowKeyIfExists(ctx, db, "fresh"); !errors.Is(err, ErrNoRowKey) {
		t.Fatalf("a profile that never had a key: %v", err)
	}
	if vaultRowsOf(t, db, "fresh") != 0 {
		t.Fatal("RowKeyIfExists created a vault row")
	}
	want, err := RowKey(ctx, db, "live")
	if err != nil {
		t.Fatal(err)
	}
	got, err := RowKeyIfExists(ctx, db, "live")
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("a live profile's key differs from RowKey: %v", err)
	}
}

// A deleted profile's key is not returned from the cache and not brought back, on the handle that cached it: the
// retained row, signed under the old key, is refused by anything that derives afterwards, and revocation skips it.
func TestADeletedProfilesKeyIsNeitherServedFromTheCacheNorRecreated(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	oldKey, err := RowKey(ctx, db, "gone") // warms the cache of this handle
	if err != nil {
		t.Fatal(err)
	}
	m := mission("m1", "gone", "owner")
	if err := InsertMissionToken(ctx, db, oldKey, m); err != nil {
		t.Fatal(err)
	}
	if got, err := GetMissionToken(ctx, db, oldKey, "gone", "m1"); err != nil || got != m {
		t.Fatalf("setup: %v", err)
	}
	deleteProfileKeys(t, db, "gone")

	// the read-only derivation sees the deletion at once and creates nothing
	if _, err := RowKeyIfExists(ctx, db, "gone"); !errors.Is(err, ErrNoRowKey) {
		t.Errorf("RowKeyIfExists after the delete: %v", err)
	}
	if vaultRowsOf(t, db, "gone") != 0 {
		t.Error("a key was created for the deleted profile")
	}
	// revocation skips the retained token and creates nothing
	if _, err := RevokeTx(ctx, db, mustKeys(t, db, Scope{Profile: "gone"}), Selector{Profile: "gone", All: true}, "x", t0); err != nil {
		t.Fatal(err)
	}
	if rv, _ := IsRevoked(ctx, db, KindMission, "m1"); rv {
		t.Error("a revocation row was written for a deleted profile")
	}
	if vaultRowsOf(t, db, "gone") != 0 {
		t.Error("revocation created a key for the deleted profile")
	}
	// the cached key is gone: whoever asks the same handle for the profile's key now gets a different one, and the
	// retained row does not verify under it
	newKey, err := RowKey(ctx, db, "gone")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(newKey, oldKey) {
		t.Fatal("the deleted profile's key was served from the cache")
	}
	if _, err := GetMissionToken(ctx, db, newKey, "gone", "m1"); !errors.Is(err, ErrRowTampered) {
		t.Errorf("the retained row verifies under the key of a deleted profile: %v", err)
	}
}

func mustKeys(t *testing.T, db *sql.DB, sc Scope) func(string) ([]byte, error) {
	t.Helper()
	keyOf, err := Revoker{DB: db}.Keys(context.Background(), []Kind{KindMission, KindSession}, sc)
	if err != nil {
		t.Fatal(err)
	}
	return keyOf
}
