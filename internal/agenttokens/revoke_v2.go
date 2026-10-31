package agenttokens

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

// A legacy revocation also revokes the v2 mission tokens of the same selection, in the same write
// transaction, so a token is never left live in the v2 tables once the legacy change has committed
// (design section 7, decision K1): if the v2 side cannot be written, the legacy change is rolled back
// and the call fails. The live registry entries are dropped after the commit (SetBinder; nil until
// the host is wired).

// SetBinder sets the registry whose live entries are ended after a revocation commits.
func (s *Store) SetBinder(b *procbind.Binder) { s.binder = b }

// beginImmediate opens the write transaction on conn, retrying only a busy database (nothing has
// been read or written yet, so a retry repeats nothing).
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

// v2Keys derives, before the transaction, the row key of every profile that holds a v2 mission token
// the selector can reach: deriving a key may write to this database, which must not happen inside the
// write transaction. A profile that gains its first token between this read and the transaction has no
// key there, and the revocation fails closed (the caller retries).
func (s *Store) v2Keys(ctx context.Context, profileID string) (map[string][]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT profile_id FROM agent_tokens_v2 WHERE (?1 = '' OR profile_id = ?1)`, profileID)
	if err != nil {
		return nil, fmt.Errorf("listing v2 token profiles: %w", err)
	}
	var profiles []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		profiles = append(profiles, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keys := map[string][]byte{}
	for _, p := range profiles {
		k, err := procbind.RowKey(ctx, s.db, p)
		if err != nil {
			return nil, fmt.Errorf("deriving the v2 row key: %w", err)
		}
		keys[p] = k
	}
	return keys, nil
}

// revokeBoth runs legacy and the v2 revocation of sel in one BEGIN IMMEDIATE transaction. Any error
// from either rolls both back. After the commit the live entries of the revoked v2 tokens are dropped.
func (s *Store) revokeBoth(ctx context.Context, sel procbind.Selector, legacy func(conn *sql.Conn) error) error {
	sel.Kind = procbind.KindMission
	keys, err := s.v2Keys(ctx, sel.Profile)
	if err != nil {
		return err
	}
	keyFor := func(profile string) ([]byte, error) {
		if k, ok := keys[profile]; ok {
			return k, nil
		}
		return nil, errors.New("no row key was derived for this profile")
	}
	conn, err := s.db.Conn(ctx)
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
	if err := legacy(conn); err != nil {
		return err
	}
	refs, err := procbind.RevokeTx(ctx, conn, keyFor, sel, "revoked", s.now())
	if err != nil {
		return fmt.Errorf("revoking v2 mission tokens: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	procbind.Drop(s.binder, refs)
	return nil
}
