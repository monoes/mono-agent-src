package agenttokens

import (
	"context"
	"database/sql"

	"github.com/monoes/mono-agent/internal/procbind"
)

// A legacy revocation also revokes the v2 mission tokens of the same selection, in the same write
// transaction (procbind.Revoker; design section 7, decision K1): if the v2 side cannot be written,
// the legacy change is rolled back and the call fails. The live registry entries are dropped after
// the commit (SetBinder; nil until the host is wired).
//
// Which v2 tokens a legacy call reaches: Revoke selects by the legacy token's id, so it finds a v2 token
// only if it has that id. In the flag-on world a turn has no legacy token (3c issues the v2 token instead,
// with its own id), so Revoke(legacy id) finds nothing there and is harmless; every caller that revokes
// a mission token it minted must then revoke by owner (RevokeOwner) or by the v2 id. Those callers are
// the 3c requirement: childTokenMinter.RevokeChildTokens/revokeToken, settleChild and the launcher's wait
// goroutine.

// SetBinder sets the registry whose live entries are ended after a revocation commits.
func (s *Store) SetBinder(b *procbind.Binder) { s.binder = b }

// revokeBoth runs legacy and the v2 revocation of sel in one transaction.
func (s *Store) revokeBoth(ctx context.Context, sel procbind.Selector, legacy func(conn *sql.Conn) error) error {
	sel.Kind = procbind.KindMission
	return procbind.Revoker{DB: s.db, Binder: s.binder, Now: s.now}.Run(ctx, []procbind.Kind{procbind.KindMission},
		func(conn *sql.Conn) ([]procbind.Selector, error) { return []procbind.Selector{sel}, legacy(conn) })
}
