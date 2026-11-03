package sstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

const epochCols = `epoch_seq, trigger, started_at, judgment_hash, model_pin, state, mac`

func scanEpoch(profile string, gen int64, sc interface{ Scan(...any) error }) (types.Epoch, []byte, error) {
	e := types.Epoch{Profile: profile, Generation: gen}
	var trig, state string
	var jh, mac []byte
	if err := sc.Scan(&e.Seq, &trig, &e.StartedAt, &jh, &e.ModelPin, &state, &mac); err != nil {
		return e, nil, err
	}
	e.Trigger, e.State = types.Trigger(trig), types.EpochState(state)
	if len(jh) > 0 {
		var err error
		if e.JudgmentHash, err = h32(jh); err != nil {
			return e, nil, err
		}
		e.HasJudgment = true
	}
	return e, mac, nil
}

func judgmentHashArg(e types.Epoch) []byte {
	if !e.HasJudgment {
		return []byte{}
	}
	return e.JudgmentHash[:]
}

// InsertEpoch opens an epoch (state open) and appends the epoch-begin audit line. The identity is
// (profile, generation, seq), so a restart that repeats the call is idempotent: when the epoch exists with the same
// trigger, start time and model pin, the stored epoch is returned with created=false and nothing is written; any
// other content under the same identity is ErrConflict.
func (s *Store) InsertEpoch(ctx context.Context, e types.Epoch) (types.Epoch, bool, error) {
	if err := e.Validate(); err != nil {
		return e, false, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if e.State != types.EpochOpen || e.HasJudgment {
		return e, false, fmt.Errorf("%w: a new epoch is open and has no judgment", ErrInvalid)
	}
	var out types.Epoch
	created := false
	err := s.inTx(ctx, e.Generation, 1, func(tx Tx) error {
		have, err := s.getEpoch(ctx, tx, e.Profile, e.Generation, e.Seq)
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
		mac, err := s.mac(e.Canonical())
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO doomsday_strategy_epochs(profile, generation, `+epochCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
			e.Profile, e.Generation, e.Seq, string(e.Trigger), e.StartedAt, judgmentHashArg(e), e.ModelPin, string(e.State), mac); err != nil {
			return mapErr(err)
		}
		if err := s.appendAudit(ctx, tx, e.Profile, e.Generation, types.KindEpochBegin,
			payload(map[string]string{"epoch": types.EpochID(e.Profile, e.Generation, e.Seq), "trigger": string(e.Trigger), "model": e.ModelPin})); err != nil {
			return err
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
// hash, in one transaction with the judgment audit line. The body hash is recomputed here, never trusted. A repeat with
// the same body for an already judged epoch is idempotent (created=false); a different body is ErrConflict.
func (s *Store) RecordJudgment(ctx context.Context, profile string, gen, seq int64, body []byte) (bool, error) {
	if len(body) == 0 || len(body) > types.MaxJudgmentBytes {
		return false, fmt.Errorf("%w: judgment body size %d", ErrInvalid, len(body))
	}
	rec := types.JudgmentRecord{Profile: profile, Generation: gen, EpochSeq: seq, Body: body, BodyHash: types.BodyHash(body)}
	created := false
	err := s.inTx(ctx, gen, 1, func(tx Tx) error {
		if err := requireRunning(ctx, tx, profile, gen); err != nil {
			return err
		}
		ep, err := s.getEpoch(ctx, tx, profile, gen, seq)
		if err != nil {
			return err
		}
		if ep.State != types.EpochOpen {
			if ep.HasJudgment && ep.JudgmentHash == rec.BodyHash {
				return nil
			}
			if ep.HasJudgment {
				return ErrConflict
			}
			return ErrNeedsOpenEpoch
		}
		jmac, err := s.mac(rec.Canonical())
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO doomsday_strategy_judgments(profile, generation, epoch_seq, body, body_hash, mac) VALUES (?,?,?,?,?,?)`,
			profile, gen, seq, string(body), rec.BodyHash[:], jmac); err != nil {
			return mapErr(err)
		}
		ep.State, ep.HasJudgment, ep.JudgmentHash = types.EpochJudged, true, rec.BodyHash
		if err := s.updateEpoch(ctx, tx, ep, types.EpochOpen); err != nil {
			return err
		}
		created = true
		return s.appendAudit(ctx, tx, profile, gen, types.KindJudgment,
			payload(map[string]string{"epoch": types.EpochID(profile, gen, seq), "body": hx(rec.BodyHash)}))
	})
	return created, err
}

// MoveEpoch moves an epoch to bad-judgment, applied or abandoned (judged is reached only through RecordJudgment). The
// run must be running, except for abandoned: an epoch left open by a Stop is closed after the run stopped.
func (s *Store) MoveEpoch(ctx context.Context, profile string, gen, seq int64, to types.EpochState) error {
	if to == types.EpochJudged || to == types.EpochOpen {
		return fmt.Errorf("%w: use RecordJudgment to judge an epoch", ErrInvalid)
	}
	return s.inTx(ctx, gen, 1, func(tx Tx) error {
		if to != types.EpochAbandoned {
			if err := requireRunning(ctx, tx, profile, gen); err != nil {
				return err
			}
		}
		ep, err := s.getEpoch(ctx, tx, profile, gen, seq)
		if err != nil {
			return err
		}
		if !types.CanMoveEpoch(ep.State, to) {
			return ErrBackwardMove
		}
		from := ep.State
		ep.State = to
		if err := s.updateEpoch(ctx, tx, ep, from); err != nil {
			return err
		}
		kind := types.KindEpochEnd
		if to == types.EpochBadJudgment {
			kind = types.KindBadJudgment
		}
		return s.appendAudit(ctx, tx, profile, gen, kind,
			payload(map[string]string{"epoch": types.EpochID(profile, gen, seq), "from": string(from), "to": string(to)}))
	})
}

func (s *Store) updateEpoch(ctx context.Context, tx Tx, e types.Epoch, from types.EpochState) error {
	mac, err := s.mac(e.Canonical())
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE doomsday_strategy_epochs SET state=?, judgment_hash=?, mac=? WHERE profile=? AND generation=? AND epoch_seq=? AND state=?`,
		string(e.State), judgmentHashArg(e), mac, e.Profile, e.Generation, e.Seq, string(from))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) getEpoch(ctx context.Context, q Querier, profile string, gen, seq int64) (types.Epoch, error) {
	e, mac, err := scanEpoch(profile, gen, q.QueryRowContext(ctx, `SELECT `+epochCols+` FROM doomsday_strategy_epochs WHERE profile=? AND generation=? AND epoch_seq=?`, profile, gen, seq))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	return e, s.verify(e.Canonical(), mac)
}

// GetEpoch returns one verified epoch.
func (s *Store) GetEpoch(ctx context.Context, profile string, gen, seq int64) (types.Epoch, error) {
	return s.getEpoch(ctx, s.db, profile, gen, seq)
}

// ListEpochs returns every epoch of the run, verified, in sequence order.
func (s *Store) ListEpochs(ctx context.Context, profile string, gen int64) ([]types.Epoch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+epochCols+` FROM doomsday_strategy_epochs WHERE profile=? AND generation=? ORDER BY epoch_seq`, profile, gen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Epoch
	for rows.Next() {
		e, mac, err := scanEpoch(profile, gen, rows)
		if err != nil {
			return nil, err
		}
		if err := s.verify(e.Canonical(), mac); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetJudgment returns the stored judgment of an epoch after verifying its MAC and that the body matches its hash.
func (s *Store) GetJudgment(ctx context.Context, profile string, gen, seq int64) (types.JudgmentRecord, error) {
	r := types.JudgmentRecord{Profile: profile, Generation: gen, EpochSeq: seq}
	var body string
	var bh, mac []byte
	err := s.db.QueryRowContext(ctx, `SELECT body, body_hash, mac FROM doomsday_strategy_judgments WHERE profile=? AND generation=? AND epoch_seq=?`, profile, gen, seq).Scan(&body, &bh, &mac)
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
	if want := types.BodyHash(r.Body); !bytes.Equal(want[:], bh) {
		return r, ErrTampered
	}
	if err := s.verify(r.Canonical(), mac); err != nil {
		return r, err
	}
	ep, err := s.getEpoch(ctx, s.db, profile, gen, seq)
	if err != nil {
		return r, err
	}
	if !ep.HasJudgment || ep.JudgmentHash != r.BodyHash {
		return r, ErrTampered
	}
	return r, nil
}
