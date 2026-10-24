package secrets

import (
	"context"
	"testing"
	"time"
)

func TestAgentRevealPolicyScopeExpiryAndRevocation(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	check := func(profile, name string, want bool) {
		t.Helper()
		got, err := CanAgentReveal(ctx, db.DB, profile, name)
		if err != nil || got != want {
			t.Fatalf("%s/%s: %v %v, want %v", profile, name, got, err, want)
		}
	}
	check("default", "password", false)
	if err := SetAgentAccess(ctx, db.DB, "default", true); err != nil {
		t.Fatal(err)
	}
	check("default", "password", false)
	grant := AccessGrant{Level: "reveal", AnyHost: true, Secrets: []string{"password", ReservedEntry, "agent-token-ci"}}
	if err := SetAgentAccessGrant(ctx, db.DB, "default", grant); err != nil {
		t.Fatal(err)
	}
	check("default", "password", true)
	check("other", "password", false)
	check("default", "other-password", false)
	check("default", ReservedEntry, false)
	check("default", "agent-token-ci", false)
	if err := AuthorizeAgentReveal(ctx, db.DB, "default", "password"); err != nil {
		t.Fatal(err)
	}
	rows, err := AgentAccessAudit(ctx, db.DB, "default")
	if err != nil || len(rows) != 1 || rows[0].Secret != "password" {
		t.Fatalf("audit: %+v %v", rows, err)
	}
	expired := time.Now().Add(-time.Minute)
	grant.ExpiresAt = &expired
	if err := SetAgentAccessGrant(ctx, db.DB, "default", grant); err != nil {
		t.Fatal(err)
	}
	check("default", "password", false)
	grant.ExpiresAt = nil
	if err := SetAgentAccessGrant(ctx, db.DB, "default", grant); err != nil {
		t.Fatal(err)
	}
	if err := SetAgentAccess(ctx, db.DB, "default", false); err != nil {
		t.Fatal(err)
	}
	check("default", "password", false)
	if err := AuthorizeAgentReveal(ctx, db.DB, "default", "password"); err == nil {
		t.Fatal("revoked reveal succeeded")
	}
}

func TestAgentLevelMigrationAndValidation(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	// A legacy database row gets Use from the migration default.
	if _, err := db.DB.Exec(`INSERT INTO profile_agent_secret_access (profile_id, enabled_at) VALUES ('default', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	g, err := GetAgentAccess(ctx, db.DB, "default")
	if err != nil || g == nil || g.Level != "use" {
		t.Fatalf("legacy grant: %+v %v", g, err)
	}
	for _, grant := range []AccessGrant{{Level: "invalid", AnyHost: true}, {Level: "reveal", Hosts: []string{"example.com"}}} {
		if err := SetAgentAccessGrant(ctx, db.DB, "default", grant); err == nil {
			t.Fatalf("invalid grant accepted: %+v", grant)
		}
	}
	g, err = GetAgentAccess(ctx, db.DB, "default")
	if err != nil || g == nil || g.Level != "use" {
		t.Fatalf("invalid mutation changed grant: %+v %v", g, err)
	}
}
