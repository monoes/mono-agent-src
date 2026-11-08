package strategy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

const dirCols = `direction_id, key_hash, title, state, proposer_session, lead_session, created_epoch`

func scanDirection(session int64, sc interface{ Scan(...any) error }) (types.Direction, error) {
	d := types.Direction{Profile: strconv.FormatInt(session, 10), Generation: 1}
	var kh []byte
	var state string
	if err := sc.Scan(&d.ID, &kh, &d.Title, &state, &d.ProposerSession, &d.SessionID, &d.CreatedEpoch); err != nil {
		return d, err
	}
	d.State = types.DirectionState(state)
	var err error
	d.KeyHash, err = h32(kh)
	return d, err
}

// AcceptProposal stores an accepted proposal: the direction (state proposed) and its novelty row, in one transaction. A
// direction whose key already exists in the session is refused with *DuplicateKeyError. At most types.MaxUncheckedPerEpoch
// accepted-unchecked rows are allowed per epoch.
func (s *Store) AcceptProposal(ctx context.Context, d types.Direction, n types.NoveltyRow) error {
	session, err := sessionOf(d.Profile, d.Generation)
	if err != nil {
		return err
	}
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
	return s.tx(ctx, func(x queryer) error {
		if n.Outcome == types.NoveltyAcceptedUnchecked && n.EpochSeq > 0 {
			var c int
			if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_strategy_novelty WHERE session_id=? AND epoch_seq=? AND outcome='accepted-unchecked'`,
				session, n.EpochSeq).Scan(&c); err != nil {
				return err
			}
			if c >= types.MaxUncheckedPerEpoch {
				return ErrUncheckedLimit
			}
		}
		_, err := x.ExecContext(ctx, `INSERT INTO mission_strategy_directions(session_id, `+dirCols+`) VALUES (?,?,?,?,?,?,?,?)`,
			session, d.ID, d.KeyHash[:], d.Title, string(d.State), d.ProposerSession, d.SessionID, d.CreatedEpoch)
		if err != nil {
			return dirInsertErr(ctx, x, session, d, err)
		}
		return insertNovelty(ctx, x, session, n)
	})
}

func dirInsertErr(ctx context.Context, q queryer, session int64, d types.Direction, err error) error {
	if e := mapErr(err); e != err {
		return e
	}
	var id string
	if qe := q.QueryRowContext(ctx, `SELECT direction_id FROM mission_strategy_directions WHERE session_id=? AND key_hash=?`,
		session, d.KeyHash[:]).Scan(&id); qe == nil {
		return &DuplicateKeyError{ExistingID: id}
	}
	return err
}

// MoveDirection moves a direction forward (compare-and-set on the current state), optionally binding the lead session
// once. The session must be running.
func (s *Store) MoveDirection(ctx context.Context, profile string, gen int64, id string, to types.DirectionState, leadSession string) error {
	session, err := sessionOf(profile, gen)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(x queryer) error {
		if err := requireRunning(ctx, x, session); err != nil {
			return err
		}
		d, err := getDirection(ctx, x, session, id)
		if err != nil {
			return err
		}
		if !types.CanMoveDirection(d.State, to) {
			return ErrBackwardMove
		}
		from := d.State
		if leadSession != "" {
			if d.SessionID != "" && d.SessionID != leadSession {
				return ErrConflict
			}
			d.SessionID = leadSession
		}
		res, err := x.ExecContext(ctx, `UPDATE mission_strategy_directions SET state=?, lead_session=? WHERE session_id=? AND direction_id=? AND state=?`,
			string(to), d.SessionID, session, id, string(from))
		if err != nil {
			return mapErr(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrConflict
		}
		return nil
	})
}

func getDirection(ctx context.Context, q queryer, session int64, id string) (types.Direction, error) {
	d, err := scanDirection(session, q.QueryRowContext(ctx, `SELECT `+dirCols+` FROM mission_strategy_directions WHERE session_id=? AND direction_id=?`, session, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// GetDirection returns one direction.
func (s *Store) GetDirection(ctx context.Context, profile string, gen int64, id string) (types.Direction, error) {
	session, err := sessionOf(profile, gen)
	if err != nil {
		return types.Direction{}, err
	}
	return getDirection(ctx, s.db, session, id)
}

// ListDirections returns every direction of the session (optionally only the given states), in creation order.
func (s *Store) ListDirections(ctx context.Context, profile string, gen int64, states ...types.DirectionState) ([]types.Direction, error) {
	session, err := sessionOf(profile, gen)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+dirCols+` FROM mission_strategy_directions WHERE session_id=? ORDER BY rowid`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Direction
	for rows.Next() {
		d, err := scanDirection(session, rows)
		if err != nil {
			return nil, err
		}
		if len(states) == 0 || containsState(states, d.State) {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// OpenDirections are the directions still being worked: proposed, active or stalled.
func (s *Store) OpenDirections(ctx context.Context, profile string, gen int64) ([]types.Direction, error) {
	return s.ListDirections(ctx, profile, gen, types.DirProposed, types.DirActive, types.DirStalled)
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

func insertNovelty(ctx context.Context, x queryer, session int64, n types.NoveltyRow) error {
	_, err := x.ExecContext(ctx, `INSERT INTO mission_strategy_novelty(session_id, epoch_seq, key_hash, direction_id, matched_id, outcome) VALUES (?,?,?,?,?,?)`,
		session, n.EpochSeq, n.KeyHash[:], n.DirectionID, n.MatchedID, string(n.Outcome))
	return mapErr(err)
}

// RecordRefusal stores a refusal row (refused-*). Accepted outcomes go through AcceptProposal.
func (s *Store) RecordRefusal(ctx context.Context, n types.NoveltyRow) error {
	switch n.Outcome {
	case types.NoveltyRefusedLedger, types.NoveltyRefusedDirection, types.NoveltyRefusedCorpus:
	default:
		return fmt.Errorf("%w: novelty outcome %q is not a refusal", ErrInvalid, n.Outcome)
	}
	session, err := sessionOf(n.Profile, n.Generation)
	if err != nil {
		return err
	}
	if n.DirectionID != "" {
		return fmt.Errorf("%w: a refusal names no direction", ErrInvalid)
	}
	return s.tx(ctx, func(x queryer) error { return insertNovelty(ctx, x, session, n) })
}

// ListNovelty returns the ledger of the session, oldest first.
func (s *Store) ListNovelty(ctx context.Context, profile string, gen int64) ([]types.NoveltyRow, error) {
	session, err := sessionOf(profile, gen)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT epoch_seq, key_hash, direction_id, matched_id, outcome FROM mission_strategy_novelty WHERE session_id=? ORDER BY novelty_id`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.NoveltyRow
	for rows.Next() {
		n := types.NoveltyRow{Profile: profile, Generation: gen}
		var kh []byte
		var oc string
		if err := rows.Scan(&n.EpochSeq, &kh, &n.DirectionID, &n.MatchedID, &oc); err != nil {
			return nil, err
		}
		n.Outcome = types.NoveltyOutcome(oc)
		if n.KeyHash, err = h32(kh); err != nil {
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
