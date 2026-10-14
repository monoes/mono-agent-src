package doctor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/health"
)

// addAccountHooks wires the accounts checks to the profile's saved
// credentials. The checks only read: no table is created, no status or
// label is saved, nothing is printed, and every secret value is removed
// from error text before it reaches the report. Only the refresh fix
// writes, and only a connection of this profile.
func addAccountHooks(env *health.Env, db *sql.DB) {
	profileID := env.ProfileID
	store := connections.NewStore(db)
	env.Connections = func(ctx context.Context) ([]health.ConnectionInfo, error) {
		if !tableExists(ctx, db, "connections") {
			return nil, nil
		}
		conns, err := store.ListAll(ctx, profileID)
		if err != nil {
			return nil, err
		}
		out := make([]health.ConnectionInfo, 0, len(conns))
		for _, c := range conns {
			rt, _ := c.Data["refresh_token"].(string)
			out = append(out, health.ConnectionInfo{ID: c.ID, Platform: c.Platform, Label: c.Label,
				Method: string(c.Method), HasRefreshToken: rt != ""})
		}
		return out, nil
	}
	env.TestConnection = func(ctx context.Context, id string) error {
		conn, err := store.Get(ctx, id, profileID)
		if err != nil || conn == nil {
			return fmt.Errorf("connection %q not found in this profile", id)
		}
		// ValidateConnection, not Manager.Test: Test saves a status and a
		// new label and prints to stdout, which would change the connection
		// and break `doctor --json`. The check bounds how long it may take.
		_, err = connections.ValidateConnection(ctx, conn)
		return classifyConnectionError(ScrubSecrets(err, ConnectionSecrets(conn)...), err, conn, time.Now())
	}
	env.RefreshConnection = func(ctx context.Context, id string) error {
		conn, err := store.Get(ctx, id, profileID)
		if err != nil || conn == nil {
			return fmt.Errorf("connection %q not found in this profile", id)
		}
		// Manager.Refresh falls back to the interactive browser flow when
		// there is no refresh token; a fix must never do that.
		if rt, _ := conn.Data["refresh_token"].(string); conn.Method != connections.MethodOAuth || rt == "" {
			return fmt.Errorf("connection %q has no refresh token — reconnect it: monoagentcli connect %s", id, conn.Platform)
		}
		mgr, err := connections.NewManager(db)
		if err != nil {
			return err
		}
		return ScrubSecrets(mgr.Refresh(ctx, id, time.Minute), ConnectionSecrets(conn)...)
	}

	env.LoginSessions = func(ctx context.Context) ([]health.SessionInfo, error) {
		if !tableExists(ctx, db, "crawler_sessions") {
			return nil, nil
		}
		rows, err := db.QueryContext(ctx,
			`SELECT platform, username, expiry FROM crawler_sessions WHERE profile_id = ? ORDER BY platform`, profileID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []health.SessionInfo
		for rows.Next() {
			var s health.SessionInfo
			if err := rows.Scan(&s.Platform, &s.Username, &s.Expiry); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, rows.Err()
	}
}

// classifyConnectionError marks a failed connection test for the check:
// credentials rejected (HTTP 401/403, or the access token is past its
// expires_at) → a silent refresh is offered; the service not answering
// (network, timeout, 5xx, 429) → nothing is offered. Anything else stays
// unmarked, and a refresh is only offered after asking. scrubbed is raw
// with the secrets removed; raw is kept for its error chain.
func classifyConnectionError(scrubbed, raw error, conn *connections.Connection, now time.Time) error {
	if raw == nil {
		return nil
	}
	code := connections.HTTPStatus(raw)
	switch {
	case connections.CredentialsRejected(raw) || tokenExpired(conn, now):
		return health.CredentialsRejected(scrubbed)
	case code >= 500 || code == http.StatusTooManyRequests || isUnreachable(raw):
		return health.Unreachable(scrubbed)
	}
	return scrubbed
}

// tokenExpired reports whether an OAuth connection's access token is past
// its expires_at.
func tokenExpired(conn *connections.Connection, now time.Time) bool {
	s, _ := conn.Data["expires_at"].(string)
	t, err := time.Parse(time.RFC3339, s)
	return err == nil && now.After(t)
}

// isUnreachable reports a transport failure: timeout, DNS, refused.
func isUnreachable(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne)
}

func tableExists(ctx context.Context, db *sql.DB, name string) bool {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return err == nil && n > 0
}

// ConnectionSecrets lists every string value a connection stores, which
// is what could end up in an error: validators put tokens in URLs
// (Telegram's is in the path) and HTTP errors print the whole URL.
func ConnectionSecrets(c *connections.Connection) []string {
	var out []string
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case map[string]interface{}:
			for _, e := range x {
				walk(e)
			}
		case []interface{}:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(c.Data)
	return out
}

// ScrubSecrets returns err with every secret value (6 characters or more,
// so a short flag value does not blank out ordinary words) replaced.
func ScrubSecrets(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, s := range secrets {
		if len(s) >= 6 {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}
