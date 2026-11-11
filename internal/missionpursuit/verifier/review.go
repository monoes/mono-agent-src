package verifier

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// conclude turns the machine results into the outcome. Only when every machine criterion passed is the reviewer asked, and
// its answer can only block: "ok" leaves the outcome passed-machine, which approves nothing by itself.
func (k *Checker) conclude(ctx context.Context, req Request, tree finish.Tree, res *Result) (Result, error) {
	failed, errored := false, false
	for _, c := range res.Criteria {
		switch c.Result {
		case ResFail:
			failed = true
		case ResPass, ResPerson:
		default:
			errored = true
		}
	}
	ev := evidenceOf(res)
	res.ResultsDigest = finish.ResultsDigest(ev)
	switch {
	case failed:
		res.Outcome, res.Reason = OutcomeFailed, ReasonCriteriaFailed
		return *res, nil
	case errored:
		res.Outcome, res.Reason = OutcomeError, ReasonCriterionError
		return *res, nil
	}
	return k.review(ctx, req, tree, ev, res)
}

// evidenceOf is what the reviewer sees: host-measured data, with the marker characters made impossible to write. The text a
// program printed (its excerpts, and the detail line, which can carry an operating-system error) goes to the reviewer ONLY when
// the programs ran confined: a program that ran unconfined could print any file the person can read, and the reviewer's
// provider would get it. For those runs the reviewer sees digests, exit codes and results only. (The person still sees the text.)
func evidenceOf(res *Result) []finish.Evidence {
	text := res.Containment != ContainmentUnconfined
	ev := make([]finish.Evidence, 0, len(res.Criteria))
	for _, c := range res.Criteria {
		if c.Result == ResPerson {
			continue
		}
		e := finish.Evidence{CriterionID: c.ID, CriterionHash: c.Hash, Result: c.Result, ExitCode: c.ExitCode,
			StdoutDigest: c.StdoutDigest, StderrDigest: c.StderrDigest, Net: c.Net, RunsSubjectCode: c.RunsSubjectCode}
		if text {
			e.Detail, e.StdoutExcerpt, e.StderrExcerpt = defang(c.Detail), defang(c.Stdout), defang(c.Stderr)
		}
		ev = append(ev, e)
	}
	return ev
}

func (k *Checker) review(ctx context.Context, req Request, tree finish.Tree, ev []finish.Evidence, res *Result) (Result, error) {
	blocked := func(r Reason) (Result, error) { res.Outcome, res.Reason = OutcomeBlocked, r; return *res, nil }
	settleFn, err := k.cfg.Meter.Reserve(ctx, k.capUSD)
	if err != nil || settleFn == nil {
		if ctx.Err() != nil {
			res.Outcome, res.Reason = OutcomeAbandoned, ReasonCancelled
			return *res, nil
		}
		return blocked(ReasonNoBudget)
	}
	var once sync.Once
	settle := func(micro int64, known bool) {
		once.Do(func() { res.CostMicro, res.CostKnown = micro, known; settleFn(micro, known) })
	}
	// A panic between here and the settlement still charges the cap, once.
	defer func() { micro, known := ChargeFor(0, false, k.capUSD); settle(micro, known) }()

	cs := &costing{inner: k.cfg.Exec}
	rv := &finish.RunnerVerifier{Pin: k.pin, Exec: cs.exec, Workspace: k.cfg.Workspace, Timeout: k.cfg.ReviewTimeout, BudgetUSD: k.capUSD}
	rt := k.cfg.ReviewTimeout
	if rt <= 0 {
		rt = 3 * time.Minute // the finish verifier's default
	}
	rctx, rcancel := context.WithTimeout(ctx, rt+30*time.Second)
	defer rcancel()
	rr, rerr := rv.Review(rctx, finish.ReviewInput{Round: finish.Round{N: req.Round, Digest: tree.Digest}, Results: ev})
	settle(cs.charge(k.capUSD))
	switch {
	case ctx.Err() != nil:
		res.Outcome, res.Reason = OutcomeAbandoned, ReasonCancelled
		return *res, nil
	case rerr != nil:
		return blocked(ReasonReviewerUnavailable)
	}
	if req.LeadPinNow != nil {
		// The lead's model is read again now that the money is spent: a lead that changed (or is no longer known) while the
		// reviewer was out blocks the check. The spend was settled above.
		if now := req.LeadPinNow(); PinKey(now) == "" || PinKey(now) != PinKey(req.LeadPin) {
			return blocked(ReasonLeadChanged)
		} else if ok, _ := Independent(req.JudgePin, k.cfg.Checker, now); !ok {
			return blocked(ReasonLeadChanged)
		}
	}
	res.ReviewerObserved = Excerpt(rr.Observed.Provider+"/"+rr.Observed.Model, 100)
	res.ReviewerVerdict = rr.Verdict
	if !rr.Observed.SameModel(k.pin) || rr.Observed.Provider == "" {
		return blocked(ReasonReviewerPinChanged)
	}
	switch rr.Verdict {
	case finish.VerdictOK:
		res.Outcome, res.Reason = OutcomePassedMachine, ReasonNone
		res.ReviewerReason = reasonCode(strings.ToLower(rr.ReasonCode))
		return *res, nil
	case finish.VerdictBlock:
		res.ReviewerReason = reasonCode(strings.ToLower(rr.ReasonCode))
		return blocked(ReasonReviewerBlocked)
	}
	res.ReviewerVerdict = ""
	return blocked(ReasonReviewerInvalid)
}
