package procbind

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// A turn's v2 tokens and their bindings are written in ONE write transaction (design 3c step 4), after the turn's
// process exists (so its pid and start stamp are known) and before anything is delivered. Inside the transaction the
// caller's Check re-reads the authoritative state (the session row exists and its current turn is this turn; for a
// mission child, its attempt is current) and aborts if it is not. Because every revoke and delete is also a write
// transaction and SQLite serialises writers, either this runs first (its rows exist when the delete's RevokeTx runs
// and the selector finds them) or the delete runs first (Check fails and nothing is inserted).

// MintToken is one token to write: its id, the hash of its secret, and what its kind needs.
type MintToken struct {
	Kind Kind
	ID   string
	// Hash is the stored hash of the secret (TokenHash): the secret itself is never stored.
	Hash string
	// Scope is the binding's scope, the string the verifier asks about.
	Scope   string
	Expires time.Time
	// session tokens
	ConversationID, TurnID string
	// mission tokens
	Owner, Role, Actor string
	Session, Task      int64
}

// MintRequest is one launch's tokens.
type MintRequest struct {
	Profile        string
	WorkerPID      int
	WorkerStamp    string
	HostGeneration string
	Now            time.Time
	Tokens         []MintToken
	// Check runs inside the write transaction, on its connection, before anything is inserted; an error aborts the
	// mint and nothing is written.
	Check func(ctx context.Context, x DBTX) error
}

// ErrMintRefused is a mint that Check refused; the cause is wrapped.
var ErrMintRefused = errors.New("procbind: the launch is no longer current")

// MintTx writes the tokens and their bindings in one BEGIN IMMEDIATE transaction on its own connection. The row key
// must be had before the call (deriving it can write). Every row is written or none: an id already in use, a failed
// Check or any database error leaves the database as it was.
func MintTx(ctx context.Context, db *sql.DB, key []byte, r MintRequest) error {
	if r.Profile == "" || r.WorkerPID <= 1 || r.WorkerStamp == "" || r.HostGeneration == "" || len(r.Tokens) == 0 || r.Now.IsZero() {
		return errors.New("procbind: a mint needs a profile, a worker pid and stamp, a host generation, a time and tokens")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := beginImmediate(ctx, conn); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if r.Check != nil {
		if err := r.Check(ctx, conn); err != nil {
			return fmt.Errorf("%w: %v", ErrMintRefused, err)
		}
	}
	seen := map[string]bool{}
	for _, t := range r.Tokens {
		if seen[t.ID] {
			return ErrIDInUse
		}
		seen[t.ID] = true
		switch t.Kind {
		case KindSession:
			err = InsertSessionToken(ctx, conn, key, SessionTokenRow{ID: t.ID, ProfileID: r.Profile, TokenHash: t.Hash, ConversationID: t.ConversationID, TurnID: t.TurnID, CreatedAt: r.Now, ExpiresAt: t.Expires})
		case KindMission:
			err = InsertMissionToken(ctx, conn, key, MissionTokenRow{ID: t.ID, ProfileID: r.Profile, TokenHash: t.Hash, Owner: t.Owner, Session: t.Session, Task: t.Task, Role: t.Role, Actor: t.Actor, CreatedAt: r.Now, ExpiresAt: t.Expires})
		default:
			err = ErrBadSelector
		}
		if err != nil {
			return err
		}
		if err := InsertBinding(ctx, conn, key, BindingRow{Kind: t.Kind, TokenID: t.ID, ProfileID: r.Profile, Scope: t.Scope, WorkerPID: r.WorkerPID, WorkerStamp: r.WorkerStamp, HostGeneration: r.HostGeneration, CreatedAt: r.Now, ExpiresAt: t.Expires}); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

// NewSecret returns a new random token secret with the given prefix, and its stored hash. The secret is 256 random
// bits (base64url); the hash is the SHA-256 of the whole secret in hex, the scheme the legacy tokens use.
func NewSecret(prefix string) (secret, hash string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("procbind: generating a token: %w", err)
	}
	secret = prefix + base64.RawURLEncoding.EncodeToString(b[:])
	return secret, HashSecret(secret), nil
}

// HashSecret is the stored form of a token secret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
