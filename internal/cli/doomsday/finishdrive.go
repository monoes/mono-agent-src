package doomsday

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/monomind"
)

func jsonLine(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }

// FinishEngine is the StoreEngine that also drives the finish (FinishOps): the production construction of the harness of
// note 13.6 over the real store (sequenced and witnessed), the monomind runner for the verifier and the stop package's
// containment for the verification unit.
type FinishEngine struct {
	*StoreEngine
	W Wiring
	// RoundTimeout is T_finish: the whole of one Advance. Default 30 minutes. At the deadline the finish parks with the
	// reason `deadline`.
	RoundTimeout time.Duration
	// Limits are the process limits and the scratch cap of a criterion (zero fields take the defaults of finish.Limits).
	Limits finish.Limits
	// RequireNetDeny parks a finish whose criteria run programs where the platform cannot deny the network.
	RequireNetDeny bool
	// VerifierExec is monomind.Exec; tests give a fake. Nothing else in the shipped path is a seam.
	VerifierExec func(ctx context.Context, opts monomind.ExecOptions, onEvent func(monomind.Event)) (*monomind.TurnResult, error)
	// SandboxFor overrides the verification environment (tests with a fake kernel give their own Placer); nil is the
	// real one over W.Kernel and W.Host.
	SandboxFor func(gen int64) func(ctx context.Context, round int64) (finish.Sandbox, func(), error)

	notes []string
}

var _ FinishOps = (*FinishEngine)(nil)

// FinishEngine returns the engine of the wiring that also drives the finish.
func (w Wiring) FinishEngine() *FinishEngine { return &FinishEngine{StoreEngine: w.Engine(), W: w} }

// ErrNotFinishing: the profile's latest run is not a finish.
var ErrNotFinishing = errors.New("the profile's latest run is not a finish (begin one with `doomsday finish --begin`)")

func (e *FinishEngine) area() string { return filepath.Join(e.W.RunDir.Dir, "verify") }

func (e *FinishEngine) timeout() time.Duration {
	if e.RoundTimeout > 0 {
		return e.RoundTimeout
	}
	return 30 * time.Minute
}

// latest is the profile's newest generation with its charter.
func (e *FinishEngine) latest(ctx context.Context) (store.Run, store.Charter, error) {
	next, err := e.W.Store.NextGeneration(ctx, e.W.Profile)
	if err != nil || next <= 1 {
		return store.Run{}, store.Charter{}, errors.Join(ErrNotFinishing, err)
	}
	run, err := e.W.Store.GetRun(ctx, e.W.Profile, next-1)
	if err != nil {
		return store.Run{}, store.Charter{}, err
	}
	c, err := e.W.Store.LoadCharter(ctx, e.W.Profile, next-1)
	return run, c, err
}

// subjectRoot resolves the subject root of a generation. The first call records it in the verification area; a later
// call may omit it, and a different one than the recorded is refused. The root must be a directory that neither contains
// nor lies inside the harness's own state.
func (e *FinishEngine) subjectRoot(gen int64, given string) (string, error) {
	rec := filepath.Join(e.area(), fmt.Sprintf("subject-g%d", gen))
	if b, err := os.ReadFile(rec); err == nil {
		have := strings.TrimSpace(string(b))
		if given != "" && abs(given) != have {
			return "", fmt.Errorf("the subject of this finish is %s; it cannot be changed to %s", have, abs(given))
		}
		return have, nil
	}
	if given == "" {
		return "", errors.New("the first call of a finish names the subject: --subject DIR")
	}
	root := abs(given)
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return "", fmt.Errorf("the subject %s is not a directory", root)
	}
	for _, own := range []string{e.W.RunDir.Dir, e.area()} {
		if within(root, own) || within(own, root) {
			return "", fmt.Errorf("the subject %s overlaps the harness's own state (%s)", root, own)
		}
	}
	if err := os.MkdirAll(e.area(), 0o700); err != nil {
		return "", err
	}
	return root, os.WriteFile(rec, []byte(root+"\n"), 0o600)
}

func abs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(abs(dir), abs(path))
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func (e *FinishEngine) harness(ctx context.Context, subject string) (*finish.Harness, store.Run, error) {
	run, c, err := e.latest(ctx)
	if err != nil {
		return nil, run, err
	}
	if run.State != store.StateQuiescent || run.Cause != store.FinishCause {
		return nil, run, fmt.Errorf("%w: generation %d is %s (cause %q)", ErrNotFinishing, run.Generation, run.State, run.Cause)
	}
	root, err := e.subjectRoot(run.Generation, subject)
	if err != nil {
		return nil, run, err
	}
	h := &finish.Harness{Ledger: e.W.Chained().FinishLedger(e.W.Profile, run.Generation), Subject: root, Area: filepath.Join(e.area(), "snapshots")}
	for _, k := range c.Criteria {
		h.Specs = append(h.Specs, finish.Spec{Kind: k.Kind, Spec: k.Spec, Expected: k.Expected})
	}
	h.Verifier = &finish.RunnerVerifier{
		Pin:  finish.Pin{Provider: c.VerifierPin.Provider, Model: c.VerifierPin.Model, Version: c.VerifierPin.Version, PolicyHash: c.VerifierPin.PurposePolicy},
		Exec: e.exec(), Workspace: func() (string, error) { return monomind.SandboxWorkspaceDir("doomsday-verify") },
	}
	if e.SandboxFor != nil {
		h.NewSandbox = e.SandboxFor(run.Generation)
	} else {
		h.NewSandbox = e.sandbox(run.Generation)
	}
	return h, run, nil
}

func (e *FinishEngine) exec() func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
	if e.VerifierExec != nil {
		return e.VerifierExec
	}
	return monomind.Exec
}

func (e *FinishEngine) report(r finish.Report, err error) (FinishReport, error) {
	out := FinishReport{State: r.State, Round: r.Round, Reason: string(r.Reason), Detail: r.Detail, Waiting: r.Waiting, Done: r.Done, Notes: e.notes}
	return out, err
}

// FinishAdvance takes the finish as far as it goes.
func (e *FinishEngine) FinishAdvance(ctx context.Context, o FinishOptions) (FinishReport, error) {
	h, run, err := e.harness(ctx, o.Subject)
	if err != nil {
		return FinishReport{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	rep, err := h.Advance(rctx)
	if err != nil && errors.Is(rctx.Err(), context.DeadlineExceeded) {
		return e.deadline(ctx, h, run, rep, err)
	}
	return e.report(rep, err)
}

func (e *FinishEngine) deadline(ctx context.Context, h *finish.Harness, run store.Run, rep finish.Report, cause error) (FinishReport, error) {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	f, ferr := h.Ledger.Facts(pctx)
	if ferr != nil || f.State != finish.StateVerifying {
		return e.report(rep, cause)
	}
	v := finish.Verdict{Outcome: finish.Park, Reason: finish.ReasonDeadline, Detail: "the round did not finish within T_finish"}
	if perr := h.Ledger.Park(pctx, v); perr != nil {
		return e.report(rep, errors.Join(cause, perr))
	}
	return e.report(finish.Report{State: finish.StateParked, Round: f.Round.N, Reason: v.Reason, Detail: v.Detail}, nil)
}

// FinishReverify starts another round of a parked finish.
func (e *FinishEngine) FinishReverify(ctx context.Context, o FinishOptions) (FinishReport, error) {
	h, _, err := e.harness(ctx, o.Subject)
	if err != nil {
		return FinishReport{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	return e.report(h.Reverify(rctx))
}

// RubricList lists the rubrics that wait, with their dossiers.
func (e *FinishEngine) RubricList(ctx context.Context) ([]RubricItem, error) {
	h, _, err := e.harness(ctx, "")
	if err != nil {
		return nil, err
	}
	ds, err := h.Dossiers(ctx)
	var out []RubricItem
	for _, d := range ds {
		out = append(out, RubricItem{ID: d.ID, Statement: d.Statement, Dossier: d.Text, Hash: d.Hash})
	}
	return out, err
}

// RubricDecide records the person's decision on one rubric.
func (e *FinishEngine) RubricDecide(ctx context.Context, id string, approve bool, dossierHash, note string) error {
	h, _, err := e.harness(ctx, "")
	if err != nil {
		return err
	}
	return h.Decide(ctx, id, approve, dossierHash, note)
}

// AbortFinish is `doomsday stop` over a finish that has frozen (note 13.6 section 7.1): the ledger records the stop, so
// nothing can commit or park afterwards, and the verification units are ended.
func (e *FinishEngine) AbortFinish(ctx context.Context) (bool, error) {
	run, _, err := e.latest(ctx)
	if err != nil || run.State != store.StateQuiescent || run.Cause != store.FinishCause {
		return false, nil
	}
	led := e.W.Chained().FinishLedger(e.W.Profile, run.Generation)
	f, err := led.Facts(ctx)
	if err != nil {
		return false, err
	}
	if f.State != finish.StateVerifying && f.State != finish.StateParked {
		return false, nil
	}
	h := &finish.Harness{Ledger: led, Area: filepath.Join(e.area(), "snapshots")}
	if err := h.Abort(ctx, "operator"); err != nil {
		return false, err
	}
	if left := e.endAllUnits(run.Generation); len(left) > 0 {
		return true, fmt.Errorf("the finish is stopped, but a verification unit is not proven gone: %s", strings.Join(left, "; "))
	}
	return true, nil
}
