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

// Run opens a BEGIN IMMEDIATE transaction on a dedicated connection, calls legacy with it, and revokes in the
// same transaction the v2 tokens of each selector legacy returns (each names its Kind). It commits, then
// drops the live entries.
//
// The row key of every profile that holds a v2 token of one of kinds is derived BEFORE the transaction:
// deriving a key may write the database, which must not happen inside the write transaction. A profile that
// gains its first v2 token between that read and the transaction has no key there, and the revocation fails
// closed: the caller retries. The 3c mint of a v2 token must therefore not interleave with a revoke-all of
// the same profile (or take the same key function first).
func (r Revoker) Run(ctx context.Context, kinds []Kind, legacy func(conn *sql.Conn) ([]Selector, error)) error {
	keyFor, err := r.Keys(ctx, kinds)
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

// Keys derives the row key of every profile that holds a v2 token of one of kinds and returns the key function
// RevokeTx takes (a profile it did not derive has no key: the revocation fails closed). It is for a caller that
// runs its own write transaction: call it BEFORE the transaction begins, because deriving a key may write the
// database.
func (r Revoker) Keys(ctx context.Context, kinds []Kind) (func(profile string) ([]byte, error), error) {
	keys, err := r.keys(ctx, kinds)
	if err != nil {
		return nil, err
	}
	return func(profile string) ([]byte, error) {
		if k, ok := keys[profile]; ok {
			return k, nil
		}
		return nil, errors.New("no row key was derived for this profile")
	}, nil
}

func (r Revoker) keys(ctx context.Context, kinds []Kind) (map[string][]byte, error) {
	seen := map[string]bool{}
	var profiles []string
	for _, k := range kinds {
		table := "agent_tokens_v2"
		if k == KindSession {
			table = "coder_session_tokens_v2"
		}
		rows, err := r.DB.QueryContext(ctx, `SELECT DISTINCT profile_id FROM `+table)
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
	for _, p := range profiles {
		key, err := RowKey(ctx, r.DB, p)
		if err != nil {
			return nil, fmt.Errorf("deriving the v2 row key: %w", err)
		}
		keys[p] = key
	}
	return keys, nil
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
