package secrets

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

const blobPrefix = "vaultenc:v1:"

// ErrPlaintextBlob is DecryptBlob's refusal of a value without the vaultenc prefix once
// RejectPlaintextBlobs is on: a writer to the database could otherwise plant plaintext where
// a ciphertext belongs.
var ErrPlaintextBlob = errors.New("secrets: stored value is not encrypted (no vaultenc prefix); refusing to treat it as a secret")

var rejectPlaintext atomic.Bool

// RejectPlaintextBlobs switches DecryptBlob from accepting an unprefixed value as plaintext (the
// rows from before encryption shipped) to refusing it. Startup turns it on once
// `secret encrypt-connections` (connections.MigrateConnectionsToVault) has left no plaintext row;
// it is off by default so that the migration itself can still read the old rows. Process-wide.
// Sealing with additional authenticated data (kv=2) is for the next vault migration.
func RejectPlaintextBlobs(on bool) { rejectPlaintext.Store(on) }

// EncryptBlob encrypts an arbitrary byte blob (e.g. a connections.data JSON
// document) under profileID's DEK and returns a self-describing string safe
// to store directly in a TEXT column.
func EncryptBlob(ctx context.Context, db *sql.DB, profileID string, plaintext []byte) (string, error) {
	dek, err := getOrCreateDEK(ctx, db, profileID)
	if err != nil {
		return "", fmt.Errorf("secrets.EncryptBlob: %w", err)
	}
	ciphertext, nonce, err := Encrypt(dek, plaintext)
	if err != nil {
		return "", fmt.Errorf("secrets.EncryptBlob: %w", err)
	}
	combined := append(nonce, ciphertext...)
	return blobPrefix + base64.StdEncoding.EncodeToString(combined), nil
}

// DecryptBlob reverses EncryptBlob. If encoded does not carry the vaultenc
// prefix (a row from before this feature shipped, not yet migrated) it is
// returned unchanged rather than erroring — callers that unmarshal JSON
// from the result get the original plaintext JSON either way. The downgrade
// is never silent, though: a stderr warning mirrors migrate.go's so an
// unmigrated plaintext row is visible instead of quietly decrypting to
// itself forever.

func DecryptBlob(ctx context.Context, db *sql.DB, profileID, encoded string) ([]byte, error) {
	if !strings.HasPrefix(encoded, blobPrefix) {
		if encoded != "" && rejectPlaintext.Load() {
			return nil, ErrPlaintextBlob
		}
		if encoded != "" {
			fmt.Fprintf(os.Stderr, "warning: secrets.DecryptBlob: %d-byte blob lacks the %q prefix — plaintext value not yet migrated; run `monoagentcli secret encrypt-connections`\n", len(encoded), blobPrefix)
		}
		return []byte(encoded), nil
	}
	dek, err := getOrCreateDEK(ctx, db, profileID)
	if err != nil {
		return nil, fmt.Errorf("secrets.DecryptBlob: %w", err)
	}
	return decryptBlobWithDEK(dek, encoded)
}

// DecryptBlobLegacy reverses a blob encrypted before per-profile keys
// existed (the shared singleton DEK, preserved read-only in
// vault_keys_legacy by migration 023). Used only by pending pre-vault
// migrations (e.g. MigrateSessionsToVault) that may still encounter data
// written under the old scheme; every other path uses DecryptBlob with the
// owning profile's own key.
func DecryptBlobLegacy(ctx context.Context, db *sql.DB, encoded string) ([]byte, error) {
	if !strings.HasPrefix(encoded, blobPrefix) {
		return []byte(encoded), nil
	}
	dek, found, err := fetchLegacyDEK(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("secrets.DecryptBlobLegacy: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("secrets.DecryptBlobLegacy: no legacy key found — nothing to migrate under it")
	}
	return decryptBlobWithDEK(dek, encoded)
}

func decryptBlobWithDEK(dek []byte, encoded string) ([]byte, error) {
	combined, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, blobPrefix))
	if err != nil {
		return nil, fmt.Errorf("secrets: decoding base64: %w", err)
	}
	const nonceSize = 12 // AES-GCM standard nonce size, matches crypto.go's gcm.NonceSize()
	if len(combined) < nonceSize {
		return nil, fmt.Errorf("secrets: encoded blob too short")
	}
	nonce, ciphertext := combined[:nonceSize], combined[nonceSize:]
	plaintext, err := Decrypt(dek, ciphertext, nonce)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	return plaintext, nil
}
