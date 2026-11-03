package finish

import "fmt"

func (f Facts) evidenceFor(c Criterion) []Evidence {
	var out []Evidence
	for _, e := range f.Evidence {
		if e.Generation == f.Generation && e.CharterHash == f.Charter.Hash && e.Round == f.Round.N &&
			e.FreezeSeq == f.Round.FreezeSeq && e.Digest == f.Round.Digest &&
			e.CriterionID == c.ID && e.CriterionHash == c.Hash && !e.Advisory {
			out = append(out, e)
		}
	}
	return out
}

func (f Facts) decisionsFor(c Criterion) (approve, reject bool) {
	for _, d := range f.Decisions {
		if d.Generation == f.Generation && d.CharterHash == f.Charter.Hash && d.Round == f.Round.N &&
			d.FreezeSeq == f.Round.FreezeSeq && d.Digest == f.Round.Digest &&
			d.CriterionID == c.ID && d.CriterionHash == c.Hash {
			if d.Approve {
				approve = true
			} else {
				reject = true
			}
		}
	}
	return approve, reject
}

// roundEvidence is the machine evidence of the current round that the review has to cover.
func (f Facts) roundEvidence() []Evidence {
	var out []Evidence
	for _, c := range f.Charter.Criteria {
		if !c.Rubric {
			out = append(out, f.evidenceFor(c)...)
		}
	}
	return out
}

func (f Facts) reviewBound(r Review) bool {
	return r.Generation == f.Generation && r.CharterHash == f.Charter.Hash && r.Round == f.Round.N &&
		r.FreezeSeq == f.Round.FreezeSeq && r.Digest == f.Round.Digest
}

// Evaluate is the finish-commit predicate of note 13.6 section 7.2, with the review condition of the 13.6 P1:
// a completed verifier review of THIS round, bound to this freeze point and digest and to the results it read,
// observed under the charter's pin, with no blocker in the round. Parks beat waits; Commit only when nothing
// is missing and nothing blocks. The ledger runs the same predicate inside the conditional insert.
func Evaluate(f Facts) Verdict {
	if f.State != StateVerifying {
		return Verdict{Park, ReasonInternalError, "the run is not verifying: " + f.State}
	}
	if why := f.Charter.CannotFinish(); why != "" {
		return Verdict{Park, ReasonInternalError, why}
	}
	if f.WorkRowsAfterClose > 0 {
		return Verdict{Park, ReasonInternalError, fmt.Sprintf("%d work-admitting rows after the close point", f.WorkRowsAfterClose)}
	}
	var parks []Verdict
	var wait *Verdict
	setWait := func(r Reason, d string) {
		if wait == nil {
			wait = &Verdict{Wait, r, d}
		}
	}
	for _, c := range f.Charter.Criteria {
		if c.Rubric {
			approve, reject := f.decisionsFor(c)
			switch {
			case reject:
				parks = append(parks, Verdict{Park, ReasonRubricRejected, c.ID})
			case !approve:
				setWait(WaitRubric, c.ID)
			}
			continue
		}
		rows := f.evidenceFor(c)
		switch {
		case len(rows) == 0:
			setWait(WaitEvidence, c.ID)
			continue
		case len(rows) > 1:
			parks = append(parks, Verdict{Park, ReasonInternalError, "more than one evidence row for " + c.ID})
			continue
		}
		if rows[0].Verifier != f.Charter.Verifier {
			parks = append(parks, Verdict{Park, ReasonVerifierChanged, "evidence row of " + c.ID + " carries another verifier pin"})
			continue
		}
		switch rows[0].Result {
		case "pass":
		case "fail":
			parks = append(parks, Verdict{Park, ReasonCriteriaFailed, c.ID})
		case "tainted":
			parks = append(parks, Verdict{Park, ReasonTainted, c.ID})
		case "tampered":
			parks = append(parks, Verdict{Park, ReasonTampered, c.ID})
		default:
			parks = append(parks, Verdict{Park, ReasonCriterionError, c.ID + ": " + rows[0].Result})
		}
	}
	if v, ok := f.reviewVerdict(); ok {
		if v.Outcome == Park {
			parks = append(parks, v)
		} else {
			setWait(v.Reason, v.Detail)
		}
	}
	for _, sev := range []Reason{ReasonTampered, ReasonTainted, ReasonCriterionError, ReasonCriteriaFailed, ReasonVerifierChanged,
		ReasonVerifierBlocked, ReasonRubricRejected, ReasonInternalError} {
		for _, p := range parks {
			if p.Reason == sev {
				return p
			}
		}
	}
	if wait != nil {
		return *wait
	}
	if !f.RecheckOK {
		return Verdict{Wait, WaitRecheck, "the last live recheck has not succeeded"}
	}
	return Verdict{Outcome: Commit}
}

// reviewVerdict decides the review condition; ok is false when the review is satisfied.
func (f Facts) reviewVerdict() (Verdict, bool) {
	ev := f.roundEvidence()
	var lastEvidence int64
	for _, e := range ev {
		if e.Seq > lastEvidence {
			lastEvidence = e.Seq
		}
	}
	want := ResultsDigest(ev)
	satisfied := false
	var changed bool
	for _, r := range f.Reviews {
		if !f.reviewBound(r) || r.State != ReviewComplete {
			continue // a started row, or another round, freeze point or digest, never counts
		}
		if r.Verdict == VerdictBlock {
			return Verdict{Park, ReasonVerifierBlocked, r.ReasonCode}, true // a blocker poisons the round
		}
		if r.Verdict != VerdictOK || r.ResultsDigest != want || r.Seq <= lastEvidence {
			continue // not a clean review of these results
		}
		if r.Observed != f.Charter.Verifier {
			changed = true
			continue
		}
		satisfied = true
	}
	switch {
	case satisfied:
		return Verdict{}, false
	case changed:
		return Verdict{Park, ReasonVerifierChanged, "the review was made by a model that is not the pinned verifier"}, true
	}
	return Verdict{Wait, WaitReview, "no completed, bound review of this round's results"}, true
}
