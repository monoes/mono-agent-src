package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// Positive approval of a run's children (#1427). The engine records, per execution, whether the run is approved
// (migration 160, workflow_run_tokens) and hands the processes its nodes start only a random token, in EnvRunToken.
// A gate that must tell an approved workflow's child from an unapproved one looks the token up (RunTokenApproved):
// no row, an unapproved row, an ended or expired row or a store that cannot be read all mean held, so a child that
// drops or forges the token fails closed. The database keeps only the token's hash; the token itself exists in the
// children's environment and in the run's context, and is never logged.
//
// The limit: the token is a bearer credential. A process of the same user can read it from a live child's
// environment (/proc/<pid>/environ) and present it until the run ends, or write the database and insert an
// approved row of its own. That is the limit the operator package documents for every same-user mechanism; what
// this closes is a child, or a parent chain, that merely drops, edits or forges the environment it was handed.
// What the run stores of its steps is masked of the token (maskValues), and a token is never logged. A token whose
// engine crashed is ended with its execution at the next issue, else it lapses after 48 hours (a resumed run
// gets a fresh one).

// EnvRunToken carries a run's token to the processes its nodes start.
const EnvRunToken = "MONOAGENT_RUN_TOKEN"

// The clock is the database's, so a skewed process clock cannot keep a token alive. A token whose execution never
// ended it (a crashed engine) lapses after runTokenLifetime; a run that goes on longer is held, the closed side.
const (
	sqlNow           = `strftime('%Y-%m-%dT%H:%M:%SZ','now')`
	runTokenLifetime = `strftime('%Y-%m-%dT%H:%M:%SZ','now','+48 hours')`
	runTokenSweepAge = `strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 day')`
)

// HashRunToken is what the store keeps of a token.
func HashRunToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// IssueRunToken records an execution as approved or not and returns its token (256 random bits). Rows of runs that
// ended or lapsed a day ago are swept, so the table does not grow without bound. An error means no token: the run's
// children carry none and are held.
func IssueRunToken(ctx context.Context, db *sql.DB, executionID string, approved bool) (string, error) {
	if db == nil {
		return "", errors.New("run token: no database")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("run token: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	flag := 0
	if approved {
		flag = 1
	}
	// A run left open (a crashed engine) ends with its execution; a store without executions has none to look at.
	_, _ = db.ExecContext(ctx, `UPDATE workflow_run_tokens SET ended_at = `+sqlNow+` WHERE ended_at IS NULL AND execution_id IN
		(SELECT id FROM workflow_executions WHERE status IN ('SUCCESS','SUCCESS_WITH_ERRORS','FAILED','CANCELLED','REJECTED'))`)
	_, _ = db.ExecContext(ctx, `DELETE FROM workflow_run_tokens WHERE (ended_at IS NOT NULL AND ended_at < `+runTokenSweepAge+`) OR expires_at < `+runTokenSweepAge)
	if _, err := db.ExecContext(ctx, `INSERT INTO workflow_run_tokens (token_hash, execution_id, approved, created_at, expires_at)
		VALUES (?, ?, ?, `+sqlNow+`, `+runTokenLifetime+`)`, HashRunToken(token), executionID, flag); err != nil {
		return "", fmt.Errorf("run token: %w", err)
	}
	return token, nil
}

// EndRunToken ends the token's execution: the token is dead from now on.
func EndRunToken(ctx context.Context, db *sql.DB, token string) error {
	if db == nil {
		return errors.New("run token: no database")
	}
	_, err := db.ExecContext(ctx, `UPDATE workflow_run_tokens SET ended_at = `+sqlNow+` WHERE token_hash = ? AND ended_at IS NULL`, HashRunToken(token))
	return err
}

// RunTokenApproved reports whether token belongs to an approved execution that is still going. Anything else is
// false, and a store that cannot be read is false with the error.
func RunTokenApproved(ctx context.Context, db *sql.DB, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	if db == nil {
		return false, errors.New("run token: no database")
	}
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_run_tokens WHERE token_hash = ? AND approved = 1 AND ended_at IS NULL AND expires_at > `+sqlNow,
		HashRunToken(token)).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

type runTokenKey struct{}

// WithRunToken carries a run's token in ctx, for RunEnvFor.
func WithRunToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, runTokenKey{}, token)
}

// RunTokenFrom is the token WithRunToken put in ctx, or "".
func RunTokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(runTokenKey{}).(string)
	return t
}
