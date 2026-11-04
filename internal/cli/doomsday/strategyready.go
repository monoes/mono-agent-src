//go:build doomsday

package doomsday

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/policy"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// The finish-ready reading. Strategy never begins a finish and never acts as the boss: it only READS whether the run looks
// ready, records one marker per judged epoch, and shows the reading (`doomsday strategy status`, the boss's read-only
// tool). The boss decides and calls finish.begin itself, through the ops package's boss check.
//
// What the reading is made of. Derived from model text: only the per-criterion STATUS strings of the latest judgment (and
// which direction ids it covers), after the judge package decoded and validated them against host-built id sets. Not
// derived from model text or from anything a lead or planner can write: the criterion ids (the charter), whether the run
// is running (the run table), the epoch states, start times and counts (MAC-verified rows the host wrote), the open
// direction ids (minted by the host), the finish engine's state and rounds (the audit chain), the subject time, the clock.
// No direction title, rationale, note or next action enters it.

// readyCalc computes the reading from the database. The host and the read-only readers use the same one.
type readyCalc struct {
	st      *sstore.Store
	db      sstore.Querier
	profile string
	cfg     policy.Config
	now     func() time.Time
	subject func(ctx context.Context, gen int64) (time.Time, error)
	runs    StrategyRunSource
}

// auditCount counts the generation's chained audit rows that match the clause.
func (c readyCalc) auditCount(ctx context.Context, gen int64, where string, args ...any) (int, error) {
	var n int
	q := `SELECT COUNT(*) FROM doomsday_audit WHERE profile = ? AND generation = ? AND ` + where
	err := c.db.QueryRowContext(ctx, q, append([]any{c.profile, gen}, args...)...).Scan(&n)
	return n, err
}

// state builds the policy state of a running or stopping run.
func (c readyCalc) state(ctx context.Context, run StrategyRun) (policy.State, error) {
	st := policy.State{RunRunning: run.Running, CriterionIDs: run.CriterionIDs(), Now: c.now()}
	eps, err := c.st.ListEpochs(ctx, c.profile, run.Generation)
	if err != nil {
		return st, err
	}
	for _, e := range eps {
		if e.Trigger == types.TriggerFinishParked {
			st.ParkedRounds++
			st.LastParkedAt = time.Unix(e.StartedAt, 0) // the park preceded the epoch it triggered
		}
	}
	// The LATEST closed epoch decides: an epoch still open is in flight and says nothing, an abandoned one never judged;
	// a bad judgment or an undecodable body invalidates every earlier reading.
	for i := len(eps) - 1; i >= 0; i-- {
		e := eps[i]
		if e.State == types.EpochOpen || e.State == types.EpochAbandoned {
			continue
		}
		if e.State == types.EpochBadJudgment || !e.HasJudgment {
			st.LatestBad = true
			break
		}
		rec, err := c.st.GetJudgment(ctx, c.profile, run.Generation, e.Seq)
		if err != nil {
			return st, err
		}
		j, derr := types.DecodeJudgment(rec.Body)
		if derr != nil {
			st.LatestBad = true // an undecodable body is no judgment: fail closed
			break
		}
		st.Judgment, st.EpochSeq, st.JudgmentHash, st.JudgmentAt = &j, e.Seq, hex.EncodeToString(e.JudgmentHash[:]), time.Unix(e.StartedAt, 0)
		break
	}
	open, err := c.st.OpenDirections(ctx, c.profile, run.Generation)
	if err != nil {
		return st, err
	}
	for _, d := range open {
		st.OpenDirectionIDs = append(st.OpenDirectionIDs, d.ID)
	}
	if c.subject != nil {
		if t, serr := c.subject(ctx, run.Generation); serr == nil {
			st.SubjectKnown, st.SubjectChangedAt = true, t
		}
	}
	n, err := c.auditCount(ctx, run.Generation, `kind = 'finish.freeze'`)
	if err != nil {
		return st, err
	}
	st.FinishInProgress = n > 0
	if n, err = c.auditCount(ctx, run.Generation, `kind = ?`, types.KindFinishGaveUp); err != nil {
		return st, err
	}
	st.GaveUpNotified = n > 0
	if st.Judgment != nil {
		d := c.cfg.Decide(st)
		if d.Ready() {
			n, err = c.auditCount(ctx, run.Generation, `kind = ? AND payload = ?`, types.KindFinishReady, policy.ReadyDetail(d.Reason, st.EpochSeq, st.JudgmentHash))
			if err != nil {
				return st, err
			}
			st.ReadyRecorded = n > 0
		}
	}
	return st, nil
}

// view is the reading for display and for the boss's tool. It writes nothing.
func (c readyCalc) view(ctx context.Context, gen int64) (FinishReadyView, error) {
	run, ok, err := c.runs.Current(ctx)
	if err != nil {
		return FinishReadyView{}, err
	}
	if !ok || (gen != 0 && run.Generation != gen) {
		return FinishReadyView{Reason: string(policy.ReasonRunNotRunning)}, nil
	}
	st, err := c.state(ctx, run)
	if err != nil {
		return FinishReadyView{}, err
	}
	d := c.cfg.Decide(st)
	v := FinishReadyView{Ready: d.Ready(), Reason: string(d.Reason), Action: string(d.Action), Recorded: st.ReadyRecorded, GaveUp: d.Action == policy.ActionGiveUp}
	if st.Judgment != nil {
		v.Epoch = st.EpochSeq
		v.JudgmentAgeSeconds = int64(st.Now.Sub(st.JudgmentAt) / time.Second)
	}
	if !v.Ready {
		v.Recorded = false
	}
	return v, nil
}

// FinishReady is the boss's read-only reading of the run it leads, for the verified top lead `session` of the profile. A
// session that is not the recorded boss of the open run reads nothing (ErrNotBoss), and with no running run the answer is
// the mode being off. The reading carries no authority: finish.begin is a separate call that checks the boss itself.
func (w Wiring) FinishReady(ctx context.Context, session int64) (FinishReadyView, error) {
	if w.Witness == nil || w.DB == nil || w.Store == nil {
		return FinishReadyView{}, errors.New("strategy: the witness, the database and the store are required")
	}
	src := StoreRunSource{Profile: w.Profile, Runs: w.Store, Bosses: BossSource{Wiring: w}, DB: w.DB}
	run, ok, err := src.Current(ctx)
	if err != nil {
		return FinishReadyView{}, err
	}
	if !ok || !run.Running {
		return FinishReadyView{}, ErrReadyNoRun
	}
	if !run.HasBoss || run.Boss != session {
		return FinishReadyView{}, ErrReadyNotBoss
	}
	return w.readyCalc(src).view(ctx, run.Generation)
}

// Errors of FinishReady.
var (
	ErrReadyNoRun   = errors.New("strategy: no run is running")
	ErrReadyNotBoss = errors.New("strategy: only the run's boss reads the finish-ready state")
)

func (w Wiring) readyCalc(src StrategyRunSource) readyCalc {
	return readyCalc{st: sstore.New(sstore.WrapDB(w.DB), w.Witness.Keys, nil), db: w.DB, profile: w.Profile, now: w.clock().Now,
		subject: w.subjectChangedAtOn(w.DB), runs: src}
}
