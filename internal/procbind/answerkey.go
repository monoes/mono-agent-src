package procbind

import (
	"context"
	"database/sql"
	"errors"

	"github.com/monoes/mono-agent/internal/secrets"
)

// Purposes of the keys derived from the profile's vault key. A purpose is part of the derivation, so a key for
// one use is not a key for another.
const (
	answerKeyPurpose = "procbind/answer/"
	rowKeyPurpose    = "procbind/rows/v2"
)

// AnswerKey is K_ans of the design: the key the binding service signs its answers with and the verifier checks
// them against. It is derived per profile and per host generation, so an answer from one start of the host
// does not verify under another, and it never leaves the process that derived it. Whoever can read the vault
// key can derive it: that is the same-user keychain adversary the design excludes.
func AnswerKey(ctx context.Context, db *sql.DB, profileID, generation string) ([]byte, error) {
	if profileID == "" || generation == "" {
		return nil, errors.New("procbind: an answer key needs a profile and a host generation")
	}
	return secrets.DeriveKey(ctx, db, profileID, answerKeyPurpose+generation)
}

// RowKeyIfExists is RowKey that never creates a key: a profile with no data key is ErrNoRowKey, and the key is read
// from the vault row on every call (not from the cache), so a profile deleted in this or another process is seen.
// Revocation and verification of retained rows use it; only a mint uses RowKey.
func RowKeyIfExists(ctx context.Context, db *sql.DB, profileID string) ([]byte, error) {
	if profileID == "" {
		return nil, errors.New("procbind: a row key needs a profile")
	}
	key, err := secrets.DeriveKeyIfExists(ctx, db, profileID, rowKeyPurpose)
	if errors.Is(err, secrets.ErrNoProfileKey) {
		return nil, ErrNoRowKey
	}
	return key, err
}

// AnswerKeyIfExists is AnswerKey that never creates a data key: a profile with none is ErrNoRowKey. The binding service
// and every verifier use it, so neither can bring back the key of a deleted profile; it is read from the vault row on
// every call.
func AnswerKeyIfExists(ctx context.Context, db *sql.DB, profileID, generation string) ([]byte, error) {
	if profileID == "" || generation == "" {
		return nil, errors.New("procbind: an answer key needs a profile and a host generation")
	}
	key, err := secrets.DeriveKeyIfExists(ctx, db, profileID, answerKeyPurpose+generation)
	if errors.Is(err, secrets.ErrNoProfileKey) {
		return nil, ErrNoRowKey
	}
	return key, err
}

// RowKey is the key the v2 rows (tokens, bindings, revocations, host starts) are signed with, per profile.
func RowKey(ctx context.Context, db *sql.DB, profileID string) ([]byte, error) {
	if profileID == "" {
		return nil, errors.New("procbind: a row key needs a profile")
	}
	return secrets.DeriveKey(ctx, db, profileID, rowKeyPurpose)
}
