package doomsday

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/secrets"
)

// StoreEngine is the Engine over the real store. Containment and Stop are the
// stop package's; until they are given, both fail closed (the mode refuses to
// start without a containment probe, and Stop reports it cannot run).
type StoreEngine struct {
	Store   store.Store
	Profile string
	// ContainmentProbe is nil only when containment is available (stop package).
	ContainmentProbe func() error
	StopFn           func(ctx context.Context) (StopOutcome, error)
	// AfterEnter runs once the run is recorded (the stop package's per-run secret). A failure is reported with
	// the run id: the run exists and `doomsday stop` still stops it (its marker is then unsigned).
	AfterEnter func(ctx context.Context, generation int64) error
	// BeforeStart runs before a charter is entered: it revisits what a dead host left (merge members, note 13.3).
	// An error refuses the start.
	BeforeStart func(ctx context.Context) error
	// Binding says whether security.peercred_binding is on. A run needs bound turns: nil, an error and false all
	// refuse the start.
	Binding func(ctx context.Context) (bool, error)
	// UnboundReason is non-empty when binding is off because this machine cannot run the binding service: the run then starts
	// without binding (with a warning and an audit row) instead of being refused. Nil: binding off always refuses.
	UnboundReason func(ctx context.Context) string
	// CreateUnit makes the run's containment unit and records it durably, after the charter is validated and
	// before the store records the run: no unit, no run (note 13.2 section 4). The discard it returns stops the
	// unit again if the store then refuses the entry. Nil refuses the start: there is no mode without containment.
	CreateUnit func(ctx context.Context, generation int64) (discard func(context.Context) error, err error)
	// BeginEntry runs before the unit is created: the witness exists and the outside memories (counter, witness,
	// anchor) agree the generation is new (note 13.5 section 5). An error refuses the start, nothing created.
	BeginEntry func(ctx context.Context, generation int64) error
	// Anchored runs once the run is recorded: the anchor in the OS secret store and the generation counter (note 13.5
	// section 5). bound false carries why rollback detection is weaker; an error is reported with the run id.
	Anchored func(ctx context.Context, generation int64) (bound bool, why string, err error)
	// HeadAnchor compares the audit head the control room reads with the witness. Nil: the head is never anchored.
	HeadAnchor HeadAnchorFunc
	// Boss records which top-level lead mission is the run's boss (Wiring.DesignateBoss). Nil: no boss can be named.
	Boss func(ctx context.Context, session int64, entry Entry) error
	// BossCheck says whether a session may be named the run's boss, before the run exists (Wiring.checkBoss). Nil: no boss can be named.
	BossCheck func(ctx context.Context, session int64) error
	// AfterStopFn ends the run's sessions and writes the chat message once a stop is proven. Nil: nothing hangs on a stop.
	AfterStopFn func(ctx context.Context, o StopOutcome, verdict string, at time.Time) AfterReport
	// RepairStopFn finishes the sessions and message of the last stop when a stop finds no open run. Nil: nothing to repair.
	RepairStopFn func(ctx context.Context, at time.Time) ([]string, RepairStatus)
}

// CheckBoss is BossChecker.
func (e *StoreEngine) CheckBoss(ctx context.Context, session int64) error {
	if e.BossCheck == nil {
		return errors.New("this install cannot name a boss")
	}
	return e.BossCheck(ctx, session)
}

// RepairStop is StopRepairer.
func (e *StoreEngine) RepairStop(ctx context.Context, at time.Time) ([]string, RepairStatus) {
	if e.RepairStopFn == nil {
		return nil, RepairNothing
	}
	return e.RepairStopFn(ctx, at)
}

// AfterStop is AfterStopper.
func (e *StoreEngine) AfterStop(ctx context.Context, o StopOutcome, verdict string, at time.Time) AfterReport {
	if e.AfterStopFn == nil {
		return AfterReport{}
	}
	return e.AfterStopFn(ctx, o, verdict, at)
}

// BindBoss records the boss session of a started run: only the operator's `start --boss-session` calls it.
func (e *StoreEngine) BindBoss(ctx context.Context, session int64, entry Entry) error {
	if e.Boss == nil {
		return errors.New("no boss can be recorded on this install")
	}
	return e.Boss(ctx, session, entry)
}

var errNoUnit = errors.New("no containment unit creator is wired, so the run's processes could not be contained")

var errNoProbe = errors.New("no containment probe is wired, so containment cannot be shown to be available")

// ErrBindingOff is why a run does not start while peer-credential binding is off ("no run without binding").
var ErrBindingOff = errors.New("no run without binding: peer-credential binding is off, and an open-ended run needs every turn's tokens bound to its process; switch it on first (coder set --peercred-binding on)")

// UnsupportedHere is returned by a Binding func for an OS where the mode cannot run at all; RequireBinding passes its text
// through unchanged.
type UnsupportedHere string

func (u UnsupportedHere) Error() string { return string(u) }

// RequireBinding is nil only when binding is on. The start asks it before the charter is shown and again in Enter.
func (e *StoreEngine) RequireBinding(ctx context.Context) error {
	if e.Binding == nil {
		return fmt.Errorf("no run without binding: whether peer-credential binding is on is not known on this install")
	}
	on, err := e.Binding(ctx)
	var un UnsupportedHere
	if errors.As(err, &un) {
		return err
	}
	if err != nil {
		return fmt.Errorf("no run without binding: whether peer-credential binding is on cannot be read: %w", err)
	}
	if !on && e.UnboundWarning(ctx) == "" {
		return ErrBindingOff
	}
	return nil
}

// UnboundWarning is "running without peer-credential binding: <reason>" when binding is off because this machine cannot run the
// service (the run then starts anyway), "" otherwise.
func (e *StoreEngine) UnboundWarning(ctx context.Context) string {
	if e.UnboundReason == nil {
		return ""
	}
	if r := e.UnboundReason(ctx); r != "" {
		return "running without peer-credential binding: " + r
	}
	return ""
}

// truncateReason bounds the reason stored in the audit row.
func truncateReason(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// KindRunUnbound is the audit row (one line in the run's feed) that a run started without peer-credential binding.
const KindRunUnbound = "run.unbound"

func (e *StoreEngine) Containment() error {
	if e.ContainmentProbe == nil {
		return errNoProbe
	}
	return e.ContainmentProbe()
}

// Canonical builds the store's charter for the next generation, validates it
// there and returns its canonical bytes' hash.
func (e *StoreEngine) Canonical(ctx context.Context, c Charter) (Canon, error) {
	gen, err := e.Store.NextGeneration(ctx, e.Profile)
	if err != nil {
		return Canon{}, err
	}
	sc := toStore(c, e.Profile, gen)
	if err := sc.Validate(); err != nil {
		return Canon{}, err
	}
	h := sc.Hash()
	return Canon{Bytes: sc.Canonical(), Hash: hex.EncodeToString(h[:]), Handle: sc}, nil
}

// Enter hands the store the charter and the hash the operator confirmed; the
// store re-hashes inside its transaction and refuses a difference.
func (e *StoreEngine) Enter(ctx context.Context, canon Canon, expectedHash string, ack Ack) (Entry, error) {
	sc, ok := canon.Handle.(store.Charter)
	if !ok {
		return Entry{}, errors.New("the charter was not prepared by this engine")
	}
	raw, err := hex.DecodeString(expectedHash)
	if err != nil || len(raw) != 32 {
		return Entry{}, errors.New("the confirmed hash is not a full SHA-256")
	}
	var confirmed [32]byte
	copy(confirmed[:], raw)
	if err := e.RequireBinding(ctx); err != nil {
		return Entry{}, err
	}
	// The probe is part of the entry itself, not of the CLI verb: no starter can skip it. It also builds the witness
	// (the pinned connection), and runs before anything is created.
	if err := e.Containment(); err != nil {
		return Entry{}, fmt.Errorf("the entry is refused, containment is not available: %w", err)
	}
	if e.BeforeStart != nil {
		if err := e.BeforeStart(ctx); err != nil {
			return Entry{}, fmt.Errorf("a begun merge of an earlier run could not be revisited, so nothing was started: %w", err)
		}
	}
	if e.CreateUnit == nil {
		return Entry{}, errNoUnit
	}
	if e.BeginEntry != nil {
		if err := e.BeginEntry(ctx, sc.Generation); err != nil {
			return Entry{}, fmt.Errorf("the entry is refused before anything was created: %w", err)
		}
	}
	discard, err := e.CreateUnit(ctx, sc.Generation)
	if err != nil {
		return Entry{}, fmt.Errorf("the containment unit could not be created, so nothing was started: %w", err)
	}
	run, err := e.Store.Enter(ctx, sc, confirmed, "ack:"+ack.Kind)
	if errors.Is(err, store.ErrCommitUnknown) {
		// The commit reported an error, so the run may exist. Read it: found, it is this entry's run and the entry goes on; not
		// there, the entry was refused; unreadable, the outcome is unknown and nothing is torn down (the unit stays).
		got, gerr := e.Store.GetRun(ctx, sc.Profile, sc.Generation)
		switch {
		case gerr == nil && got.CharterID == sc.ID():
			run, err = got, nil
		case gerr == nil:
			err = fmt.Errorf("another run holds the profile (the entry's own was not recorded): %w", err) // refused below
		case errors.Is(gerr, sql.ErrNoRows):
			// no run row: refused below
		default:
			return Entry{}, unknownEntry(fmt.Errorf("the run may have started but the outcome of its commit could not be read (run `doomsday stop` if it is running): %v: %w", gerr, err))
		}
	}
	if err != nil {
		if derr := discard(ctx); derr != nil {
			err = fmt.Errorf("%w (and the unit created for it could not be removed: %v)", err, derr)
		}
		return Entry{}, err
	}
	// The audit row is the stream's own record that a script, not a person, may have supplied the lift.
	if _, err := e.Store.AppendAudit(ctx, run.Profile, run.Generation, "start", fmt.Sprintf("ack=%s lines=%d", ack.Kind, ack.Lines)); err != nil {
		return Entry{}, recorded(fmt.Errorf("the run started but its audit record failed: %w", err))
	}
	warning := ""
	if on, berr := e.Binding(ctx); berr == nil && !on {
		if warning = e.UnboundWarning(ctx); warning != "" {
			if _, err := e.Store.AppendAudit(ctx, run.Profile, run.Generation, KindRunUnbound, "unbound: "+truncateReason(e.UnboundReason(ctx))); err != nil {
				return Entry{}, recorded(fmt.Errorf("the run started but its audit record of running without binding failed: %w", err))
			}
		}
	}
	if e.AfterEnter != nil {
		if err := e.AfterEnter(ctx, run.Generation); err != nil {
			return Entry{}, recorded(fmt.Errorf("run %s/%d started but its stop secret could not be created (run `doomsday stop`): %w", run.Profile, run.Generation, err))
		}
	}
	ent := Entry{RunID: fmt.Sprintf("%s/%d", run.Profile, run.Generation), CharterID: run.CharterID, Hash: canon.Hash, Generation: run.Generation, Warning: warning}
	if e.Anchored != nil {
		bound, why, err := e.Anchored(ctx, run.Generation)
		if err != nil {
			return Entry{}, recorded(fmt.Errorf("run %s started but its anchor and generation counter could not be recorded (run `doomsday stop`): %w", ent.RunID, err))
		}
		ent.AnchorBound, ent.AnchorNote = bound, why
	}
	return ent, nil
}

func (e *StoreEngine) Stop(ctx context.Context) (StopOutcome, error) {
	if e.StopFn == nil {
		return StopOutcome{}, errors.New("stop is not wired on this install; nothing was stopped")
	}
	return e.StopFn(ctx)
}

// Status reads the latest generation of the profile.
func (e *StoreEngine) Status(ctx context.Context) (Status, error) {
	next, err := e.Store.NextGeneration(ctx, e.Profile)
	if err != nil {
		return Status{}, err
	}
	if next <= 1 {
		return Status{}, nil
	}
	gen := next - 1
	run, err := e.Store.GetRun(ctx, e.Profile, gen)
	if err != nil {
		return Status{}, err
	}
	sc, err := e.Store.LoadCharter(ctx, e.Profile, gen)
	if err != nil {
		return Status{}, err
	}
	members, err := e.Store.LiveMembers(ctx, e.Profile, gen)
	if err != nil {
		return Status{}, err
	}
	h := sc.Hash()
	var fin, finReason string
	var finRound int64
	if v, ok := e.Store.(finishView); ok {
		if fin, finReason, finRound, err = FinishStatus(ctx, v, e.Profile, run); err != nil {
			return Status{}, fmt.Errorf("the finish of generation %d cannot be read: %w", gen, err)
		}
	}
	var head store.AuditHead
	if r, ok := e.Store.(store.AuditHeadReader); ok {
		if head, err = r.AuditHead(ctx, e.Profile, gen); err != nil {
			return Status{}, fmt.Errorf("the audit chain of generation %d does not verify: %w", gen, err)
		}
	}
	return Status{AuditSeq: head.Seq, AuditHead: hex.EncodeToString(head.MAC[:min(len(head.MAC), 8)]), Active: run.State != store.StateQuiescent, RunID: fmt.Sprintf("%s/%d", e.Profile, gen), CharterID: run.CharterID,
		Hash: hex.EncodeToString(h[:]), State: string(run.State), Generation: gen, Lines: sc.Lines, Members: len(members),
		Finish: fin, FinishReason: finReason, FinishRound: finRound}, nil
}

func toStore(c Charter, profile string, gen int64) store.Charter {
	sc := store.Charter{
		Profile: profile, Generation: gen, Goal: c.Goal,
		Limits: store.Limits{TreeDepth: c.Limits.TreeDepth, LivePerTree: c.Limits.LivePerTree, SpawnRate: c.Limits.SpawnRate,
			OpenSessions: c.Limits.OpenSessions, MaxConcurrent: c.Limits.MaxConcurrent, MaxTotal: c.Limits.MaxTotal,
			BudgetMicro: c.Limits.BudgetMicro, NoCeiling: c.Limits.NoCeiling,
			QueueMaxRows: c.Limits.QueueMaxRows, QueueMaxBytes: c.Limits.QueueMaxBytes, SafeMinConcurrent: c.Limits.SafeMinConcurrent},
		BossPin:     store.Pin{Provider: c.Pins.Boss.Provider, Model: c.Pins.Boss.Model, Version: c.Pins.Boss.Version, PurposePolicy: c.Pins.Judging},
		VerifierPin: store.Pin{Provider: c.Pins.Verifier.Provider, Model: c.Pins.Verifier.Model, Version: c.Pins.Verifier.Version, PurposePolicy: c.Pins.Judging},
	}
	for _, l := range c.Lines {
		sc.Lines = append(sc.Lines, string(l))
	}
	for _, k := range c.Criteria {
		if k.Kind == "command" {
			sc.Criteria = append(sc.Criteria, store.Criterion{Kind: "command", Spec: k.Command, Expected: k.Expected})
		} else {
			sc.Criteria = append(sc.Criteria, store.Criterion{Kind: "rubric", Spec: k.Text})
		}
	}
	if c.PerAsk.AutoApproves() {
		sc.PerAsk = store.PerAsk{Declared: true, SpendMicro: c.PerAsk.SpendMicro, Tasks: c.PerAsk.Tasks, Access: c.PerAsk.Access, Depth: c.PerAsk.Depth,
			RunSpendMicro: c.PerAsk.RunSpendMicro, RunTasks: c.PerAsk.RunTasks, AnswerClasses: slices.Clone(c.PerAsk.AnswerClasses)}
	}
	return sc
}

// VaultKeyer derives the store's MAC keys from the profile's vault data key
// (secrets.DeriveKeyIfExists: it never creates a key, so a profile without one
// fails closed).
type VaultKeyer struct {
	DB      *sql.DB
	Profile string
}

func (k VaultKeyer) MAC(domain string, parts ...[]byte) ([]byte, error) {
	key, err := secrets.DeriveKeyIfExists(context.Background(), k.DB, k.Profile, "doomsday/mac")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", store.ErrUnverifiable, err)
	}
	return store.NewHMACKeyer(key).MAC(domain, parts...)
}

// ErrRunRecorded marks an Enter failure that happened AFTER this call recorded the run: the run exists and is this call's. A
// failure without it happened before the record (a refusal), so a run that exists then is somebody else's.
var ErrRunRecorded = errors.New("the run was recorded by this entry")

type recordedErr struct{ err error }

func (r recordedErr) Error() string      { return r.err.Error() }
func (r recordedErr) Unwrap() error      { return r.err }
func (recordedErr) Is(target error) bool { return target == ErrRunRecorded }

func recorded(err error) error { return recordedErr{err} }

// ErrEntryUnknown marks an Enter failure whose effect on the run is unknown (the commit's outcome could not be read): the run may
// exist, nothing was torn down, and a caller that made anything for the run keeps it.
var ErrEntryUnknown = errors.New("the outcome of the entry is unknown")

type unknownErr struct{ err error }

func (u unknownErr) Error() string { return u.err.Error() }
func (u unknownErr) Unwrap() error { return u.err }
func (unknownErr) Is(t error) bool { return t == ErrEntryUnknown }

func unknownEntry(err error) error { return unknownErr{err} }
