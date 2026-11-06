package secrets

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"

	"github.com/zalando/go-keyring"
)

func newBlobTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	db := testdb.Open(t)
	return db
}

func TestEncryptDecryptBlob_RoundTrip(t *testing.T) {
	db := newBlobTestDB(t)
	ctx := context.Background()
	plaintext := []byte(`{"access_token":"abc123"}`)

	encoded, err := EncryptBlob(ctx, db.DB, "default", plaintext)
	if err != nil {
		t.Fatalf("EncryptBlob: %v", err)
	}
	if encoded == string(plaintext) {
		t.Fatal("encoded blob must not equal plaintext")
	}

	got, err := DecryptBlob(ctx, db.DB, "default", encoded)
	if err != nil {
		t.Fatalf("DecryptBlob: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

func TestDecryptBlob_PassesThroughLegacyPlaintext(t *testing.T) {
	db := newBlobTestDB(t)
	ctx := context.Background()
	legacy := `{"access_token":"legacy-plaintext"}`

	got, err := DecryptBlob(ctx, db.DB, "default", legacy)
	if err != nil {
		t.Fatalf("DecryptBlob on legacy plaintext: %v", err)
	}
	if string(got) != legacy {
		t.Fatalf("got %q, want unchanged %q", got, legacy)
	}
}
