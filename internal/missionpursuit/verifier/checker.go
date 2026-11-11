package verifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/monomind"
)

// ErrBusy: a check is already running in this Checker; one at a time.
var ErrBusy = errors.New("verifier: a check is already running")

// Config builds the real verifier.
type Config struct {
	// Checker is the checker model's pin, "runtime/model[@version]": required.
	Checker string
	// ScratchBase is an existing private folder the run's snapshot and working areas are made in (and removed from): required.
	ScratchBase string
	// Meter reserves and settles the reviewer's spend: required (no reviewer call is ever made unmetered).
	Meter Meter
	// Exec and Workspace default to monomind.Exec and monomind.SandboxWorkspaceDir("pursuit-verifier") (an empty folder, never
	// the project).
	Exec      ExecFunc
	Workspace func() (string, error)
	// CallCapUSD is the most one reviewer call may cost: 0 is DefaultCallCapUSD.
	CallCapUSD    float64
	ReviewTimeout time.Duration // 0: the finish verifier's default
	// CriterionTimeout and TotalTimeout can only lower the caps (2 minutes, 20 minutes), for tests.
	CriterionTimeout, TotalTimeout time.Duration
	// Confiner is the confinement of project commands, proved by the caller (the host, a later slice). Without one that passes
	// its SelfTest, command and output criteria are not run (cannot-check: no-confinement); file criteria need none.
	Confiner Confiner
	// NetDenyAvailable overrides the platform probe (tests).
	NetDenyAvailable func() bool

	afterCriteria func() // test seam: runs after the criteria, before the recheck
	afterCopy     func() // test seam: runs after the snapshot copy, before the first digest of the live folder
	beforeWait    func() // test seam: runs before the check waits for the snapshot goroutine
}

// Confiner is a confinement the host provides and proves: it is what keeps a project command from the rest of the computer
// (see doc.go for what it must hold).
type Confiner interface {
	// Name is recorded in every result.
	Name() string
	// SelfTest proves the confinement works here and now (a canary the command must not be able to read or write, see
	// CanarySpecs). Any error means it does not hold.
	SelfTest(ctx context.Context) error
	// Place makes cmd start inside the confinement, as finish.Sandbox.Place does.
	Place(cmd *exec.Cmd) (release func(), err error)
}

// Hider is an optional part of a Confiner: it returns a copy of the confiner for one check that also hides the live project
// folder root from the programs (the criteria run on a sandbox copy, so the live folder is never mounted; where it lies outside
// the places the confinement already hides, the copy must say so). The self test runs on the original, the programs on the copy.
type Hider interface {
	Hide(root string) Confiner
}

// LeftoverChecker is an optional part of a Confiner: it lists what a program left running after its release (units, processes).
// The outlive, fork-bomb, memory and timeout canaries need it; a confiner without it cannot pass them (fail closed).
type LeftoverChecker interface {
	Leftovers(ctx context.Context) ([]string, error)
}

// InfraReporter is an optional part of a Confiner: it says what the confinement itself got wrong while a criterion ran (a unit
// that never started, one killed from outside), which the program's exit status cannot tell from the program's own. TakeInfra
// returns the failures since the last call and clears them; the checker clears before each criterion and reads after it, and
// a failure ends the criterion in error, never in a verdict.
type InfraReporter interface {
	TakeInfra() []string
}

// Checker is the real Verifier: finish.Sandbox for the machine criteria, finish.RunnerVerifier for the block-only review.
type Checker struct {
	cfg       Config
	pin       finish.Pin
	capUSD    float64
	crit, tot time.Duration
	run       sync.Mutex
}

var _ Verifier = (*Checker)(nil)

// New builds the checker. A pin that does not name a runtime and a model, a missing meter or a missing scratch folder is
// refused.
func New(c Config) (*Checker, error) {
	rt, md, ok := strings.Cut(c.Checker, "/")
	name, ver, _ := strings.Cut(md, "@")
	if !ok || rt == "" || name == "" || strings.ContainsAny(c.Checker, " \t\r\n") {
		return nil, fmt.Errorf("verifier: the pin %q is not runtime/model[@version]", c.Checker)
	}
	if c.Meter == nil {
		return nil, errors.New("verifier: no meter, so the reviewer's spend would not be reserved")
	}
	if st, err := os.Stat(c.ScratchBase); c.ScratchBase == "" || err != nil || !st.IsDir() {
		return nil, errors.New("verifier: the scratch folder is not an existing folder")
	}
	k := &Checker{cfg: c, pin: finish.Pin{Provider: rt, Model: name, Version: ver}}
	if k.cfg.Exec == nil {
		k.cfg.Exec = monomind.Exec
	}
	if k.cfg.Workspace == nil {
		k.cfg.Workspace = func() (string, error) { return monomind.SandboxWorkspaceDir("pursuit-verifier") }
	}
	k.capUSD = c.CallCapUSD
	if !(k.capUSD > 0) || k.capUSD > 100 { // also false for NaN
		k.capUSD = DefaultCallCapUSD
	}
	k.crit, k.tot = lowered(c.CriterionTimeout, CriterionTimeout), lowered(c.TotalTimeout, TotalTimeout)
	return k, nil
}

func lowered(want, limit time.Duration) time.Duration {
	if want <= 0 || want > limit {
		return limit
	}
	return want
}

func (k *Checker) Pin() string { return k.cfg.Checker }

var (
	idRE   = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// validate says why a request cannot be checked, or "".
func validate(req Request) Reason {
	if len(req.Criteria) > MaxCriteria {
		return ReasonTooManyCriteria
	}
	if (req.LeadPinNow != nil) == req.NoLeadReread { // neither (silently skipped) or both (ambiguous)
		return ReasonBadRequest
	}
	if !hashRE.MatchString(req.GoalHash) || len(req.Criteria) == 0 || req.Root == "" || !filepath.IsAbs(req.Root) {
		return ReasonBadRequest
	}
	seen := map[string]bool{}
	for _, c := range req.Criteria {
		switch c.Kind {
		case "command", "output", "file", "rubric": // "test" is never run by the harness, so it is refused up front
		default:
			return ReasonBadRequest
		}
		if !idRE.MatchString(c.ID) || !hashRE.MatchString(c.Hash) || seen[c.ID] {
			return ReasonBadRequest
		}
		seen[c.ID] = true
	}
	return ReasonNone
}

func isProgram(kind string) bool { return kind == "command" || kind == "output" }

// Run is one goal check. Everything it can conclude is an Outcome, a cancelled context included (abandoned); the error is for
// a scratch area that could not be made and for a check already running.
//
// Deadlines: the criteria share TotalTimeout (20 minutes) and have CriterionTimeout (2 minutes) each; the snapshot and the
// rechecks stop at once when ctx ends (the copy finishes in the background and removes its own scratch area); the reviewer
// call is bounded by its timeout (3 minutes by default) plus a 30 second grace.
func (k *Checker) Run(ctx context.Context, req Request) (Result, error) {
	if !k.run.TryLock() {
		return Result{}, ErrBusy
	}
	defer k.run.Unlock()
	res := Result{Containment: ContainmentFileOnly, ReviewerPin: k.cfg.Checker, GoalHash: req.GoalHash}
	end := func(o Outcome, r Reason) (Result, error) { res.Outcome, res.Reason = o, r; return res, nil }

	if r := validate(req); r != ReasonNone {
		return end(OutcomeCannotCheck, r)
	}
	if ok, _ := Independent(req.JudgePin, k.cfg.Checker, req.LeadPin); !ok {
		return end(OutcomeCannotCheck, ReasonPinNotIndependent)
	}
	if !platformOK {
		return end(OutcomeCannotCheck, ReasonNoContainment)
	}
	machine, programs := 0, 0
	for _, c := range req.Criteria {
		if c.Kind != "rubric" {
			machine++
		}
		if isProgram(c.Kind) {
			programs++
		}
	}
	if machine == 0 {
		return end(OutcomeCannotCheck, ReasonNoMachineCriteria)
	}
	var conf Confiner
	if programs > 0 {
		switch {
		case k.cfg.Confiner != nil:
			// A confinement that does not prove itself here and now is no confinement, and there is no fall back to the opt-in.
			if err := k.cfg.Confiner.SelfTest(ctx); err != nil {
				if ctx.Err() != nil {
					return end(OutcomeAbandoned, ReasonCancelled)
				}
				res.ConfinementDetail = Excerpt(err.Error(), 300)
				return end(OutcomeCannotCheck, ReasonNoConfinement)
			}
			conf = k.cfg.Confiner
			res.Containment = ContainmentConfinedPrefix + Excerpt(conf.Name(), 64) + " + process-group"
		case req.AllowUnconfined:
			res.Containment = ContainmentUnconfined
		default:
			return end(OutcomeCannotCheck, ReasonNoConfinement)
		}
	}
	sb := finish.Sandbox{RequireNetDeny: true, NetDenyAvailable: k.cfg.NetDenyAvailable, Timeout: k.crit, OutCap: OutputCap}
	if programs > 0 && !sb.NetEnforceable() {
		return end(OutcomeCannotCheck, ReasonNoNetworkBlock)
	}
	if ctx.Err() != nil {
		return end(OutcomeAbandoned, ReasonCancelled)
	}

	dir, err := os.MkdirTemp(k.cfg.ScratchBase, "check-")
	if err != nil {
		return Result{}, fmt.Errorf("verifier: no scratch area: %w", err)
	}
	clean := true
	defer func() {
		if clean {
			finish.RemoveTree(dir)
		}
	}()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		return Result{}, fmt.Errorf("verifier: no scratch area: %w", err)
	}
	snap := filepath.Join(dir, "snap")
	type sealed struct {
		before string
		tree   finish.Tree
		err    error
		moved  bool
	}
	ch := make(chan sealed, 1)
	go func() {
		var s sealed
		if s.before, s.err = finish.StatManifest(req.Root); s.err == nil {
			s.tree, s.err = finish.SnapshotCtx(ctx, req.Root, snap)
		}
		if s.err == nil {
			if k.cfg.afterCopy != nil {
				k.cfg.afterCopy()
			}
			// The tree must not have moved while it was copied.
			live, derr := finish.DigestTreeCtx(ctx, req.Root, "")
			s.moved = derr != nil || live.Digest != s.tree.Digest
		}
		ch <- s
	}()
	var sl sealed
	if k.cfg.beforeWait != nil {
		k.cfg.beforeWait()
	}
	select {
	case sl = <-ch:
	case <-ctx.Done():
		clean = false // the copy is still running: it is removed when it ends
		go func() { <-ch; finish.RemoveTree(dir) }()
		return end(OutcomeAbandoned, ReasonCancelled)
	}
	if ctx.Err() != nil { // a cancellation that raced the finishing copy is a cancellation, not a refused or moved snapshot
		return end(OutcomeAbandoned, ReasonCancelled)
	}
	before, tree := sl.before, sl.tree
	if sl.err != nil {
		return end(OutcomeCannotCheck, ReasonSnapshotRefused)
	}
	res.SubjectDigest = tree.Digest
	for _, e := range tree.Entries {
		if e.Kind == 'f' {
			res.SubjectFiles++
		}
	}
	// ... and must be the one the host saw quiet.
	if sl.moved || (req.ExpectDigest != "" && req.ExpectDigest != tree.Digest) {
		return end(OutcomeStale, ReasonSubjectChanged)
	}

	gp := &GroupPlace{}
	defer gp.Close()
	if h, ok := conf.(Hider); ok {
		conf = h.Hide(req.Root) // a per-check copy that also keeps the live project out of the programs' sight
	}
	sb.Place, sb.Scratch = placeBoth(gp, conf), work
	runCtx, cancel := context.WithTimeout(ctx, k.tot)
	defer cancel()
	k.runCriteria(runCtx, sb, conf, snap, tree, req, &res)
	if k.cfg.afterCriteria != nil {
		k.cfg.afterCriteria()
	}
	gp.Close()
	cancel()
	if abandoned(ctx, runCtx, &res) {
		return res, nil
	}
	if programs > 0 {
		res.Net = finish.NetEnforced
	}
	for _, c := range res.Criteria {
		if isProgram(c.Kind) && c.Result != ResPerson && c.Net != "" && c.Net != finish.NetEnforced {
			res.Net = c.Net
			return end(OutcomeCannotCheck, ReasonNoNetworkBlock)
		}
	}
	type rechecked struct {
		after string
		live  finish.Tree
		aerr  error
		derr  error
	}
	rc := make(chan rechecked, 1)
	go func() {
		var r rechecked
		r.after, r.aerr = finish.StatManifest(req.Root)
		r.live, r.derr = finish.DigestTreeCtx(ctx, req.Root, "")
		rc <- r
	}()
	select {
	case r := <-rc:
		if r.aerr != nil || r.derr != nil || r.after != before || r.live.Digest != tree.Digest {
			return end(OutcomeStale, ReasonSubjectChanged)
		}
	case <-ctx.Done():
		return end(OutcomeAbandoned, ReasonCancelled)
	}
	return k.conclude(ctx, req, tree, &res)
}

// abandoned marks the result abandoned when the caller cancelled or the run's deadline passed.
func abandoned(ctx, runCtx context.Context, res *Result) bool {
	switch {
	case ctx.Err() != nil:
		res.Outcome, res.Reason = OutcomeAbandoned, ReasonCancelled
	case runCtx.Err() != nil && !errors.Is(runCtx.Err(), context.Canceled):
		res.Outcome, res.Reason = OutcomeAbandoned, ReasonRunDeadline
	default:
		return false
	}
	return true
}

// placeBoth places a command in the process group and, when there is one, the confinement; both are released. The release
// it returns is never nil (the sandbox defers it after the program started), even when a confiner returned nil, and a
// confiner that rewrote the command's process attributes so that it is no longer in its own process group is refused.
func placeBoth(gp *GroupPlace, conf Confiner) func(*exec.Cmd) (func(), error) {
	return func(cmd *exec.Cmd) (func(), error) {
		rg, err := gp.Place(cmd)
		if err != nil {
			return nil, err
		}
		if conf == nil {
			return rg, nil
		}
		rc, err := conf.Place(cmd)
		if err != nil {
			rg()
			return nil, err
		}
		if _, err := gp.Place(cmd); err != nil { // the group requirement must still hold after the confiner touched cmd
			if rc != nil {
				rc()
			}
			rg()
			return nil, errors.New("the confiner changed the command so it is no longer in its own process group")
		}
		return func() {
			if rc != nil {
				rc()
			}
			rg()
		}, nil
	}
}
