package secrets

import (
	"context"
	"testing"
)

func TestCorruptGrantListsFailClosed(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if err := SetAgentAccessGrant(ctx, db.DB, "default", AccessGrant{Level: "use", AnyHost: true, Secrets: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"secret_names", "hosts"} {
		if _, err := db.DB.Exec(`UPDATE profile_agent_secret_access SET `+col+` = ?`, `["a"`); err != nil {
			t.Fatal(err)
		}
		g, err := GetAgentAccess(ctx, db.DB, "default")
		if err == nil || g != nil {
			t.Fatalf("corrupt %s: got grant %+v err %v, want error", col, g, err)
		}
		if _, err := db.DB.Exec(`UPDATE profile_agent_secret_access SET ` + col + ` = NULL`); err != nil {
			t.Fatal(err)
		}
	}
}
