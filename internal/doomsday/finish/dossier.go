package finish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// The rubric workflow of note 13.6 section 10. A rubric needs a person: the dossier is what the person is shown, its hash
// binds the decision to exactly that, and the decision is a row only an operator verb writes. No timeout, no default.

// ErrRubric: the rubric decision was refused; the message says why.
var ErrRubric = errors.New("finish: the rubric decision is refused")

// Dossier is one pending rubric as the person sees it.
type Dossier struct {
	ID        string
	Statement string
	Text      string
	Hash      string // full hex
}

func (h *Harness) pendingRubrics(f Facts) []string {
	var out []string
	for _, c := range f.Charter.Criteria {
		if !c.Rubric {
			continue
		}
		if a, r := f.decisionsFor(c); !a && !r {
			out = append(out, c.ID)
		}
	}
	return out
}

// dossier builds the text and hash a person decides on: the statement (from the charter), the round and its digest,
// the machine results, and the verifier's closed finding under its pin.
func (h *Harness) dossier(f Facts, c Criterion) Dossier {
	i := criterionIndex(f, c)
	statement := h.Specs[i].Spec
	var b strings.Builder
	fmt.Fprintf(&b, "Rubric %s, round %d, freeze point %d\nSubject digest: %s\nStatement: %s\n", c.ID, f.Round.N, f.Round.FreezeSeq, f.Round.Digest, statement)
	b.WriteString("Machine results of this round:\n")
	for _, e := range f.roundEvidence() {
		fmt.Fprintf(&b, "  %s: %s (exit %d) stdout %s\n", e.CriterionID, e.Result, e.ExitCode, e.StdoutDigest)
	}
	if rv, ok := lastCompleteReview(f); ok {
		fmt.Fprintf(&b, "Verifier finding (%s/%s %s): %s, reason %s\n", rv.Observed.Provider, rv.Observed.Model, rv.Observed.Version, rv.Verdict, rv.ReasonCode)
	} else {
		b.WriteString("Verifier finding: none yet\n")
	}
	sum := sha256.Sum256([]byte("monoagent-finish-dossier-v1\x00" + b.String()))
	return Dossier{ID: c.ID, Statement: statement, Text: b.String(), Hash: hex.EncodeToString(sum[:])}
}

func lastCompleteReview(f Facts) (Review, bool) {
	var out Review
	found := false
	for _, r := range f.Reviews {
		if f.reviewBound(r) && r.State == ReviewComplete {
			out, found = r, true
		}
	}
	return out, found
}

// Dossiers lists the rubrics of the current round that have no decision. The finish must be verifying.
func (h *Harness) Dossiers(ctx context.Context) ([]Dossier, error) {
	f, err := h.Ledger.Facts(ctx)
	if err != nil {
		return nil, err
	}
	if f.State != StateVerifying {
		return nil, fmt.Errorf("%w: the finish is %s, not verifying", ErrRubric, f.State)
	}
	var out []Dossier
	for _, c := range f.Charter.Criteria {
		if c.Rubric {
			if a, r := f.decisionsFor(c); !a && !r {
				out = append(out, h.dossier(f, c))
			}
		}
	}
	return out, nil
}

// Decide records the person's decision on one rubric. dossierHash must be the hash (or an 8-or-more hex prefix of it) of
// the dossier as it is NOW: a decision made on a dossier that has changed is refused. The subject is re-digested before
// the row is written; a changed subject parks the finish instead. A second decision for the same rubric is refused. A
// reject parks the finish (rubric-rejected); an approve never finishes by itself.
func (h *Harness) Decide(ctx context.Context, id string, approve bool, dossierHash, note string) error {
	f, err := h.Ledger.Facts(ctx)
	if err != nil {
		return err
	}
	if f.State != StateVerifying {
		return fmt.Errorf("%w: the finish is %s, not verifying", ErrRubric, f.State)
	}
	var crit *Criterion
	for i := range f.Charter.Criteria {
		if f.Charter.Criteria[i].ID == id && f.Charter.Criteria[i].Rubric {
			crit = &f.Charter.Criteria[i]
		}
	}
	if crit == nil {
		return fmt.Errorf("%w: %q is not a rubric criterion", ErrRubric, id)
	}
	if a, r := f.decisionsFor(*crit); a || r {
		return fmt.Errorf("%w: %s already has a decision in this round", ErrRubric, id)
	}
	d := h.dossier(f, *crit)
	if len(dossierHash) < 8 || !strings.HasPrefix(d.Hash, strings.ToLower(dossierHash)) {
		return fmt.Errorf("%w: the dossier no longer matches what was shown (now %s)", ErrRubric, d.Hash[:16])
	}
	live, err := DigestTree(h.Subject, "")
	if err != nil || live.Digest != f.Round.Digest {
		v := Verdict{Park, ReasonSubjectChanged, "the live subject changed before the rubric decision was written"}
		if perr := h.Ledger.Park(ctx, v); perr != nil {
			return perr
		}
		return fmt.Errorf("%w: the subject changed; the finish is parked", ErrRubric)
	}
	if len(note) > 200 {
		note = note[:200]
	}
	if err := h.Ledger.RecordDecision(ctx, RubricDecision{Round: f.Round.N, FreezeSeq: f.Round.FreezeSeq, Digest: f.Round.Digest,
		CriterionID: crit.ID, Approve: approve, DossierHash: d.Hash, Note: note}); err != nil {
		return err
	}
	if !approve {
		return h.Ledger.Park(ctx, Verdict{Park, ReasonRubricRejected, crit.ID})
	}
	return nil
}

// Abort ends the finish at an operator Stop (note 13.6 section 7.1): nothing can commit or park afterwards.
func (h *Harness) Abort(ctx context.Context, by string) error { return h.Ledger.Abort(ctx, by) }
