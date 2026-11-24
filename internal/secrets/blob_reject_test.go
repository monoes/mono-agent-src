package secrets

import (
	"context"
	"errors"
	"testing"
)

func TestDecryptBlob_RejectsPlaintextOnceSwitchedOn(t *testing.T) {
	db := newBlobTestDB(t)
	ctx := context.Background()
	RejectPlaintextBlobs(true)
	t.Cleanup(func() { RejectPlaintextBlobs(false) })

	if _, err := DecryptBlob(ctx, db.DB, "default", `{"access_token":"planted"}`); !errors.Is(err, ErrPlaintextBlob) {
		t.Fatalf("plaintext accepted: %v", err)
	}
	enc, err := EncryptBlob(ctx, db.DB, "default", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecryptBlob(ctx, db.DB, "default", enc); err != nil || string(got) != "x" {
		t.Fatalf("a real blob: %q %v", got, err)
	}
	if got, err := DecryptBlob(ctx, db.DB, "default", ""); err != nil || len(got) != 0 {
		t.Fatalf("an empty value: %q %v", got, err)
	}
}
