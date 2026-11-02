package agenttokens

import (
	"context"
	"testing"
	"time"
)

func vaultKeyRows(t *testing.T, s *Store, profile string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM vault_keys WHERE profile_id = ?`, profile).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A deleted profile (its vault key row gone, its v2 token rows retained) does not get a key made again by a later
// revocation, and the revocation still works: its retained token is inert, not revoked, and not an error.
func TestARevocationDoesNotRecreateTheKeyOfADeletedProfile(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tok, _, err := s.CreateMission(ctx, "default", missionB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	v2Mission(t, s, tok.ID, "default", "run-a")
	v2Mission(t, s, "v2-gone", "gone", "run-a") // same owner, a profile that is deleted below
	if _, err := s.db.Exec(`DELETE FROM vault_keys WHERE profile_id = 'gone'`); err != nil {
		t.Fatal(err)
	}
	if vaultKeyRows(t, s, "gone") != 0 {
		t.Fatal("setup: the deleted profile still has a key row")
	}
	if _, err := s.RevokeOwner(ctx, "run-a"); err != nil {
		t.Fatalf("a deleted profile's retained token broke the revocation: %v", err)
	}
	if vaultKeyRows(t, s, "gone") != 0 {
		t.Error("the revocation created a vault key for the deleted profile")
	}
	if !v2Revoked(t, s, tok.ID) {
		t.Error("the live profile's v2 token was not revoked")
	}
	if v2Revoked(t, s, "v2-gone") {
		t.Error("a revocation row was written for the deleted profile")
	}
}

// Only the affected profile's key is derived: revoking in one profile never touches another profile's key row.
func TestARevocationDerivesOnlyTheAffectedProfilesKey(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tok, _, err := s.CreateMission(ctx, "default", missionB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	v2Mission(t, s, tok.ID, "default", "run-a")
	v2Mission(t, s, "v2-other", "other", "run-b")
	if _, err := s.db.Exec(`DELETE FROM vault_keys WHERE profile_id = 'other'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(ctx, "default", tok.ID); err != nil {
		t.Fatal(err)
	}
	if vaultKeyRows(t, s, "other") != 0 {
		t.Error("revoking in one profile created a vault key for another")
	}
	if !v2Revoked(t, s, tok.ID) {
		t.Error("the v2 token was not revoked")
	}
}
