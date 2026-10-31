package procbind

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Revoker runs a legacy token change and the v2 revocation of the same tokens in ONE write transaction, so a
// token is never left live in the v2 tables once the legacy change has committed (decision K1): if the v2
// side cannot be written, the legacy change is rolled back and the call fails. After the commit the live
// registry entries of the revoked tokens are dropped (Binder nil: nothing to drop, the host is not wired).
type Revoker struct {
	DB     *sql.DB
	Binder *Binder
	Now    func() time.Time
}

// Scope narrows which profiles' row keys a revocation derives: only those holding a not-yet-revoked v2 token the
// call can reach. A zero field does not narrow. Profile, TokenID, Owner (the owner and owner/...; mission tokens)
// and Session (a conversation; session tokens) mirror the Selectors the legacy callback returns; Orphans reaches
// the session tokens whose conversation no longer exists (the orphan sweep).
type Scope struct {
	Profile, TokenID, Owner, Session string
	Orphans                          bool
}

// Run opens a BEGIN IMMEDIATE transaction on a dedicated connection, calls legacy with it, and revokes in the
// same transaction the v2 tokens of each selector legacy returns (each names its Kind). It commits, then
// drops the live entries.
//
// The row key of each profile that holds a not-yet-revoked v2 token within scope is derived BEFORE the
// transaction (Keys): deriving a key may write the database, which must not happen inside the write
// transaction. A profile that gains its first v2 token between that read and the transaction has no key there,
// and the revocation fails closed: the caller retries. The 3c mint of a v2 token must therefore not interleave
// with a revoke-all of the same profile (or take the same key function first).
func (r Revoker) Run(ctx context.Context, kinds []Kind, scope Scope, legacy func(conn *sql.Conn) ([]Selector, error)) error {
	keyFor, err := r.Keys(ctx, kinds, scope)
	if err != nil {
		return err
	}
	conn, err := r.DB.Conn(ctx)
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
	sels, err := legacy(conn)
	if err != nil {
		return err
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	var refs []TokenRef
	for _, sel := range sels {
		if sel.Kind == "" {
			return ErrBadSelector
		}
		got, err := RevokeTx(ctx, conn, keyFor, sel, "revoked", now())
		if err != nil {
			return fmt.Errorf("revoking v2 tokens: %w", err)
		}
		refs = append(refs, got...)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	Drop(r.Binder, refs)
	return nil
}

// Keys derives the row key of every profile that holds a not-yet-revoked v2 token of one of kinds within scope,
// and returns the key function RevokeTx takes. It is for a caller that runs its own write transaction: call it
// BEFORE the transaction begins, because deriving a key may write the database.
//
// It never creates a key. A profile without a vault key row (a deleted profile: its retained token rows are
// signed under a key that no longer exists, so they verify nowhere and are inert) gets ErrNoRowKey, which
// RevokeTx reads as "nothing to revoke here"; so a later delete cannot bring a deleted profile's key back, and
// a delete does not depend on the keys of profiles it does not touch. A profile whose key row exists but cannot
// be read is an error (fail closed). A profile that is not in the answer fails closed too.
func (r Revoker) Keys(ctx context.Context, kinds []Kind, scope Scope) (func(profile string) ([]byte, error), error) {
	seen := map[string]bool{}
	var profiles []string
	for _, k := range kinds {
		q, args := scopeQuery(k, scope)
		rows, err := r.DB.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("listing v2 token profiles: %w", err)
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[p] {
				seen[p] = true
				profiles = append(profiles, p)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	keys := map[string][]byte{}
	noKey := map[string]bool{}
	for _, p := range profiles {
		var one int
		switch err := r.DB.QueryRowContext(ctx, `SELECT 1 FROM vault_keys WHERE profile_id = ?`, p).Scan(&one); {
		case errors.Is(err, sql.ErrNoRows):
			noKey[p] = true
			continue
		case err != nil:
			return nil, fmt.Errorf("looking up the vault key of a profile: %w", err)
		}
		key, err := RowKey(ctx, r.DB, p)
		if err != nil {
			return nil, fmt.Errorf("deriving the v2 row key: %w", err)
		}
		keys[p] = key
	}
	return func(profile string) ([]byte, error) {
		if k, ok := keys[profile]; ok {
			return k, nil
		}
		if noKey[profile] {
			return nil, ErrNoRowKey
		}
		return nil, errors.New("no row key was derived for this profile")
	}, nil
}

// scopeQuery lists the profiles holding a not-yet-revoked v2 token of kind k within scope.
func scopeQuery(k Kind, sc Scope) (string, []any) {
	if k == KindSession {
		orphans := 0
		if sc.Orphans {
			orphans = 1
		}
		return `SELECT DISTINCT profile_id FROM coder_session_tokens_v2 t
			WHERE (?1 = '' OR profile_id = ?1) AND (?2 = '' OR id = ?2) AND (?3 = '' OR conversation_id = ?3)
			  AND (?4 = 0 OR NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = t.conversation_id AND c.profile_id = t.profile_id))
			  AND NOT EXISTS (SELECT 1 FROM token_revocations_v2 r WHERE r.token_kind = 'session' AND r.token_id = t.id)`,
			[]any{sc.Profile, sc.TokenID, sc.Session, orphans}
	}
	return `SELECT DISTINCT profile_id FROM agent_tokens_v2 t
		WHERE (?1 = '' OR profile_id = ?1) AND (?2 = '' OR id = ?2)
		  AND (?3 = '' OR mission_owner = ?3 OR substr(mission_owner, 1, length(?3) + 1) = ?3 || '/')
		  AND NOT EXISTS (SELECT 1 FROM token_revocations_v2 r WHERE r.token_kind = 'mission' AND r.token_id = t.id)`,
		[]any{sc.Profile, sc.TokenID, sc.Owner}
}

// beginImmediate opens the write transaction on conn, retrying only a busy database (nothing has been read
// or written yet, so a retry repeats nothing).
func beginImmediate(ctx context.Context, conn *sql.Conn) error {
	for attempt := 0; ; attempt++ {
		_, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
		if err == nil || attempt >= 4 || !isBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(100 * time.Millisecond << attempt):
		}
	}
}

func isBusy(err error) bool {
	m := err.Error()
	return strings.Contains(m, "SQLITE_BUSY") || strings.Contains(m, "database is locked") || strings.Contains(m, "database table is locked")
}
