//go:build doomsday

package doomsday

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/monomind"
)

// The production construction of the strategy layer over a wiring. Nothing here starts anything by itself: the host is
// ticked by the engine's loop (cmd/monoagentcli), and the operations are taken by the boss tools of the MCP server. With
// no run open, a tick reads one query and does nothing.

// witnessSeq is sstore.Sequencer over the profile's sequencer. A strategy write is an ordinary transition: it is never
// an ending one and never ends a generation, whatever the caller asks.
type witnessSeq struct{ s *stop.Sequencer }

func (w witnessSeq) SequenceRows(ctx context.Context, gen int64, kind string, rows int, fn func(context.Context) error) error {
	return w.s.SequenceRows(ctx, gen, kind, false, false, rows, fn)
}

// WitnessSequencer adapts the stop package's sequencer for the strategy store.
func WitnessSequencer(s *stop.Sequencer) sstore.Sequencer { return witnessSeq{s: s} }

// runnerJudgeModel is judge.Model over the product's monomind runner (the same path as the finish verifier): one turn, no
// tools, the pinned model of the run's charter.
type runnerJudgeModel struct {
	pin       StrategyPin
	exec      func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error)
	workspace func() (string, error)
	timeout   time.Duration
	budgetUSD float64
}

func (m runnerJudgeModel) Pin() string { return m.pin.String() }

func (m runnerJudgeModel) Complete(ctx context.Context, req judge.Request) ([]byte, error) {
	cwd, err := m.workspace()
	if err != nil {
		return nil, err
	}
	// The reply is bounded while it streams: past the cap the turn is cancelled, so an oversized answer is never held
	// whole. The cap is the judgment's own (judge.ReadReply applies it to what is left).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var seen int
	over := false
	res, err := m.exec(ctx, monomind.ExecOptions{
		Runtime: m.pin.Provider, Model: m.pin.Model, Prompt: req.User, SystemPrompt: req.System,
		Cwd: cwd, Timeout: m.timeout, BudgetUSD: m.budgetUSD, MaxTurns: 1, Sandbox: monomind.TurnSandboxMode,
	}, func(ev monomind.Event) {
		if seen += len(ev.Text); seen > types.MaxJudgmentBytes && !over {
			over = true
			cancel()
		}
	})
	if over {
		return nil, judge.ErrReplyTooLong
	}
	if err != nil {
		return nil, err
	}
	if res == nil || res.Err != nil || !res.SawDone || res.ExitCode != 0 {
		return nil, errors.New("the judging turn did not complete")
	}
	if len(res.ResultText) > types.MaxJudgmentBytes {
		return nil, judge.ErrReplyTooLong
	}
	return judge.ReadReply(strings.NewReader(res.ResultText))
}

// StrategyExtras are the seams a test replaces; the zero value is production.
type StrategyExtras struct {
	// JudgeExec is monomind.Exec; tests give a fake.
	JudgeExec func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error)
	Clock     epoch.Clock
	// Similar is the memory and knowledge lookup of the novelty gate. Nil is the production default today: the corpus is
	// then unreachable and the gate accepts at most one unchecked proposal per epoch (Q4), still ledgered.
	Similar SimilarFunc
}

// StrategyDeps builds the dependencies of the strategy host over the wiring: the witnessed sequence, the store's strategy
// writer, the run source with its boss registry, and the judge on the runner. A wiring without a witness has no strategy
// layer (the error says why).
func (w Wiring) StrategyHostDeps(ctx context.Context, x StrategyExtras) (StrategyHostDeps, error) {
	if w.Witness == nil || w.DB == nil || w.Store == nil {
		return StrategyHostDeps{}, errors.New("strategy: the witness, the database and the store are required")
	}
	seq, err := w.Sequencer(ctx)
	if err != nil {
		return StrategyHostDeps{}, err
	}
	exec := x.JudgeExec
	if exec == nil {
		exec = monomind.Exec
	}
	clock := x.Clock
	if clock == nil {
		clock = epoch.RealClock{}
	}
	d := StrategyHostDeps{
		DB: w.DB, Keys: w.Witness.Keys, Audit: NewStoreTxAppender(w.Store), Seq: WitnessSequencer(seq), Profile: w.Profile,
		Runs:  StoreRunSource{Profile: w.Profile, Runs: w.Store, Bosses: BossSource{Wiring: w}, DB: w.DB},
		Clock: clock, Timeout: 3 * time.Minute,
		ModelFor: func(run StrategyRun) judge.Model {
			if run.Judge.String() == "" {
				return nil
			}
			return runnerJudgeModel{pin: run.Judge, exec: exec, timeout: 3 * time.Minute, budgetUSD: 0.5,
				workspace: func() (string, error) { return monomind.SandboxWorkspaceDir("doomsday-judge") }}
		},
		// The judge may equal the boss (Q2), never the finish verifier.
		RefuseFor: func(run StrategyRun) func(string) bool {
			v := run.Verifier.String()
			return func(pin string) bool { return v == "" || pin == v }
		},
		SubjectChangedAt: w.subjectChangedAt,
		Similar:          x.Similar,
	}
	return d, nil
}

// subjectChangedAt is the last change of the verified subject. While a run is running there is no freeze (a freeze row
// exists only after finish.begin has stopped the run), so the subject has not changed since the run began and the answer is
// the zero time. A generation that already has a freeze row answers "now": every judgment is then older than the subject,
// which makes the policy wait.
func (w Wiring) subjectChangedAt(ctx context.Context, gen int64) (time.Time, error) {
	return w.subjectChangedAtOn(w.DB)(ctx, gen)
}

// subjectChangedAtOn is subjectChangedAt over a given database handle (the read-only verbs open their own).
func (w Wiring) subjectChangedAtOn(db *sql.DB) func(ctx context.Context, gen int64) (time.Time, error) {
	return func(ctx context.Context, gen int64) (time.Time, error) {
		return subjectChanged(ctx, db, w.Profile, gen)
	}
}

func subjectChanged(ctx context.Context, db *sql.DB, profile string, gen int64) (time.Time, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_audit WHERE profile = ? AND generation = ? AND kind = 'finish.freeze'`, profile, gen).Scan(&n)
	if err != nil {
		return time.Time{}, err
	}
	if n > 0 {
		return time.Now(), nil
	}
	return time.Time{}, nil
}

// StrategyHost is the ticking host over the wiring. It holds no finish engine and calls none: all it can do about a finish
// is record the ready marker (strategyready.go); the boss's own finish.begin is the only way one starts.
func (w Wiring) StrategyHost(ctx context.Context, x StrategyExtras) (*StrategyHost, error) {
	d, err := w.StrategyHostDeps(ctx, x)
	if err != nil {
		return nil, err
	}
	return NewStrategyHost(d)
}

// StrategyReader is the read-only verbs' reader over the wiring's database. It needs no witness and writes nothing.
func (w Wiring) StrategyReader(db *sql.DB, keys sstore.Keyer) StrategyReader {
	r := NewStrategyReader(sstore.New(sstore.WrapDB(db), keys, nil), w.Store, w.Profile).(strategyReader)
	src := StoreRunSource{Profile: w.Profile, Runs: w.Store, Bosses: BossSource{Wiring: w}, DB: db}
	c := readyCalc{st: r.st, db: db, profile: w.Profile, now: w.clock().Now, subject: w.subjectChangedAtOn(db), runs: src}
	return r.withReady(c)
}

// BossStrategy runs one session-callable strategy operation for the verified lead session of the profile (the boss tools'
// body). With no running run it is refused as ops.ErrNoCharter and nothing is written. The operation is taken by
// ops.StrategyOps, whose own allowlist (AllowedStrategy) is the only one it reads: finish.begin, strategy.epoch and every
// unknown name are refused there.
func (w Wiring) BossStrategy(ctx context.Context, session int64, op string, args json.RawMessage) (ops.StrategyResult, error) {
	return w.BossStrategyWith(ctx, StrategyExtras{}, session, op, args)
}

// BossStrategyWith is BossStrategy with the seams of the host replaced (tests).
func (w Wiring) BossStrategyWith(ctx context.Context, x StrategyExtras, session int64, op string, args json.RawMessage) (ops.StrategyResult, error) {
	o := w.Ops(nil)
	act, ok, err := o.Adapter.Active(ctx, w.Profile)
	if err != nil {
		return ops.StrategyResult{}, err
	}
	if !ok {
		return ops.StrategyResult{}, ops.ErrNoCharter
	}
	d, err := w.StrategyHostDeps(ctx, x)
	if err != nil {
		return ops.StrategyResult{}, err
	}
	h, err := NewStrategyOpsHost(d)
	if err != nil {
		return ops.StrategyResult{}, err
	}
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ops.StrategyResult{}, fmt.Errorf("a call id cannot be made: %w", err)
	}
	so := &ops.StrategyOps{E: o.Enforcer, Sink: h.Sink()}
	return so.Handle(ctx, ops.StrategyRequest{Profile: w.Profile, CharterID: act.ID, Hash: act.Hash, Generation: act.Generation, Op: op,
		CallID: hex.EncodeToString(id[:]), Caller: ops.Caller{Session: session}, Addressed: session, Args: args})
}
