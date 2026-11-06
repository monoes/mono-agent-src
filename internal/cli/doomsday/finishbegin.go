package doomsday

import (
	"context"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// KindFinishDenied is the audit kind of a finish.begin that failed a precondition (note 13.6 section 13: a `denied-attempt`
// row). The finish.* kinds are reserved to the ledger's own writers, so the denial is not one of them.
const KindFinishDenied = "denied-attempt"

// FinishBegin is `finish.begin` (note 13.6 sections 7.1 and 7.2, requirement A3): a boss operation without an approval
// record; its precondition check is its enforcement function. The preconditions are that the generation is running, that
// the charter can finish (it has criteria and a pinned verifier that is distinct from the boss and, where the charter
// names them, from the work roster) and that no person item is open. A refused call writes a `denied-attempt` row and
// changes nothing.
//
// Accepted, it IS the Stop path with cause=finish: the same marker, intent, revocation and unit end, the same outcomes. The
// run reaches quiescent only when a Stop would; until then the finish waits for cleanup.
//
// What registers this as a typed operation the boss may call is the ops allowlist (not part of this package); it calls
// this method.
func (e *FinishEngine) FinishBegin(ctx context.Context) (BeginReport, error) {
	w := e.W
	runs, err := w.Store.OpenRuns(ctx, w.Profile)
	if err != nil {
		return BeginReport{}, fmt.Errorf("the open runs cannot be read, nothing was begun: %w", err)
	}
	var run *store.Run
	for i := range runs {
		if runs[i].State == store.StateRunning {
			run = &runs[i]
		}
	}
	if run == nil {
		return BeginReport{Refused: "there is no running run to finish"}, nil
	}
	rep := BeginReport{Generation: run.Generation}
	deny := func(why string) (BeginReport, error) {
		rep.Refused = why
		// A boss that keeps asking for a finish that cannot begin gets the same answer without a new row each time (the
		// count is the stream's own, so it holds across the processes that serve boss calls).
		n, cerr := (AuditCounter{DB: w.DB}).count(ctx, w.Profile, run.Generation, "kind = '"+KindFinishDenied+"'")
		if w.DB != nil && (cerr != nil || n >= ops.MaxParkRows) {
			return rep, nil
		}
		if _, aerr := w.Chained().AppendAudit(ctx, w.Profile, run.Generation, KindFinishDenied, why); aerr != nil {
			rep.Refused += " (and the refusal could not be recorded: " + aerr.Error() + ")"
		}
		return rep, nil
	}
	facts, err := w.Chained().FinishFacts(ctx, w.Profile, run.Generation)
	if err != nil {
		return BeginReport{}, fmt.Errorf("the charter of generation %d cannot be read: %w", run.Generation, err)
	}
	if why := facts.Charter.CannotFinish(); why != "" {
		return deny(why)
	}
	if w.DB == nil {
		return deny("the open person items cannot be read, so a finish cannot be shown to be free of them")
	}
	items, err := coderhost.NewStore(w.DB).OpenPersonItems(w.Profile)
	if err != nil {
		return deny("the open person items cannot be read: " + err.Error())
	}
	if len(items) > 0 {
		return deny(fmt.Sprintf("%d person item(s) are open: answer them first (a finish cannot be begun while one blocks completion)", len(items)))
	}
	if w.Tokens == nil {
		return BeginReport{}, errors.New("token revocation is not wired; nothing was begun")
	}
	st := w.Stopper("finish", e.ender())
	report := st.Stop(ctx, uint64(run.Generation), run.CharterID, store.FinishCause)
	rep.Accepted, rep.State = true, string(report.Final)
	if !report.Stopped() { // a best-effort stop prints its lines too: they carry the limit
		rep.Lines = report.Lines() // the run is not quiescent yet: the finish waits for cleanup
	}
	return rep, nil
}

// ender is Wiring.ender with the end configuration's clock set: EndUnit reads cfg.Clock for its escalation windows, and the
// Wiring's own ender leaves it nil, which would panic at the first unit that has a ticket sub-cgroup to end. (Reported;
// wire.go is not this package's file.)
func (e *FinishEngine) ender() stop.Ender {
	w := e.W
	if w.Kernel == nil {
		return notProvenEnder{}
	}
	u := stop.UnitEnder{K: w.Kernel, Dir: w.RunDir, Cfg: stop.EndConfig{Clock: w.clock()}}
	if w.Host != nil {
		u.StopUnit = w.Host.StopUnit
	}
	seq, err := w.Sequencer(context.Background())
	if err != nil {
		return u
	}
	u.Witness = seq.BoundUnits
	return stop.NewWitnessEnder(u, seq)
}
