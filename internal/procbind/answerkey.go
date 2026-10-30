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

// RowKey is the key the v2 rows (tokens, bindings, revocations, host starts) are signed with, per profile.
func RowKey(ctx context.Context, db *sql.DB, profileID string) ([]byte, error) {
	if profileID == "" {
		return nil, errors.New("procbind: a row key needs a profile")
	}
	return secrets.DeriveKey(ctx, db, profileID, rowKeyPurpose)
}
