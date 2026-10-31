package ai

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/procbind"
)

func vaultRows(t *testing.T, s *AIStore, profile string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM vault_keys WHERE profile_id = ?`, profile).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A later conversation delete does not bring a deleted profile's vault key back. The deleted profile keeps its v2
// token rows (they are insert-only) but not its key row; the delete in another profile neither derives that key nor
// fails because of it, and it still revokes its own profile's tokens.
func TestDeleteConversationDoesNotRecreateADeletedProfilesKey(t *testing.T) {
	s, db := newChatEventStore(t)
	ctx := context.Background()
	gone := "deleted-profile"
	key, err := procbind.RowKey(ctx, db, gone)
	if err != nil {
		t.Fatal(err)
	}
	if err := procbind.InsertSessionToken(ctx, db, key, procbind.SessionTokenRow{ID: "v2-gone-s", ProfileID: gone, TokenHash: "h1", ConversationID: "c-gone", TurnID: "t", CreatedAt: v2Created, ExpiresAt: v2Expires}); err != nil {
		t.Fatal(err)
	}
	if err := procbind.InsertMissionToken(ctx, db, key, procbind.MissionTokenRow{ID: "v2-gone-m", ProfileID: gone, TokenHash: "h2", Owner: "o", Session: 1, Task: 1, Role: "w", Actor: "a", CreatedAt: v2Created, ExpiresAt: v2Expires}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM vault_keys WHERE profile_id = ?`, gone); err != nil {
		t.Fatal(err)
	}
	c, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	v2SessionToken(t, db, nil, "v2-live", c.ID)
	before := vaultRows(t, s, gone)
	if err := s.DeleteConversation(c.ID, "p1"); err != nil {
		t.Fatalf("a deleted profile's retained rows broke an unrelated delete: %v", err)
	}
	if vaultRows(t, s, gone) != before || before != 0 {
		t.Errorf("the deleted profile's key row came back (%d rows)", vaultRows(t, s, gone))
	}
	if !v2SessionRevoked(t, db, "v2-live") {
		t.Error("the live profile's token was not revoked")
	}
	if rv, _ := procbind.IsRevoked(ctx, db, procbind.KindSession, "v2-gone-s"); rv {
		t.Error("a revocation row was written for the deleted profile")
	}
}
