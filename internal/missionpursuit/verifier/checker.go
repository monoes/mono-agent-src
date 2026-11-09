package verifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/missionpursuit/judgecost"
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
	// CallCapUSD is the most one reviewer call may cost: 0 is judgecost.PerCallCapUSD.
	CallCapUSD    float64
	ReviewTimeout time.Duration // 0: the finish verifier's default
	// CriterionTimeout and TotalTimeout can only lower the caps (2 minutes, 20 minutes), for tests.
	CriterionTimeout, TotalTimeout time.Duration
	// NetDenyAvailable overrides the platform probe (tests).
	NetDenyAvailable func() bool

	afterCriteria func() // test seam: runs after the criteria, before the recheck
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
		k.capUSD = judgecost.PerCallCapUSD
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
	if len(req.Criteria) == 0 || req.Root == "" || !filepath.IsAbs(req.Root) {
		return ReasonBadRequest
	}
	seen := map[string]bool{}
	for _, c := range req.Criteria {
		switch c.Kind {
		case "command", "output", "file", "test", "rubric":
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

// Run is one goal check. Everything it can conclude is an Outcome; the error is for a scratch area that could not be made
// and for a check already running.
func (k *Checker) Run(ctx context.Context, req Request) (Result, error) {
	if !k.run.TryLock() {
		return Result{}, ErrBusy
	}
	defer k.run.Unlock()
	res := Result{Containment: Containment, ReviewerPin: k.cfg.Checker}
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
	defer finish.RemoveTree(dir)
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		return Result{}, fmt.Errorf("verifier: no scratch area: %w", err)
	}
	before, err := finish.StatManifest(req.Root)
	if err != nil {
		return end(OutcomeCannotCheck, ReasonSnapshotRefused)
	}
	snap := filepath.Join(dir, "snap")
	tree, err := finish.Snapshot(req.Root, snap)
	if err != nil {
		return end(OutcomeCannotCheck, ReasonSnapshotRefused)
	}
	res.SubjectDigest = tree.Digest
	for _, e := range tree.Entries {
		if e.Kind == 'f' {
			res.SubjectFiles++
		}
	}
	// The tree must be the one the host saw quiet, and must not have moved while it was copied.
	if live, derr := finish.DigestTree(req.Root, ""); derr != nil || live.Digest != tree.Digest ||
		(req.ExpectDigest != "" && req.ExpectDigest != tree.Digest) {
		return end(OutcomeStale, ReasonSubjectChanged)
	}

	gp := &GroupPlace{}
	defer gp.Close()
	sb.Place, sb.Scratch = gp.Place, work
	runCtx, cancel := context.WithTimeout(ctx, k.tot)
	defer cancel()
	k.runCriteria(runCtx, sb, snap, tree, req, &res)
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
	after, aerr := finish.StatManifest(req.Root)
	live, derr := finish.DigestTree(req.Root, "")
	if aerr != nil || derr != nil || after != before || live.Digest != tree.Digest {
		return end(OutcomeStale, ReasonSubjectChanged)
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
