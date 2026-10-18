package secrets

import (
	"context"
	"errors"
	"testing"
)

// httpapi-token is the HTTP API's bearer: no write reaches it unless its
// context allows it (the server's own, and the user's commands). Adding,
// renaming to, changing, deleting and importing it are refused otherwise,
// in any case and with spaces around the name; every other entry stays
// writable (#845 security review).
func TestOnlyAnAllowedWriteReachesTheHTTPAPIToken(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	allowed := AllowReservedWrite(ctx)
	token, err := Add(allowed, db.DB, "default", "secret", ReservedEntry, map[string]string{"token": "server-made"}, "", "", "")
	if err != nil {
		t.Fatalf("the server's own write: %v", err)
	}
	other, err := Add(ctx, db.DB, "default", "secret", "openai", map[string]string{"secret": "sk-1"}, "", "", "")
	if err != nil {
		t.Fatalf("another entry: %v", err)
	}
	export, _, _, err := Export(allowed, db.DB, "default", "passphrase-1")
	if err != nil {
		t.Fatal(err)
	}

	renamed := " HTTPAPI-token "
	for what, write := range map[string]func() error{
		"add it": func() error {
			_, err := Add(ctx, db.DB, "default", "secret", ReservedEntry, map[string]string{"token": "agent-made"}, "", "", "")
			return err
		},
		"add it in other case": func() error {
			_, err := Add(ctx, db.DB, "default", "secret", renamed, map[string]string{"token": "agent-made"}, "", "", "")
			return err
		},
		"rename another to it": func() error { return Update(ctx, db.DB, "default", other, &renamed, nil, nil, nil, nil) },
		"change it": func() error {
			return Update(ctx, db.DB, "default", token, nil, nil, nil, nil, map[string]string{"token": "agent-made"})
		},
		"delete it":         func() error { return Delete(ctx, db.DB, "default", token) },
		"delete it, linked": func() error { return DeleteCascade(ctx, db.DB, "default", token) },
	} {
		if err := write(); !errors.Is(err, ErrReservedEntry) {
			t.Errorf("%s: %v, want ErrReservedEntry", what, err)
		}
	}
	// An import into a profile without one leaves it out.
	if imported, _, err := Import(ctx, db.DB, "work", "passphrase-1", export, nil, nil); err != nil || imported != 1 {
		t.Errorf("an import: %d imported, %v; want only openai", imported, err)
	}
	if entries, _ := List(ctx, db.DB, "work"); len(entries) != 1 || entries[0].Name != "openai" {
		t.Errorf("the imported entries: %+v", entries)
	}

	fields, _, err := DecryptFields(ctx, db.DB, "default", token)
	if err != nil || fields["token"] != "server-made" {
		t.Errorf("the token after the refusals: %v, %v", fields, err)
	}
	if err := Update(ctx, db.DB, "default", other, nil, nil, nil, nil, map[string]string{"secret": "sk-2"}); err != nil {
		t.Errorf("another entry stays writable: %v", err)
	}
	if err := Update(allowed, db.DB, "default", token, nil, nil, nil, nil, map[string]string{"token": "user-made"}); err != nil {
		t.Errorf("the user's own change: %v", err)
	}
}
