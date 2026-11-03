package finish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The host harness of note 13.6 section 7: freeze (snapshot and digest), evidence (the confined criteria), the review by
// the distinct pinned verifier, the last live recheck and the commit through the witnessed ledger. Every step is
// idempotent and resumable: all state is in the ledger, so a crashed harness is simply run again (Advance).

// HarnessLedger is the ledger as the harness uses it (store.FinishLedger, and the sequenced SeqFinish).
type HarnessLedger interface {
	Ledger
	Freeze(ctx context.Context, digest, snapshot string) (round int64, err error)
	RecordEvidence(ctx context.Context, e Evidence) error
	RecordDecision(ctx context.Context, d RubricDecision) error
	RecordRecheck(ctx context.Context, ok bool) error
	Abort(ctx context.Context, by string) error
}

// Spec is one criterion as the charter hash names it. Specs are aligned with the charter's criteria by position.
type Spec struct{ Kind, Spec, Expected string }

// Harness drives one generation's finish.
type Harness struct {
	Ledger   HarnessLedger
	Verifier Verifier
	Specs    []Spec
	// Subject is the live root the operator names; the snapshot is taken from it and the recheck compares against it.
	Subject string
	// Area holds the sealed snapshots (mode 0700), one directory per generation and round.
	Area string
	// NewSandbox makes the confined environment of one round: the verification unit is created, recorded and a Placer
	// bound to it. end proves the unit gone and removes the round's scratch; it is always called.
	NewSandbox func(ctx context.Context, round int64) (sb Sandbox, end func(), err error)
	// Hook names the pause points of the tests ("freeze.snapshotted", "evidence.recorded", "after-last-evidence",
	// "after-full-digest", "after-recheck", and the Finisher's review.* and commit); an error models a crash there.
	Hook func(point string) error
	// FreezeTries bounds the attempts to take a snapshot of a subject that does not move meanwhile (default 3).
	FreezeTries int
}

func (h *Harness) at(p string) error {
	if h.Hook != nil {
		return h.Hook(p)
	}
	return nil
}

// Report is where the finish stands after one Advance.
type Report struct {
	State   string // the ledger's state: quiescent, verifying, finish-parked, finished
	Round   int64
	Reason  Reason
	Detail  string
	Waiting []string // what the finish waits for: rubric ids, or a note
	Done    bool     // finished
}

func (h *Harness) roundDir(gen, round int64) string {
	return filepath.Join(h.Area, "g"+strconv.FormatInt(gen, 10)+"-r"+strconv.FormatInt(round, 10))
}

// Advance takes the finish as far as it can go without a person: it freezes a quiescent finish, runs the machine criteria
// that have no evidence in the round, has the verifier review, rechecks the live subject and commits or parks. A rubric
// without a decision is a wait, never a park. A parked finish is reported as it is; only Reverify starts another round.
func (h *Harness) Advance(ctx context.Context) (Report, error) {
	f, err := h.Ledger.Facts(ctx)
	if err != nil {
		return Report{}, err
	}
	rep := Report{State: f.State, Round: f.Round.N}
	switch f.State {
	case StateFinished:
		rep.Done = true
		return rep, nil
	case StateParked:
		rep.Reason, rep.Detail = f.ParkReason, "the finish is parked for this round; `doomsday finish --reverify` starts a new round, `doomsday stop` ends it"
		return rep, nil
	case "quiescent":
		return h.freeze(ctx, f)
	case StateVerifying:
		return h.verify(ctx, f)
	case "stopped":
		rep.Detail = "the finish was stopped"
		return rep, nil
	}
	rep.Detail = "finish waiting for cleanup: the run is " + f.State
	return rep, nil
}

// Reverify starts a new round of a parked finish (note 13.6 section 7.4): a new snapshot, a new freeze point, all
// criteria again. The ledger bounds the rounds.
func (h *Harness) Reverify(ctx context.Context) (Report, error) {
	f, err := h.Ledger.Facts(ctx)
	if err != nil {
		return Report{}, err
	}
	if f.State != StateParked {
		return Report{State: f.State, Round: f.Round.N}, fmt.Errorf("finish: only a parked finish can be verified again; it is %s", f.State)
	}
	return h.freeze(ctx, f)
}

func (h *Harness) freeze(ctx context.Context, f Facts) (Report, error) {
	if why := f.Charter.CannotFinish(); why != "" {
		return Report{State: f.State}, errors.New("finish: " + why)
	}
	if h.Subject == "" || h.Area == "" {
		return Report{State: f.State}, errors.New("finish: the subject root and the verification area are required")
	}
	round := f.Round.N + 1
	if err := os.MkdirAll(h.Area, 0o700); err != nil {
		return Report{State: f.State}, err
	}
	dir := h.roundDir(f.Generation, round)
	RemoveTree(dir) // a snapshot of a crashed earlier attempt, never recorded
	if err := os.Mkdir(dir, 0o700); err != nil {
		return Report{State: f.State}, err
	}
	snap := filepath.Join(dir, "snapshot")
	tries := h.FreezeTries
	if tries <= 0 {
		tries = 3
	}
	var tree Tree
	moving := true
	for i := 0; i < tries && moving; i++ {
		if i > 0 {
			RemoveTree(snap)
		}
		t, err := Snapshot(h.Subject, snap)
		if err != nil {
			RemoveTree(dir)
			return Report{State: f.State}, err
		}
		tree = t
		live, err := DigestTree(h.Subject, "")
		moving = err != nil || live.Digest != tree.Digest
	}
	if err := h.at("freeze.snapshotted"); err != nil {
		return Report{State: f.State}, err
	}
	got, err := h.Ledger.Freeze(ctx, tree.Digest, filepath.Base(dir))
	if err != nil {
		RemoveTree(dir)
		return Report{State: f.State}, err
	}
	if got != round {
		return Report{State: f.State}, fmt.Errorf("finish: the ledger froze round %d, not %d", got, round)
	}
	if moving { // an unmediated writer is active: the workers are closed, so this is the documented residue
		v := Verdict{Park, ReasonSubjectChanged, "the subject kept changing while it was snapshotted"}
		if err := h.Ledger.Park(ctx, v); err != nil {
			return Report{}, err
		}
		return Report{State: StateParked, Round: round, Reason: v.Reason, Detail: v.Detail}, nil
	}
	return h.Advance(ctx)
}

// criterionIndex maps a ledger criterion to its position (the Specs index).
func criterionIndex(f Facts, c Criterion) int {
	for i, x := range f.Charter.Criteria {
		if x.ID == c.ID {
			return i
		}
	}
	return -1
}

func (h *Harness) verify(ctx context.Context, f Facts) (Report, error) {
	if len(h.Specs) != len(f.Charter.Criteria) {
		return Report{State: f.State}, fmt.Errorf("finish: %d specs for %d criteria", len(h.Specs), len(f.Charter.Criteria))
	}
	snap := filepath.Join(h.roundDir(f.Generation, f.Round.N), "snapshot")
	tree, err := DigestTree(snap, "")
	if err != nil || tree.Digest != f.Round.Digest {
		v := Verdict{Park, ReasonTampered, "the sealed snapshot is missing or no longer has the round's digest"}
		return h.parked(ctx, f, v)
	}
	var todo []Criterion
	for _, c := range f.Charter.Criteria {
		if !c.Rubric && len(f.evidenceFor(c)) == 0 {
			todo = append(todo, c)
		}
	}
	if len(todo) > 0 {
		if err := h.runCriteria(ctx, f, snap, tree, todo); err != nil {
			return Report{State: f.State, Round: f.Round.N}, err
		}
	}
	if err := h.at("after-last-evidence"); err != nil {
		return Report{}, err
	}
	fn := Finisher{Ledger: h.Ledger, Verifier: h.Verifier, Hook: h.Hook, Recheck: func(ctx context.Context) (*Verdict, error) { return h.recheck(ctx, tree) }}
	v, err := fn.Step(ctx)
	if err != nil {
		return Report{State: f.State, Round: f.Round.N}, err
	}
	return h.report(ctx, v)
}

func (h *Harness) parked(ctx context.Context, f Facts, v Verdict) (Report, error) {
	if err := h.Ledger.Park(ctx, v); err != nil {
		return Report{}, err
	}
	return Report{State: StateParked, Round: f.Round.N, Reason: v.Reason, Detail: v.Detail}, nil
}

func (h *Harness) runCriteria(ctx context.Context, f Facts, snap string, tree Tree, todo []Criterion) error {
	if h.NewSandbox == nil {
		return errors.New("finish: no confined environment is wired, so no criterion is run")
	}
	sb, end, err := h.NewSandbox(ctx, f.Round.N)
	if err != nil {
		return fmt.Errorf("finish: the verification unit could not be made: %w", err)
	}
	defer end()
	for _, c := range todo {
		s := h.Specs[criterionIndex(f, c)]
		m := Measured{Result: ResError}
		if plan, perr := ParsePlan(s.Kind, s.Spec, s.Expected); perr != nil {
			m.Detail = perr.Error()
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else {
			m = sb.Run(ctx, snap, tree, plan)
		}
		ev := Evidence{Generation: f.Generation, CharterHash: f.Charter.Hash, Round: f.Round.N, FreezeSeq: f.Round.FreezeSeq, Digest: f.Round.Digest,
			CriterionID: c.ID, CriterionHash: c.Hash, Result: m.Result, Verifier: f.Charter.Verifier,
			StdoutDigest: m.StdoutDigest, StderrDigest: m.StderrDigest, ExitCode: m.ExitCode, Net: NetDeclared, Detail: m.Detail,
			StdoutExcerpt: m.Stdout, StderrExcerpt: m.Stderr, RunsSubjectCode: m.RunsSubjectCode, Tainted: m.Tainted}
		if err := h.Ledger.RecordEvidence(ctx, ev); err != nil {
			return err
		}
		if err := h.at("evidence.recorded"); err != nil {
			return err
		}
	}
	return nil
}

// recheck is the last live recheck of section 7.3: the full digest of the live subject, then a cheap stat comparison
// taken around it, then the recheck row. A difference parks with the paths. The residue is the interval from the second
// stat comparison to the commit, and a writer that restores sizes and times.
func (h *Harness) recheck(ctx context.Context, snap Tree) (*Verdict, error) {
	f, err := h.Ledger.Facts(ctx)
	if err != nil {
		return nil, err
	}
	changed := func(why string, live Tree) (*Verdict, error) {
		return &Verdict{Park, ReasonSubjectChanged, why + ": " + strings.Join(Diff(snap, live, 10), ", ")}, nil
	}
	before, err := StatManifest(h.Subject)
	if err != nil {
		return &Verdict{Park, ReasonSubjectChanged, "the live subject cannot be read"}, nil
	}
	live, err := DigestTree(h.Subject, "")
	if err != nil || live.Digest != f.Round.Digest {
		return changed("the live subject no longer has the round's digest", live)
	}
	if err := h.at("after-full-digest"); err != nil {
		return nil, err
	}
	after, err := StatManifest(h.Subject)
	if err != nil || after != before {
		now, _ := DigestTree(h.Subject, "")
		return changed("the live subject changed during the recheck", now)
	}
	if err := h.Ledger.RecordRecheck(ctx, true); err != nil {
		return nil, err
	}
	return nil, h.at("after-recheck")
}

func (h *Harness) report(ctx context.Context, v Verdict) (Report, error) {
	f, err := h.Ledger.Facts(ctx)
	if err != nil {
		return Report{}, err
	}
	rep := Report{State: f.State, Round: f.Round.N, Reason: v.Reason, Detail: v.Detail}
	switch f.State {
	case StateFinished:
		rep.Done, rep.Reason, rep.Detail = true, "", ""
	case StateParked:
		rep.Reason = f.ParkReason
	case StateVerifying:
		rep.Waiting = h.pendingRubrics(f)
	}
	return rep, nil
}

// StatManifest is a quick description of a root (names, sizes, kinds, modification times), compared before and after the
// full digest of the recheck. It is a detection aid, not a lock.
func StatManifest(root string) (string, error) {
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(&b, "%s|%d|%d|%d\n", rel, info.Size(), info.Mode(), info.ModTime().UnixNano())
		return nil
	})
	return b.String(), err
}
