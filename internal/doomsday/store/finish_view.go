package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// The finish of note 13.6 lives in the chained audit stream. A run that stopped with cause=finish and is quiescent is
// "verifying" once a `finish.freeze` row exists; evidence, rubric decisions, reviews and rechecks are `finish.*` rows
// bound to the freeze; `finish.commit` and `finish.park` close the round. Nothing here changes the run's state machine
// (migration 153 keeps its five states), so the finish states are derived from the rows and are as verifiable as the chain.

const (
	kindFinishFreeze   = "finish.freeze"
	kindFinishEvidence = "finish.evidence"
	kindFinishDecision = "finish.decision"
	kindFinishReview   = "finish.review"
	kindFinishRecheck  = "finish.recheck"
	kindFinishCommit   = "finish.commit"
	kindFinishPark     = "finish.park"
	kindFinishStop     = "finish.stop"

	// MaxFinishRounds bounds the verification rounds of one generation (note 13.6 section 7.4).
	MaxFinishRounds = 3
	// FinishCause is the cause the Stop intent carries when the stop is a finish.
	FinishCause = "finish"
	// StateStopped is the finish state after an operator Stop during verification.
	StateStopped = "stopped"
)

type txq interface {
	DBTX
	querier
}

type freezeRow struct {
	Seq      int64  `json:"-"`
	Round    int64  `json:"round"`
	Digest   string `json:"digest"`
	Snapshot string `json:"snapshot"`
}

type parkRow struct {
	Seq    int64  `json:"-"`
	Round  int64  `json:"round"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

type recheckRow struct {
	Seq   int64 `json:"-"`
	Round int64 `json:"round"`
	OK    bool  `json:"ok"`
}

// finishView is everything the finish reads, taken from one verified read of the chain.
type finishView struct {
	run      Run
	charter  Charter
	head     int64
	closeSeq int64
	freezes  []freezeRow
	parks    []parkRow
	rechecks []recheckRow
	commit   bool
	stopped  bool
	facts    finish.Facts
}

func criterionID(i int) string { return "c" + strconv.Itoa(i) }

// CriterionID is the id the finish gives the i-th criterion of a charter (its position); the harness binds evidence to it.
func CriterionID(i int) string { return criterionID(i) }

// CriterionHash is the domain-separated hash of one criterion.
func CriterionHash(c Criterion) string {
	h := sha256.Sum256([]byte("monoes/doomsday/criterion/v1\x00" + c.Kind + "\x00" + c.Spec + "\x00" + c.Expected))
	return hex.EncodeToString(h[:])
}

func finishPin(p Pin) finish.Pin {
	return finish.Pin{Provider: p.Provider, Model: p.Model, Version: p.Version, PolicyHash: p.PurposePolicy}
}

// finishCharter is the part of the charter the finish reads. The charter has no roster of work-session models, so the
// roster check of CannotFinish is not applied here (see the package notes).
func finishCharter(c Charter) finish.Charter {
	h := c.Hash()
	out := finish.Charter{Hash: hex.EncodeToString(h[:]), Verifier: finishPin(c.VerifierPin), Boss: finishPin(c.BossPin), MaxRounds: MaxFinishRounds}
	for i, cr := range c.Criteria {
		out.Criteria = append(out.Criteria, finish.Criterion{ID: criterionID(i), Hash: CriterionHash(cr), Rubric: cr.Kind == "rubric"})
	}
	return out
}

func unmarshalRow[T any](r chainRow, into *T) error {
	if json.Unmarshal([]byte(r.Payload), into) != nil {
		return fmt.Errorf("%w: %s row %d does not parse", ErrUnverifiable, r.Kind, r.Seq)
	}
	return nil
}

// loadFinishView verifies the chain and the run and charter rows and builds the facts. Any row that does not verify or
// parse fails the whole read: a finish is never decided on facts that were not checked.
func (s *SQLStore) loadFinishView(ctx context.Context, q txq, profile string, generation int64) (*finishView, error) {
	run, err := getRun(ctx, q, s, profile, generation)
	if err != nil {
		return nil, err
	}
	c, err := loadCharterRow(ctx, q, s, profile, generation)
	if err != nil {
		return nil, err
	}
	rows, err := s.readChain(ctx, q, profile, generation)
	if err != nil {
		return nil, err
	}
	v := &finishView{run: run, charter: c}
	v.facts = finish.Facts{Generation: generation, Charter: finishCharter(c)}
	for _, r := range rows {
		v.head = r.Seq
		if err := v.take(r); err != nil {
			return nil, err
		}
	}
	for _, r := range rows {
		if r.Seq > v.closeSeq && v.closeSeq > 0 && WorkAdmittingKind(r.Kind) {
			v.facts.WorkRowsAfterClose++
		}
	}
	v.derive()
	return v, nil
}

// take reads one row into the view.
func (v *finishView) take(r chainRow) error {
	f := &v.facts
	switch r.Kind {
	case "run.advance":
		if v.closeSeq == 0 && strings.HasPrefix(r.Payload, "running>stop-requested") {
			v.closeSeq = r.Seq
		}
	case kindFinishFreeze:
		fr := freezeRow{Seq: r.Seq}
		if err := unmarshalRow(r, &fr); err != nil {
			return err
		}
		v.freezes = append(v.freezes, fr)
	case kindFinishEvidence:
		var e finish.Evidence
		if err := unmarshalRow(r, &e); err != nil {
			return err
		}
		e.Seq = r.Seq
		f.Evidence = append(f.Evidence, e)
	case kindFinishDecision:
		var d finish.RubricDecision
		if err := unmarshalRow(r, &d); err != nil {
			return err
		}
		f.Decisions = append(f.Decisions, d)
	case kindFinishReview:
		var rv finish.Review
		if err := unmarshalRow(r, &rv); err != nil {
			return err
		}
		rv.Seq = r.Seq
		f.Reviews = append(f.Reviews, rv)
	case kindFinishRecheck:
		rc := recheckRow{Seq: r.Seq}
		if err := unmarshalRow(r, &rc); err != nil {
			return err
		}
		v.rechecks = append(v.rechecks, rc)
	case kindFinishPark:
		p := parkRow{Seq: r.Seq}
		if err := unmarshalRow(r, &p); err != nil {
			return err
		}
		v.parks = append(v.parks, p)
	case kindFinishCommit:
		v.commit = true
	case kindFinishStop:
		v.stopped = true
	}
	return nil
}

// derive fills the state, the round and the recheck flag.
func (v *finishView) derive() {
	f := &v.facts
	f.State = string(v.run.State)
	if len(v.freezes) > 0 {
		fz := v.freezes[len(v.freezes)-1]
		f.Round = finish.Round{N: fz.Round, FreezeSeq: fz.Seq, Digest: fz.Digest, CloseSeq: v.closeSeq}
	}
	finishing := v.run.State == StateQuiescent && v.run.Cause == FinishCause && len(v.freezes) > 0
	switch {
	case v.commit:
		f.State = finish.StateFinished
	case v.stopped:
		f.State = StateStopped
	case finishing && len(v.parks) > 0 && v.parks[len(v.parks)-1].Seq > f.Round.FreezeSeq:
		f.State = finish.StateParked
		f.ParkReason = finish.Reason(v.parks[len(v.parks)-1].Reason)
	case finishing:
		f.State = finish.StateVerifying
	}
	var lastEvidence int64
	for _, e := range f.Evidence {
		if !e.Advisory && e.Round == f.Round.N && e.FreezeSeq == f.Round.FreezeSeq && e.Seq > lastEvidence {
			lastEvidence = e.Seq
		}
	}
	for _, rc := range v.rechecks {
		if rc.Round == f.Round.N {
			f.RecheckOK = rc.OK && rc.Seq > lastEvidence // the last recheck of the round decides
		}
	}
}
