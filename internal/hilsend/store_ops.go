package hilsend

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/commsync"
)

// Edit changes a pending item's text and/or subject (nil leaves one alone).
// The item stays pending, so the card re-previews the edit.
func (s Store) Edit(ctx context.Context, id string, text, subject *string) (Payload, error) {
	for attempt := 0; ; attempt++ {
		p, retry, err := s.editOnce(ctx, id, text, subject)
		if !retry || attempt >= 1 {
			return p, err
		}
	}
}

// editOnce is one try of Edit; retry says the row changed under it (the scrub
// of an older row, a claim) between the read and the write.
func (s Store) editOnce(ctx context.Context, id string, text, subject *string) (Payload, bool, error) {
	r, err := s.find(ctx, id)
	if err != nil {
		return Payload{}, false, err
	}
	p, _, err := r.payload()
	if err != nil {
		return Payload{}, false, err
	}
	if p.State != StatePending {
		return p, false, fmt.Errorf("send item %s is %s, only a pending one can be edited", p.ID, p.State)
	}
	if text != nil && p.TextReadOnly {
		return p, false, fmt.Errorf("send item %s: %w", p.ID, ErrNoText)
	}
	if subject != nil && p.SubjectReadOnly {
		return p, false, fmt.Errorf("send item %s: %w", p.ID, ErrNoSubject)
	}
	if text != nil {
		p.Text = *text
	}
	if subject != nil {
		p.Subject = *subject
	}
	raw, _ := json.Marshal(editable{Text: p.Text, Subject: p.Subject})
	res, err := s.DB.ExecContext(ctx,
		`UPDATE hil_pending SET editable_data = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = 'pending' AND profile_id = ? AND editable_data = ?`,
		string(raw), p.ID, s.Profile, r.ed)
	if err != nil {
		return p, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		cur, gerr := s.Get(ctx, p.ID)
		return cur, cur.State == StatePending, gerr
	}
	warn(commsync.SetText(ctx, s.DB, p.ID, &p.Text, &p.Subject), "edit")
	return p, false, nil
}

// betweenReadAndWrite lets a test change the row after claim read it.
var betweenReadAndWrite func()

// claim moves a pending item to sending, once. It returns the payload and
// replay as claimed and the proof that lets the performing process through
// Guard. claimed is false when the item was not pending (already resolved,
// or someone else claimed it first). always (an always-allow approve) is
// refused for a label recipient (ErrAlwaysLabel), leaving it pending.
func (s Store) claim(ctx context.Context, id string, always bool) (p Payload, rp replay, proof string, claimed bool, err error) {
	for attempt := 0; ; attempt++ {
		var retry bool
		p, rp, proof, claimed, retry, err = s.claimOnce(ctx, id, always)
		if !retry || attempt >= 1 {
			return
		}
	}
}

// claimOnce is one try of claim; retry says the row changed under it (the
// scrub of an older row) between the read and the write: the write is guarded
// by the node_config it read, so a scrubbed row is never overwritten.
func (s Store) claimOnce(ctx context.Context, id string, always bool) (p Payload, rp replay, proof string, claimed, retry bool, err error) {
	r, err := s.find(ctx, id)
	if betweenReadAndWrite != nil {
		betweenReadAndWrite()
	}
	if err != nil {
		return p, rp, "", false, false, err
	}
	p, rp, err = r.payload()
	if err != nil {
		return p, rp, "", false, false, err
	}
	if p.State != StatePending {
		return p, rp, "", false, false, nil
	}
	if p.Unreviewable != "" {
		// A card that can't show what the step sends isn't approved from:
		// it stays pending, to be rejected.
		return p, rp, "", false, false, fmt.Errorf("send item %s: %w (%s)", p.ID, ErrUnreviewable, p.Unreviewable)
	}
	if always && p.Recipient.Label {
		// What is claimed decides, not an earlier read: it stays pending.
		return p, rp, "", false, false, ErrAlwaysLabel
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return p, rp, "", false, false, err
	}
	proof = hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(proof))
	rp.ProofHash = hex.EncodeToString(sum[:])
	rp.ResolvedAt = now()
	raw, _ := json.Marshal(rp)
	res, err := s.DB.ExecContext(ctx,
		`UPDATE hil_pending SET status = 'sending', node_config = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = 'pending' AND profile_id = ? AND node_config = ?`, string(raw), p.ID, s.Profile, r.nc)
	if err != nil {
		return p, rp, "", false, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		cur, gerr := s.Get(ctx, p.ID)
		return cur, rp, "", false, cur.State == StatePending, gerr
	}
	p.State, p.ResolvedAt = StateApproved, rp.ResolvedAt
	s.commsStatus(ctx, p.ID, commsync.Approved, commsync.Detail{Text: &p.Text, Subject: &p.Subject})
	s.commsStatus(ctx, p.ID, commsync.Sending, commsync.Detail{})
	return p, rp, proof, true, false, nil
}

// reject marks a pending item rejected; false when it was not pending.
func (s Store) reject(ctx context.Context, id string) (Payload, bool, error) {
	for attempt := 0; ; attempt++ {
		p, done, retry, err := s.rejectOnce(ctx, id)
		if !retry || attempt >= 1 {
			return p, done, err
		}
	}
}

func (s Store) rejectOnce(ctx context.Context, id string) (Payload, bool, bool, error) {
	r, err := s.find(ctx, id)
	if err != nil {
		return Payload{}, false, false, err
	}
	p, rp, err := r.payload()
	if err != nil {
		return p, false, false, err
	}
	if p.State != StatePending {
		return p, false, false, nil
	}
	rp.ResolvedAt = now()
	raw, _ := json.Marshal(rp)
	res, err := s.DB.ExecContext(ctx,
		`UPDATE hil_pending SET status = 'rejected', node_config = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = 'pending' AND profile_id = ? AND node_config = ?`, string(raw), p.ID, s.Profile, r.nc)
	if err != nil {
		return p, false, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		cur, gerr := s.Get(ctx, p.ID)
		return cur, false, cur.State == StatePending, gerr
	}
	p.State, p.ResolvedAt = StateRejected, rp.ResolvedAt
	s.commsStatus(ctx, p.ID, commsync.Rejected, commsync.Detail{})
	return p, true, false, nil
}

// finish records how a claimed send ended.
func (s Store) finish(ctx context.Context, id string, result json.RawMessage, runErr error) (Payload, error) {
	for attempt := 0; ; attempt++ {
		p, retry, err := s.finishOnce(ctx, id, result, runErr)
		if !retry {
			return p, err
		}
		if attempt >= 1 {
			// The row changed under both tries: say so rather than answer
			// success for a card that is still "sending".
			return p, ErrNotFinished
		}
	}
}

func (s Store) finishOnce(ctx context.Context, id string, result json.RawMessage, runErr error) (Payload, bool, error) {
	r, err := s.find(ctx, id)
	if err != nil {
		return Payload{}, false, err
	}
	_, rp, err := r.payload()
	if err != nil {
		return Payload{}, false, err
	}
	status := "sent"
	rp.ProofHash = ""
	if runErr != nil {
		status, rp.Error = "failed", runErr.Error()
	} else {
		rp.Result = result
	}
	raw, _ := json.Marshal(rp)
	res, err := s.DB.ExecContext(ctx,
		`UPDATE hil_pending SET status = ?, node_config = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = 'sending' AND profile_id = ? AND node_config = ?`, status, string(raw), r.id, s.Profile, r.nc)
	if err != nil {
		return Payload{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if cur, gerr := s.Get(ctx, r.id); gerr == nil && cur.State == StateApproved {
			return cur, true, nil // the row was scrubbed under the read: once more
		}
	}
	if runErr != nil {
		s.commsStatus(ctx, r.id, commsync.Failed, commsync.Detail{Error: runErr.Error()})
	} else {
		s.commsStatus(ctx, r.id, commsync.Sent, commsync.Detail{})
	}
	p, err := s.Get(ctx, r.id)
	return p, false, err
}
