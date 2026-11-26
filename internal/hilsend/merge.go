package hilsend

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/dbconn"
	"github.com/monoes/mono-agent/internal/hilscrub"
)

// An identical send that still waits is one card (r10-R10-07): an agent that
// retries, or a loop over the same item, would otherwise queue copies, and
// approving each would send it again.
//
// Identical is strict: the same profile, node type and action, account,
// connection and recipient, the same current text and subject, attachments
// (path, size and type) and details, the same run config and input, prepared
// by the same caller, and both still pending. Anything else gets its own card,
// as does a send that holds a secret: its references are bound to one card.
//
// Nothing waits on a send card (its execution id "send:<id>" is no run's):
// every caller gets an output naming the card and goes on, so a caller merged
// into a card has nothing to resume. The card's one Communications row and its
// one publication approval are shared with it.

// mergedNote ends the message of an output whose send joined a waiting card.
const mergedNote = " An identical send was already waiting, so no second card was made: it is sent once, if approved."

// createMerged is create, unless an identical card already waits: it then
// returns that card and merged true. The look and the insert are one write
// transaction, so concurrent callers make one card.
func (s Store) createMerged(ctx context.Context, p Payload, rp replay) (Payload, bool, error) {
	if len(rp.RefHashes) > 0 {
		q, err := s.create(ctx, p, rp)
		return q, false, err
	}
	// Waiting cards are compared and one may be returned: older rows are scrubbed
	// first, as before any read of a card's text (outside the transaction below).
	hilscrub.Ensure(ctx, s.DB, s.Profile)
	p, c, err := encodeCard(p, rp)
	if err != nil {
		return p, false, err
	}
	var waiting *Payload
	err = dbconn.WithWriteTx(ctx, s.DB, func(conn *sql.Conn) error {
		var err error
		if waiting, err = s.identicalPending(ctx, conn, p, c); err != nil || waiting != nil {
			return err
		}
		return s.insertCard(ctx, conn, p, c)
	})
	if err != nil {
		return p, false, fmt.Errorf("queueing the send for approval: %w", err)
	}
	if waiting != nil {
		return *waiting, true, nil
	}
	s.commsQueue(ctx, p, c.replay)
	return p, false, nil
}

// identicalPending is the oldest pending card of the profile that is the same
// send as the new card c of p, or nil. The query narrows to the cards with its
// node, action, text and address; sameness decides.
func (s Store) identicalPending(ctx context.Context, conn *sql.Conn, p Payload, c cardRow) (*Payload, error) {
	want, ok := sameness(c.ro, c.ed, c.nc)
	if !ok {
		return nil, nil
	}
	rows, err := conn.QueryContext(ctx,
		`SELECT id, status, readonly_data, editable_data, node_config FROM hil_pending
		 WHERE profile_id = ? AND status = 'pending' AND execution_id LIKE 'send:%' AND node_id = ? AND node_name = ?
		   AND editable_data = ? AND CASE WHEN json_valid(node_config) THEN COALESCE(json_extract(node_config, '$.address'), '') END = ?
		 ORDER BY created_at, rowid`,
		s.Profile, p.NodeType, "send:"+p.Action, c.ed, c.replay.Address)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.status, &r.ro, &r.ed, &r.nc); err != nil {
			return nil, err
		}
		if got, ok := sameness(r.ro, r.ed, r.nc); ok && got == want {
			if found, _, err := r.payload(); err == nil {
				return &found, nil
			}
		}
	}
	return nil, rows.Err()
}

// sameness is everything a card's row says about its send, in one canonical
// string: all but the card's own id and age, and the People row its recipient
// was matched to, which queueing the first card may itself have created (a
// person the config names is in the config too). False for a row it can't read.
func sameness(ro, ed, nc string) (string, bool) {
	var p Payload
	var e editable
	var rp replay
	for _, part := range []struct {
		raw string
		to  any
	}{{ro, &p}, {ed, &e}, {nc, &rp}} {
		dec := json.NewDecoder(bytes.NewReader([]byte(part.raw)))
		dec.UseNumber() // a long numeric id must not round to its neighbour
		if dec.Decode(part.to) != nil {
			return "", false
		}
	}
	p.ID, p.CreatedAt, p.Recipient.PersonID, p.Recipient.PersonName = "", "", "", ""
	raw, err := json.Marshal([]any{p, e, rp})
	return string(raw), err == nil
}
