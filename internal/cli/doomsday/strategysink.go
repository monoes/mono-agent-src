//go:build doomsday

package doomsday

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/novelty"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/policy"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// ChainTxAppender appends one strategy.* row to the generation's MAC-chained audit table inside the CALLER's transaction,
// so a strategy row and its audit line commit or roll back together. The production one is the store's own exported
// writer (store.SQLStore.AppendStrategyAuditTx, which writes strategy kinds and nothing else). It does not sequence the
// write: every caller runs it inside the witnessed sequence (sstore.Sequencer), because a chained row written outside it
// makes the next admission of the generation fail closed.
type ChainTxAppender interface {
	AppendAuditTx(ctx context.Context, tx sstore.Querier, profile string, generation int64, kind, payload string) error
}

// MaxAuditDetail bounds the payload of a policy line.
const MaxAuditDetail = 2 * types.MaxNoteLen

// auditSink is sstore.AuditSink over a ChainTxAppender.
type auditSink struct{ app ChainTxAppender }

func (a auditSink) Append(ctx context.Context, tx sstore.Tx, profile string, gen int64, kind, payload string) error {
	if !types.IsReserved(kind) {
		return fmt.Errorf("strategy audit: %q is not a strategy kind", kind)
	}
	return a.app.AppendAuditTx(ctx, tx, profile, gen, kind, payload)
}

// NewStrategyAuditSink is the sstore audit sink the store is built with.
func NewStrategyAuditSink(app ChainTxAppender) sstore.AuditSink { return auditSink{app: app} }

// policySink is policy.Sink: it appends a strategy.* line in its own short transaction.
type policySink struct {
	db      sstore.DB
	app     ChainTxAppender
	seq     sstore.Sequencer
	profile string
	gen     int64
}

func (p policySink) Record(ctx context.Context, kind, detail string) error {
	if !types.IsReserved(kind) {
		return fmt.Errorf("strategy audit: %q is not a strategy kind", kind)
	}
	if len(detail) > MaxAuditDetail {
		detail = detail[:MaxAuditDetail]
	}
	write := func(ctx context.Context) error {
		tx, err := p.db.Begin(ctx)
		if err != nil {
			return err
		}
		done := false
		defer func() {
			if !done {
				_ = tx.Rollback() // also when the appender panics: the loop recovers, the write lock must not stay held
			}
		}()
		if err := p.app.AppendAuditTx(ctx, tx, p.profile, p.gen, kind, detail); err != nil {
			return err
		}
		err = tx.Commit()
		done = err == nil
		return err
	}
	if p.seq == nil {
		return errors.New("strategy audit: no witnessed sequence is wired")
	}
	return p.seq.SequenceRows(ctx, p.gen, "strategy", 1, write)
}

// runStateDB answers "is this generation running" from the run table, never from a local flag.
type runStateDB struct {
	db      sstore.Querier
	profile string
}

func (r runStateDB) running(ctx context.Context, gen int64) (bool, error) {
	var state string
	err := r.db.QueryRowContext(ctx, `SELECT state FROM doomsday_runs WHERE profile = ? AND generation = ?`, r.profile, gen).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && state == "running", err
}

// genRun is policy.RunState and epoch.RunState for one generation.
type genRun struct {
	r   runStateDB
	gen int64
}

func (g genRun) Running(ctx context.Context) (bool, error) { return g.r.running(ctx, g.gen) }

// policyRun adapts runStateDB to policy.RunState (which names the profile and generation).
type policyRun struct{ r runStateDB }

func (p policyRun) Running(ctx context.Context, profile string, gen int64) (bool, error) {
	if profile != p.r.profile {
		return false, nil
	}
	return p.r.running(ctx, gen)
}

// SimilarFunc looks memory and knowledge up for text close to the normalised title. It is supplied by the host's
// embedder; nil means the corpus is unreachable (the novelty gate then accepts at most one unchecked proposal per epoch).
type SimilarFunc func(ctx context.Context, normalised string) ([]novelty.Hit, error)

// ErrCorpusUnreachable is the answer of a corpus with no similarity source.
var ErrCorpusUnreachable = errors.New("strategy: memory and knowledge are not reachable")

// ledgerCorpus is novelty.Corpus over the strategy store.
type ledgerCorpus struct {
	st      *sstore.Store
	profile string
	gen     int64
	similar SimilarFunc
}

func (c ledgerCorpus) LedgerKey(ctx context.Context, key [32]byte) (novelty.Match, bool, error) {
	n, ok, err := c.st.FindNovelty(ctx, c.profile, c.gen, key)
	if err != nil || !ok {
		return novelty.Match{}, false, err
	}
	id := n.DirectionID
	if id == "" {
		id = "ledger:" + hex.EncodeToString(key[:6])
	}
	return novelty.Match{ID: id}, true, nil
}

func (c ledgerCorpus) DirectionKey(ctx context.Context, key [32]byte) (novelty.Match, bool, error) {
	all, err := c.st.ListDirections(ctx, c.profile, c.gen)
	if err != nil {
		return novelty.Match{}, false, err
	}
	for _, d := range all {
		if d.KeyHash == key && (policy.AcceptsNewWork(d.State) || d.State == types.DirPruned) {
			return novelty.Match{ID: d.ID}, true, nil
		}
	}
	return novelty.Match{}, false, nil
}

func (c ledgerCorpus) Similar(ctx context.Context, normalised string) ([]novelty.Hit, error) {
	if c.similar == nil {
		return nil, ErrCorpusUnreachable
	}
	return c.similar(ctx, normalised)
}

// randomID is the default direction id source.
func randomID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "d-" + hex.EncodeToString(b[:]), nil
}

// strategySink is ops.StrategySink over the strategy store, the novelty gate and the policy intake.
type strategySink struct{ h *StrategyHost }

var _ ops.StrategySink = strategySink{}

func (s strategySink) Propose(ctx context.Context, p ops.StrategyProposal) (ops.ProposeOutcome, error) {
	h := s.h
	if p.Profile != h.d.Profile {
		return ops.ProposeOutcome{}, sstore.ErrRunNotRunning
	}
	seq, unchecked, err := h.epochContext(ctx, p.Generation)
	if err != nil {
		return ops.ProposeOutcome{}, err
	}
	gate, err := novelty.New(ledgerCorpus{st: h.store, profile: p.Profile, gen: p.Generation, similar: h.d.Similar}, h.d.NoveltyThreshold)
	if err != nil {
		return ops.ProposeOutcome{}, err
	}
	pr := policy.Proposer{Profile: p.Profile, Generation: p.Generation, Gate: gate, Ledger: h.store,
		Run: policyRun{r: h.runs}, NewID: h.d.NewID}
	out, _, err := pr.Submit(ctx, seq, unchecked, []types.Proposal{p.Proposal})
	if err != nil {
		return ops.ProposeOutcome{}, err
	}
	if len(out) != 1 {
		return ops.ProposeOutcome{}, errors.New("strategy: the intake gave no answer")
	}
	in := out[0]
	if in.Err != nil {
		if errors.Is(in.Err, sstore.ErrTampered) || errors.Is(in.Err, sstore.ErrKey) {
			// The run's own ledger or direction table failed verification: nothing is taken in, and the layer is parked
			// (recorded once per call; the ticking host parks on its own next read of the same rows).
			_ = h.policySink(p.Generation).Record(ctx, types.KindParked, "a stored strategy row failed verification at intake")
		}
		return ops.ProposeOutcome{}, in.Err
	}
	return ops.ProposeOutcome{Accepted: in.Accepted(), DirectionID: in.DirectionID, Outcome: in.Outcome, MatchedID: in.MatchedID}, nil
}

func (s strategySink) GetDirection(ctx context.Context, profile string, gen int64, id string) (types.Direction, error) {
	if profile != s.h.d.Profile {
		return types.Direction{}, sstore.ErrNotFound
	}
	return s.h.store.GetDirection(ctx, profile, gen, id)
}

func (s strategySink) MoveDirection(ctx context.Context, profile string, gen int64, id string, to types.DirectionState) error {
	if profile != s.h.d.Profile {
		return sstore.ErrNotFound
	}
	return s.h.store.MoveDirection(ctx, profile, gen, id, to, "")
}

func (s strategySink) EpochExpect(ctx context.Context, profile string, gen, seq int64) (types.Expect, error) {
	if profile != s.h.d.Profile {
		return types.Expect{}, sstore.ErrNotFound
	}
	run, ok, err := s.h.d.Runs.Current(ctx)
	if err != nil {
		return types.Expect{}, err
	}
	if !ok || run.Generation != gen {
		return types.Expect{}, sstore.ErrRunNotRunning
	}
	open, err := s.h.store.OpenDirections(ctx, profile, gen)
	if err != nil {
		return types.Expect{}, err
	}
	exp := types.Expect{CriterionIDs: run.CriterionIDs()}
	for _, d := range open {
		exp.DirectionIDs = append(exp.DirectionIDs, d.ID)
	}
	return exp, nil
}

func (s strategySink) RecordJudgment(ctx context.Context, profile string, gen, seq int64, body []byte) (bool, error) {
	if profile != s.h.d.Profile {
		return false, sstore.ErrNotFound
	}
	return s.h.store.RecordJudgment(ctx, profile, gen, seq, body)
}

func (s strategySink) MarkBadJudgment(ctx context.Context, profile string, gen, seq int64) error {
	if profile != s.h.d.Profile {
		return sstore.ErrNotFound
	}
	return s.h.store.MoveEpoch(ctx, profile, gen, seq, types.EpochBadJudgment)
}

// epochContext is the epoch a proposal belongs to (the latest, 0 when none yet) and how many accepted-unchecked
// proposals that epoch already holds.
func (h *StrategyHost) epochContext(ctx context.Context, gen int64) (seq int64, unchecked int, err error) {
	latest, ok, err := h.store.LatestEpoch(ctx, h.d.Profile, gen)
	if err != nil {
		return 0, 0, err
	}
	if ok {
		seq = latest.Seq
	}
	rows, err := h.store.ListNovelty(ctx, h.d.Profile, gen)
	if err != nil {
		return 0, 0, err
	}
	for _, n := range rows {
		if n.EpochSeq == seq && n.Outcome == types.NoveltyAcceptedUnchecked {
			unchecked++
		}
	}
	return seq, unchecked, nil
}

// NewStoreTxAppender is the ChainTxAppender over the store's exported strategy writer.
func NewStoreTxAppender(st *store.SQLStore) ChainTxAppender { return storeTx{st: st} }

type storeTx struct{ st *store.SQLStore }

func (a storeTx) AppendAuditTx(ctx context.Context, tx sstore.Querier, profile string, gen int64, kind, payload string) error {
	if !types.IsReserved(kind) {
		return fmt.Errorf("strategy audit: %q is not a strategy kind", kind)
	}
	return a.st.AppendStrategyAuditTx(ctx, tx, profile, gen, kind, payload)
}
