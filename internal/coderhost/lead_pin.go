package coderhost

import (
	"context"
	"database/sql"
	"strings"

	"github.com/monoes/mono-agent/internal/monomind"
)

// LeadPin is the runtime/model a conversation runs, as the app recorded it, or "" when it is not known (no model named: the
// runtime's own default, which the host cannot tell from the judge's).
func LeadPin(db *sql.DB, profileID, conversationID string) string {
	return LeadPinIn(context.Background(), db, profileID, conversationID)
}

// LeadPinIn is LeadPin read through q (a transaction's connection, for `pursuit begin`).
func LeadPinIn(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, profileID, conversationID string) string {
	var rt, md string
	if err := q.QueryRowContext(ctx, `SELECT runtime_id, model FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`, conversationID, profileID).Scan(&rt, &md); err != nil {
		return ""
	}
	if md = strings.TrimSpace(md); md == "" {
		return ""
	}
	if rt = strings.TrimSpace(rt); rt == "" {
		rt = monomind.DefaultCoderRuntime
	}
	return rt + "/" + md
}
