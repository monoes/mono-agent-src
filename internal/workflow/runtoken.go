package workflow

import (
	"context"
	"database/sql"
	"errors"
)

// EnvRunToken carries a run's token to the processes its nodes start.
const EnvRunToken = "MONOAGENT_RUN_TOKEN"

var errRunTokenTODO = errors.New("run tokens: not implemented")

// IssueRunToken: see the implementation.
func IssueRunToken(ctx context.Context, db *sql.DB, executionID string, approved bool) (string, error) {
	return "", errRunTokenTODO
}

// EndRunToken: see the implementation.
func EndRunToken(ctx context.Context, db *sql.DB, token string) error { return errRunTokenTODO }

// RunTokenApproved: see the implementation.
func RunTokenApproved(ctx context.Context, db *sql.DB, token string) (bool, error) {
	return false, errRunTokenTODO
}

type runTokenKey struct{}

// WithRunToken carries a run's token in ctx.
func WithRunToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, runTokenKey{}, token)
}

// RunTokenFrom is the token WithRunToken put in ctx, or "".
func RunTokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(runTokenKey{}).(string)
	return t
}
