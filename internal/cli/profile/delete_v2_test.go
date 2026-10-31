package profile

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/storage"
)

func v2ProfileTokens(t *testing.T, dbPath, id string) (*storage.Database, string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, db.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := procbind.InsertMissionToken(ctx, db.DB, key, procbind.MissionTokenRow{ID: "v2-m", ProfileID: id, TokenHash: "h-m", Owner: "o", Session: 7, Task: 3, Role: "worker", Actor: "a", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := procbind.InsertSessionToken(ctx, db.DB, key, procbind.SessionTokenRow{ID: "v2-s", ProfileID: id, TokenHash: "h-s", ConversationID: "c", TurnID: "t", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return db, id
}

// Deleting a profile revokes its v2 tokens (both families) in the delete's transaction; the rows stay (retainedOnDelete).
func TestDeleteProfileRevokesItsV2Tokens(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	db, _ := v2ProfileTokens(t, dbPath, id)
	ctx := context.Background()
	if _, err := deleteProfile(ctx, db.DB, id); err != nil {
		t.Fatal(err)
	}
	for kind, tok := range map[procbind.Kind]string{procbind.KindMission: "v2-m", procbind.KindSession: "v2-s"} {
		if rv, err := procbind.IsRevoked(ctx, db.DB, kind, tok); err != nil || !rv {
			t.Errorf("%s token %s is still live after the profile delete (%v)", kind, tok, err)
		}
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_tokens_v2 WHERE profile_id = ?`, id).Scan(&n); err != nil || n != 1 {
		t.Errorf("the v2 token row should stay (%d, %v)", n, err)
	}
}

// If the v2 revocation cannot be written the profile delete is rolled back.
func TestDeleteProfileRollsBackWhenTheV2RevocationFails(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	db, _ := v2ProfileTokens(t, dbPath, id)
	if _, err := db.DB.Exec(`CREATE TRIGGER zz_refuse BEFORE INSERT ON token_revocations_v2 BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteProfile(context.Background(), db.DB, id); err == nil {
		t.Fatal("the profile was deleted although the v2 revocation failed")
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM profiles WHERE id = ?`, id).Scan(&n); err != nil || n != 1 {
		t.Errorf("the profile is gone (%d, %v)", n, err)
	}
}

// Deleting a second profile does not bring the first deleted profile's vault key back: its retained v2 token rows
// are not the second delete's business.
func TestDeletingAnotherProfileDoesNotRecreateADeletedProfilesKey(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	first := createTestProfile(t, dbPath, "first")
	second := createTestProfile(t, dbPath, "second")
	db, _ := v2ProfileTokens(t, dbPath, first)
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, db.DB, second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := procbind.InsertMissionToken(ctx, db.DB, key, procbind.MissionTokenRow{ID: "v2-second", ProfileID: second, TokenHash: "h", Owner: "o", Session: 7, Task: 3, Role: "worker", Actor: "a", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteProfile(ctx, db.DB, first); err != nil {
		t.Fatal(err)
	}
	rows := func(p string) int {
		var n int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM vault_keys WHERE profile_id = ?`, p).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if rows(first) != 0 {
		t.Skip("the profile delete leaves the key row here; nothing to recreate")
	}
	if _, err := deleteProfile(ctx, db.DB, second); err != nil {
		t.Fatalf("deleting the second profile: %v", err)
	}
	if rows(first) != 0 {
		t.Error("deleting another profile brought the first deleted profile's vault key back")
	}
}

// After a profile delete the handle that cached the profile's data key no longer serves it: the retained v2 row,
// signed under the old key, is refused, and the read-only derivation says there is no key.
func TestAfterAProfileDeleteTheCachedKeyNoLongerVerifiesItsRetainedRows(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	oldKey, err := procbind.RowKey(ctx, db.DB, id) // cached on this handle
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	row := procbind.MissionTokenRow{ID: "v2-kept", ProfileID: id, TokenHash: "h", Owner: "o", Session: 7, Task: 3, Role: "worker", Actor: "a", CreatedAt: now.Truncate(time.Millisecond), ExpiresAt: now.Add(time.Hour).Truncate(time.Millisecond)}
	if err := procbind.InsertMissionToken(ctx, db.DB, oldKey, row); err != nil {
		t.Fatal(err)
	}
	if _, err := procbind.GetMissionToken(ctx, db.DB, oldKey, id, "v2-kept"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := deleteProfile(ctx, db.DB, id); err != nil {
		t.Fatal(err)
	}
	if _, err := procbind.RowKeyIfExists(ctx, db.DB, id); !errors.Is(err, procbind.ErrNoRowKey) {
		t.Errorf("RowKeyIfExists after the delete: %v", err)
	}
	newKey, err := procbind.RowKey(ctx, db.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(newKey, oldKey) {
		t.Fatal("the deleted profile's key was still served from this handle's cache")
	}
	if _, err := procbind.GetMissionToken(ctx, db.DB, newKey, id, "v2-kept"); !errors.Is(err, procbind.ErrRowTampered) {
		t.Errorf("the retained row verifies after the profile delete: %v", err)
	}
}
