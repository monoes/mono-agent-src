package secrets

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
)

// ErrNoProfileKey is DeriveKeyIfExists' answer for a profile that has no data key (it never had one, or it was deleted).
var ErrNoProfileKey = errors.New("secrets: the profile has no data key")

// DeriveKeyIfExists is DeriveKey that never creates anything: no wrapping key in the keychain, no vault_keys row,
// no cache entry. It reads the profile's vault_keys row and unwraps it on every call (it does not use the
// per-handle cache, so a row removed or replaced by this or another process is seen at once), and returns
// ErrNoProfileKey when the profile has no row. A row whose wrapping key cannot be had is an error, not "no key".
// It is for the paths that must not bring a deleted profile's key back: revocation and verification of
// retained rows.
func DeriveKeyIfExists(ctx context.Context, db *sql.DB, profileID, purpose string) ([]byte, error) {
	if purpose == "" {
		return nil, errors.New("secrets: a derived key needs a purpose")
	}
	dek, err := peekDEK(ctx, db, profileID)
	if err != nil {
		return nil, err
	}
	return deriveFrom(dek, purpose), nil
}

func deriveFrom(dek []byte, purpose string) []byte {
	m := hmac.New(sha256.New, dek)
	m.Write([]byte("monoagent/derived-key/v1/" + purpose))
	return m.Sum(nil)
}

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
	return deriveFrom(dek, purpose), nil
}
