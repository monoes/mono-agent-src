package secrets

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
)

// DeriveKey returns a 32-byte key for purpose, derived from the profile's vault
// data key, which only opens with the profile's key in the OS keychain (or the
// file keyring that stands in for it): nothing that reads the database alone
// holds it, so a record in the database that carries a MAC under this key was
// made by something that had the keychain, not by a row write. It is for
// authenticating the app's own records; it is no boundary against a process
// that can read the user's keychain as the user.
func DeriveKey(ctx context.Context, db *sql.DB, profileID, purpose string) ([]byte, error) {
	if purpose == "" {
		return nil, errors.New("secrets: a derived key needs a purpose")
	}
	dek, err := getOrCreateDEK(ctx, db, profileID)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, dek)
	m.Write([]byte("monoagent/derived-key/v1/" + purpose))
	return m.Sum(nil), nil
}
