package secrets

import (
	"context"
	"testing"
)

func TestGrantDroppedWhenGrantedSecretValueChanges(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	id, err := Add(ctx, db.DB, "default", "secret", "api", map[string]string{"v": "1"}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, db.DB, "default", "secret", "other", map[string]string{"v": "1"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	grant := func(names ...string) {
		t.Helper()
		if err := SetAgentAccessGrant(ctx, db.DB, "default", AccessGrant{AnyHost: true, Secrets: names}); err != nil {
			t.Fatal(err)
		}
	}
	current := func() *AccessGrant {
		t.Helper()
		g, err := GetAgentAccess(ctx, db.DB, "default")
		if err != nil {
			t.Fatal(err)
		}
		return g
	}

	// A notes-only edit keeps the grant.
	n := "note"
	grant("api", "other")
	if err := Update(ctx, db.DB, "default", id, nil, nil, nil, &n, nil); err != nil {
		t.Fatal(err)
	}
	if g := current(); g == nil || len(g.Secrets) != 2 {
		t.Fatalf("notes edit changed grant: %+v", g)
	}
	// A value edit drops only that secret.
	if err := Update(ctx, db.DB, "default", id, nil, nil, nil, nil, map[string]string{"v": "2"}); err != nil {
		t.Fatal(err)
	}
	if g := current(); g == nil || len(g.Secrets) != 1 || g.Secrets[0] != "other" {
		t.Fatalf("after value edit: %+v", g)
	}
	// Dropping the last named secret removes the grant, never widens it.
	grant("api")
	if err := Update(ctx, db.DB, "default", id, nil, nil, nil, nil, map[string]string{"v": "3"}); err != nil {
		t.Fatal(err)
	}
	if g := current(); g != nil {
		t.Fatalf("grant survived: %+v", g)
	}
	// Delete, then re-add under the same name, drops the grant.
	grant("api")
	if err := Delete(ctx, db.DB, "default", id); err != nil {
		t.Fatal(err)
	}
	if g := current(); g != nil {
		t.Fatalf("grant survived delete: %+v", g)
	}
	grant("api")
	if _, err := Add(ctx, db.DB, "default", "secret", "api", map[string]string{"v": "9"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if g := current(); g != nil {
		t.Fatalf("grant survived re-add: %+v", g)
	}
	// The blanket default grant is unchanged by a value edit.
	if err := SetAgentAccess(ctx, db.DB, "default", true); err != nil {
		t.Fatal(err)
	}
	id2, _ := Add(ctx, db.DB, "default", "secret", "third", map[string]string{"v": "1"}, "", "", "")
	if err := Update(ctx, db.DB, "default", id2, nil, nil, nil, nil, map[string]string{"v": "2"}); err != nil {
		t.Fatal(err)
	}
	if current() == nil {
		t.Fatal("blanket grant was dropped")
	}
}
