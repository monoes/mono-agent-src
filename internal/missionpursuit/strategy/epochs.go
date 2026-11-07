package strategy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

const epochCols = `epoch_seq, trigger, started_at, judgment_hash, model_pin, state`

func scanEpoch(session int64, sc interface{ Scan(...any) error }) (types.Epoch, error) {
	e := types.Epoch{Profile: strconv.FormatInt(session, 10), Generation: 1}
	var trig, state string
	var jh []byte
	if err := sc.Scan(&e.Seq, &trig, &e.StartedAt, &jh, &e.ModelPin, &state); err != nil {
		return e, err
	}
	e.Trigger, e.State = types.Trigger(trig), types.EpochState(state)
	if len(jh) > 0 {
		var err error
		if e.JudgmentHash, err = h32(jh); err != nil {
			return e, err
		}
		e.HasJudgment = true
	}
	return e, nil
}

func judgmentHashArg(e types.Epoch) []byte {
	if !e.HasJudgment {
		return []byte{}
	}
	return e.JudgmentHash[:]
}

// InsertEpoch opens an epoch (state open). The identity is (session, seq), so a restart that repeats the call is
// idempotent: when the epoch exists with the same trigger, start time and model pin, the stored epoch is returned with
// created=false and nothing is written; any other content under the same identity is ErrConflict.
func (s *Store) InsertEpoch(ctx context.Context, e types.Epoch) (types.Epoch, bool, error) {
	session, err := sessionOf(e.Profile, e.Generation)
	if err != nil {
		return e, false, err
	}
	if err := e.Validate(); err != nil {
		return e, false, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if e.State != types.EpochOpen || e.HasJudgment {
		return e, false, fmt.Errorf("%w: a new epoch is open and has no judgment", ErrInvalid)
	}
	var out types.Epoch
	created := false
	err = s.tx(ctx, func(x queryer) error {
		have, err := getEpoch(ctx, x, session, e.Seq)
		if err == nil {
			if have.Trigger != e.Trigger || have.StartedAt != e.StartedAt || have.ModelPin != e.ModelPin {
				return ErrConflict
			}
			out = have
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if _, err := x.ExecContext(ctx, `INSERT INTO mission_strategy_epochs(session_id, `+epochCols+`) VALUES (?,?,?,?,?,?,?)`,
			session, e.Seq, string(e.Trigger), e.StartedAt, judgmentHashArg(e), e.ModelPin, string(e.State)); err != nil {
			return mapErr(err)
		}
		out, created = e, true
		return nil
	})
	if err != nil {
		return e, false, err
	}
	return out, created, nil
}

// RecordJudgment stores the validated judgment body of an open epoch and moves the epoch to judged, binding the body
// hash, in one transaction. The body hash is recomputed here, never trusted. A repeat with the same body for an already
// judged epoch is idempotent (created=false); a different body is ErrConflict.
func (s *Store) RecordJudgment(ctx context.Context, profile string, gen, seq int64, body []byte) (bool, error) {
	session, err := sessionOf(profile, gen)
	if err != nil {
		return false, err
	}
	if len(body) == 0 || len(body) > types.MaxJudgmentBytes {
		return false, fmt.Errorf("%w: judgment body size %d", ErrInvalid, len(body))
	}
	hash := types.BodyHash(body)
	created := false
	err = s.tx(ctx, func(x queryer) error {
		if err := requireRunning(ctx, x, session); err != nil {
			return err
		}
		ep, err := getEpoch(ctx, x, session, seq)
		if err != nil {
			return err
		}
		if ep.State != types.EpochOpen {
			if ep.HasJudgment && ep.JudgmentHash == hash {
				return nil
			}
			if ep.HasJudgment {
				return ErrConflict
			}
			return ErrNeedsOpenEpoch
		}
		if _, err := x.ExecContext(ctx, `INSERT INTO mission_strategy_judgments(session_id, epoch_seq, body, body_hash) VALUES (?,?,?,?)`,
			session, seq, string(body), hash[:]); err != nil {
			return mapErr(err)
		}
		ep.State, ep.HasJudgment, ep.JudgmentHash = types.EpochJudged, true, hash
		if err := updateEpoch(ctx, x, session, ep, types.EpochOpen); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

// MoveEpoch moves an epoch to bad-judgment, applied or abandoned (judged is reached only through RecordJudgment). The
// session must be running, except for abandoned: an epoch left open by a pause or a stop is closed afterwards.
func (s *Store) MoveEpoch(ctx context.Context, profile string, gen, seq int64, to types.EpochState) error {
	session, err := sessionOf(profile, gen)
	if err != nil {
		return err
	}
	if to == types.EpochJudged || to == types.EpochOpen {
		return fmt.Errorf("%w: use RecordJudgment to judge an epoch", ErrInvalid)
	}
	return s.tx(ctx, func(x queryer) error {
		if to != types.EpochAbandoned {
			if err := requireRunning(ctx, x, session); err != nil {
				return err
			}
		}
		ep, err := getEpoch(ctx, x, session, seq)
		if err != nil {
			return err
		}
		if !types.CanMoveEpoch(ep.State, to) {
			return ErrBackwardMove
		}
		from := ep.State
		ep.State = to
		return updateEpoch(ctx, x, session, ep, from)
	})
}

func updateEpoch(ctx context.Context, x queryer, session int64, e types.Epoch, from types.EpochState) error {
	res, err := x.ExecContext(ctx, `UPDATE mission_strategy_epochs SET state=?, judgment_hash=? WHERE session_id=? AND epoch_seq=? AND state=?`,
		string(e.State), judgmentHashArg(e), session, e.Seq, string(from))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrConflict
	}
	return nil
}

func getEpoch(ctx context.Context, q queryer, session, seq int64) (types.Epoch, error) {
	e, err := scanEpoch(session, q.QueryRowContext(ctx, `SELECT `+epochCols+` FROM mission_strategy_epochs WHERE session_id=? AND epoch_seq=?`, session, seq))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// GetEpoch returns one epoch.
func (s *Store) GetEpoch(ctx context.Context, profile string, gen, seq int64) (types.Epoch, error) {
	session, err := sessionOf(profile, gen)
	if err != nil {
		return types.Epoch{}, err
	}
	return getEpoch(ctx, s.db, session, seq)
}

// ListEpochs returns every epoch of the session in sequence order.
func (s *Store) ListEpochs(ctx context.Context, profile string, gen int64) ([]types.Epoch, error) {
	session, err := sessionOf(profile, gen)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+epochCols+` FROM mission_strategy_epochs WHERE session_id=? ORDER BY epoch_seq`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Epoch
	for rows.Next() {
		e, err := scanEpoch(session, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetJudgment returns the stored judgment of an epoch, checking that the body matches its hash and the epoch's.
func (s *Store) GetJudgment(ctx context.Context, profile string, gen, seq int64) (types.JudgmentRecord, error) {
	session, err := sessionOf(profile, gen)
	if err != nil {
		return types.JudgmentRecord{}, err
	}
	r := types.JudgmentRecord{Profile: profile, Generation: gen, EpochSeq: seq}
	var body string
	var bh []byte
	err = s.db.QueryRowContext(ctx, `SELECT body, body_hash FROM mission_strategy_judgments WHERE session_id=? AND epoch_seq=?`, session, seq).Scan(&body, &bh)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Body = []byte(body)
	if r.BodyHash, err = h32(bh); err != nil {
		return r, err
	}
	if r.BodyHash != types.BodyHash(r.Body) {
		return r, fmt.Errorf("%w: the stored judgment does not match its hash", ErrInvalid)
	}
	ep, err := getEpoch(ctx, s.db, session, seq)
	if err != nil {
		return r, err
	}
	if !ep.HasJudgment || ep.JudgmentHash != r.BodyHash {
		return r, fmt.Errorf("%w: the epoch does not carry this judgment", ErrInvalid)
	}
	return r, nil
}

// LatestEpoch is the epoch with the highest sequence; ok is false when there is none. After a restart the epoch engine
// continues from it (an open one is resumed, never re-opened under a new id).
func (s *Store) LatestEpoch(ctx context.Context, profile string, gen int64) (e types.Epoch, ok bool, err error) {
	all, err := s.ListEpochs(ctx, profile, gen)
	if err != nil || len(all) == 0 {
		return e, false, err
	}
	return all[len(all)-1], true, nil
}

// NextEpochSeq is the sequence the next epoch takes: latest+1, or 1.
func (s *Store) NextEpochSeq(ctx context.Context, profile string, gen int64) (int64, error) {
	e, ok, err := s.LatestEpoch(ctx, profile, gen)
	if err != nil || !ok {
		return 1, err
	}
	return e.Seq + 1, nil
}

// ParkedFinishRounds counts the epochs that were started because a finish was parked. Counting stored epochs keeps the
// policy's bound across restarts.
func (s *Store) ParkedFinishRounds(ctx context.Context, profile string, gen int64) (int, error) {
	all, err := s.ListEpochs(ctx, profile, gen)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range all {
		if e.Trigger == types.TriggerFinishParked {
			n++
		}
	}
	return n, nil
}
