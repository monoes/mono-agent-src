package sstore

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

func payload(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func hx(h [32]byte) string { return hex.EncodeToString(h[:]) }

// ---- directions ----

const dirCols = `direction_id, key_hash, title, state, proposer_session, session_id, created_epoch, mac`

func scanDirection(profile string, gen int64, sc interface{ Scan(...any) error }) (types.Direction, []byte, error) {
	d := types.Direction{Profile: profile, Generation: gen}
	var kh, mac []byte
	var state string
	if err := sc.Scan(&d.ID, &kh, &d.Title, &state, &d.ProposerSession, &d.SessionID, &d.CreatedEpoch, &mac); err != nil {
		return d, nil, err
	}
	d.State = types.DirectionState(state)
	var err error
	d.KeyHash, err = h32(kh)
	return d, mac, err
}

// AcceptProposal stores an accepted proposal: the direction (state proposed) and its novelty row, with the proposal
// and novelty audit lines, in one transaction. A direction whose key already exists in the run is refused with
// *DuplicateKeyError. At most types.MaxUncheckedPerEpoch accepted-unchecked rows are allowed per epoch.
func (s *Store) AcceptProposal(ctx context.Context, d types.Direction, n types.NoveltyRow) error {
	if err := d.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if d.State != types.DirProposed {
		return fmt.Errorf("%w: a new direction starts as proposed", ErrInvalid)
	}
	if n.Outcome != types.NoveltyAccepted && n.Outcome != types.NoveltyAcceptedUnchecked {
		return fmt.Errorf("%w: novelty outcome %q does not accept a proposal", ErrInvalid, n.Outcome)
	}
	n.Profile, n.Generation, n.DirectionID, n.KeyHash = d.Profile, d.Generation, d.ID, d.KeyHash
	return s.inTx(ctx, d.Generation, 2, func(tx Tx) error {
		if n.Outcome == types.NoveltyAcceptedUnchecked && n.EpochSeq > 0 {
			var c int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_strategy_novelty WHERE profile=? AND generation=? AND epoch_seq=? AND outcome='accepted-unchecked'`,
				d.Profile, d.Generation, n.EpochSeq).Scan(&c); err != nil {
				return err
			}
			if c >= types.MaxUncheckedPerEpoch {
				return ErrUncheckedLimit
			}
		}
		mac, err := s.mac(d.Canonical())
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO doomsday_strategy_directions(profile, generation, `+dirCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			d.Profile, d.Generation, d.ID, d.KeyHash[:], d.Title, string(d.State), d.ProposerSession, d.SessionID, d.CreatedEpoch, mac)
		if err != nil {
			return s.dirInsertErr(ctx, tx, d, err)
		}
		if err := s.insertNovelty(ctx, tx, n); err != nil {
			return err
		}
		if err := s.appendAudit(ctx, tx, d.Profile, d.Generation, types.KindProposal,
			payload(map[string]string{"direction": d.ID, "key": hx(d.KeyHash), "proposer": d.ProposerSession})); err != nil {
			return err
		}
		return s.appendAudit(ctx, tx, d.Profile, d.Generation, types.KindNovelty,
			payload(map[string]string{"direction": d.ID, "key": hx(d.KeyHash), "outcome": string(n.Outcome)}))
	})
}

func (s *Store) dirInsertErr(ctx context.Context, q Querier, d types.Direction, err error) error {
	if e := mapErr(err); e != err {
		return e
	}
	var id string
	if qe := q.QueryRowContext(ctx, `SELECT direction_id FROM doomsday_strategy_directions WHERE profile=? AND generation=? AND key_hash=?`,
		d.Profile, d.Generation, d.KeyHash[:]).Scan(&id); qe == nil {
		return &DuplicateKeyError{ExistingID: id}
	}
	return err
}

// MoveDirection moves a direction forward (compare-and-set on the current state), optionally binding the lead session
// once, re-MACs it and appends the direction-state audit line. The run must be running.
func (s *Store) MoveDirection(ctx context.Context, profile string, gen int64, id string, to types.DirectionState, sessionID string) error {
	return s.inTx(ctx, gen, 1, func(tx Tx) error {
		if err := requireRunning(ctx, tx, profile, gen); err != nil {
			return err
		}
		d, err := s.getDirection(ctx, tx, profile, gen, id)
		if err != nil {
			return err
		}
		if !types.CanMoveDirection(d.State, to) {
			return ErrBackwardMove
		}
		from := d.State
		d.State = to
		if sessionID != "" {
			if d.SessionID != "" && d.SessionID != sessionID {
				return ErrConflict
			}
			d.SessionID = sessionID
		}
		mac, err := s.mac(d.Canonical())
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE doomsday_strategy_directions SET state=?, session_id=?, mac=? WHERE profile=? AND generation=? AND direction_id=? AND state=?`,
			string(to), d.SessionID, mac, profile, gen, id, string(from))
		if err != nil {
			return mapErr(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrConflict
		}
		return s.appendAudit(ctx, tx, profile, gen, types.KindDirectionState,
			payload(map[string]string{"direction": id, "from": string(from), "to": string(to)}))
	})
}

func (s *Store) getDirection(ctx context.Context, q Querier, profile string, gen int64, id string) (types.Direction, error) {
	d, mac, err := scanDirection(profile, gen, q.QueryRowContext(ctx, `SELECT `+dirCols+` FROM doomsday_strategy_directions WHERE profile=? AND generation=? AND direction_id=?`, profile, gen, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	return d, s.verify(d.Canonical(), mac)
}

// GetDirection returns one verified direction.
func (s *Store) GetDirection(ctx context.Context, profile string, gen int64, id string) (types.Direction, error) {
	return s.getDirection(ctx, s.db, profile, gen, id)
}

// ListDirections returns every direction of the run (optionally only the given states), verified, in creation order.
func (s *Store) ListDirections(ctx context.Context, profile string, gen int64, states ...types.DirectionState) ([]types.Direction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+dirCols+` FROM doomsday_strategy_directions WHERE profile=? AND generation=? ORDER BY rowid`, profile, gen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Direction
	for rows.Next() {
		d, mac, err := scanDirection(profile, gen, rows)
		if err != nil {
			return nil, err
		}
		if err := s.verify(d.Canonical(), mac); err != nil {
			return nil, err
		}
		if len(states) == 0 || containsState(states, d.State) {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

func containsState(ss []types.DirectionState, s types.DirectionState) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ---- novelty ----

func (s *Store) insertNovelty(ctx context.Context, tx Tx, n types.NoveltyRow) error {
	mac, err := s.mac(n.Canonical())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO doomsday_strategy_novelty(profile, generation, epoch_seq, key_hash, direction_id, matched_id, outcome, mac) VALUES (?,?,?,?,?,?,?,?)`,
		n.Profile, n.Generation, n.EpochSeq, n.KeyHash[:], n.DirectionID, n.MatchedID, string(n.Outcome), mac)
	return mapErr(err)
}

// RecordRefusal stores a refusal row (refused-*) and its novelty audit line. Accepted outcomes go through AcceptProposal.
func (s *Store) RecordRefusal(ctx context.Context, n types.NoveltyRow) error {
	switch n.Outcome {
	case types.NoveltyRefusedLedger, types.NoveltyRefusedDirection, types.NoveltyRefusedCorpus:
	default:
		return fmt.Errorf("%w: novelty outcome %q is not a refusal", ErrInvalid, n.Outcome)
	}
	if n.Profile == "" || n.Generation < 1 || n.DirectionID != "" {
		return fmt.Errorf("%w: a refusal names a run and no direction", ErrInvalid)
	}
	return s.inTx(ctx, n.Generation, 1, func(tx Tx) error {
		if err := s.insertNovelty(ctx, tx, n); err != nil {
			return err
		}
		return s.appendAudit(ctx, tx, n.Profile, n.Generation, types.KindNovelty,
			payload(map[string]string{"key": hx(n.KeyHash), "outcome": string(n.Outcome), "matched": n.MatchedID}))
	})
}

// ListNovelty returns the ledger of the run, verified, oldest first.
func (s *Store) ListNovelty(ctx context.Context, profile string, gen int64) ([]types.NoveltyRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT epoch_seq, key_hash, direction_id, matched_id, outcome, mac FROM doomsday_strategy_novelty WHERE profile=? AND generation=? ORDER BY novelty_id`, profile, gen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.NoveltyRow
	for rows.Next() {
		n := types.NoveltyRow{Profile: profile, Generation: gen}
		var kh, mac []byte
		var oc string
		if err := rows.Scan(&n.EpochSeq, &kh, &n.DirectionID, &n.MatchedID, &oc, &mac); err != nil {
			return nil, err
		}
		n.Outcome = types.NoveltyOutcome(oc)
		if n.KeyHash, err = h32(kh); err != nil {
			return nil, err
		}
		if err := s.verify(n.Canonical(), mac); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// FindNovelty returns the first ledger row with the key hash (the ledger lookup the novelty gate uses), if any.
func (s *Store) FindNovelty(ctx context.Context, profile string, gen int64, key [32]byte) (types.NoveltyRow, bool, error) {
	all, err := s.ListNovelty(ctx, profile, gen)
	if err != nil {
		return types.NoveltyRow{}, false, err
	}
	for _, n := range all {
		if n.KeyHash == key {
			return n, true, nil
		}
	}
	return types.NoveltyRow{}, false, nil
}
