package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/monoes/mono-agent/internal/secrets"
)

// tokenSecretName is the vault entry name under which the HTTP API's
// bearer token is stored, scoped per profile like every other credential
// in this repo. Stored via the public secrets.Add (kind "secret") rather
// than a system kind — the token isn't owned by another subsystem's
// record the way a connection/session/ai_provider is, and this keeps
// internal/secrets untouched by this package.
const tokenSecretName = secrets.ReservedEntry

// tokenField is the vault_secrets field key holding the token value.
const tokenField = "token"

// ensureToken returns the profile's HTTP API bearer token, generating and
// persisting a new 32-byte random token on first use. Safe to call
// concurrently with itself for the same profile — a benign race just
// creates (and then ignores) a duplicate secret entry the first time.
func ensureToken(ctx context.Context, db *sql.DB, profileID string) (string, error) {
	if tok, err := existingToken(ctx, db, profileID); err != nil || tok != "" {
		return tok, err
	}
	tok, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("httpapi: generate token: %w", err)
	}
	// The server makes the token: the one write of it that isn't the user's own.
	if _, err := secrets.Add(secrets.AllowReservedWrite(ctx), db, profileID, "secret", tokenSecretName,
		map[string]string{tokenField: tok}, "", "", "monoagentcli httpapi bearer token (auto-generated)"); err != nil {
		return "", fmt.Errorf("httpapi: store token: %w", err)
	}
	return tok, nil
}

// existingToken returns the profile's token, or "" when it has none yet. It
// never creates one: a caller that only checks a credential must not mint
// tokens for profiles nobody asked for.
func existingToken(ctx context.Context, db *sql.DB, profileID string) (string, error) {
	entries, err := secrets.List(ctx, db, profileID)
	if err != nil {
		return "", fmt.Errorf("httpapi: list vault entries: %w", err)
	}
	for _, e := range entries {
		if e.Kind != "secret" || e.Name != tokenSecretName {
			continue
		}
		fields, _, err := secrets.DecryptFields(ctx, db, profileID, e.ID)
		if err != nil {
			return "", fmt.Errorf("httpapi: decrypt existing token: %w", err)
		}
		if tok := fields[tokenField]; tok != "" {
			return tok, nil
		}
	}
	return "", nil
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// constantTimeEqual reports whether a and b are equal, in constant time
// relative to the compared lengths — the same guard the webhook server
// uses for its auth token comparisons.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
