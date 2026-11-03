package doomsday

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

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
}

var errNoProbe = errors.New("no containment probe is wired, so containment cannot be shown to be available")

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
	run, err := e.Store.Enter(ctx, sc, confirmed, "ack:"+ack.Kind)
	if err != nil {
		return Entry{}, err
	}
	// The audit row is the stream's own record that a script, not a person, may have supplied the lift.
	if _, err := e.Store.AppendAudit(ctx, run.Profile, run.Generation, "start", fmt.Sprintf("ack=%s lines=%d", ack.Kind, ack.Lines)); err != nil {
		return Entry{}, fmt.Errorf("the run started but its audit record failed: %w", err)
	}
	return Entry{RunID: fmt.Sprintf("%s/%d", run.Profile, run.Generation), CharterID: run.CharterID, Hash: canon.Hash, Generation: run.Generation}, nil
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
	return Status{Active: run.State != store.StateQuiescent, RunID: fmt.Sprintf("%s/%d", e.Profile, gen), CharterID: run.CharterID,
		Hash: hex.EncodeToString(h[:]), State: string(run.State), Generation: gen, Lines: sc.Lines, Members: len(members)}, nil
}

func toStore(c Charter, profile string, gen int64) store.Charter {
	sc := store.Charter{
		Profile: profile, Generation: gen, Goal: c.Goal,
		Limits: store.Limits{TreeDepth: c.Limits.TreeDepth, LivePerTree: c.Limits.LivePerTree, SpawnRate: c.Limits.SpawnRate,
			OpenSessions: c.Limits.OpenSessions, MaxConcurrent: c.Limits.MaxConcurrent, MaxTotal: c.Limits.MaxTotal,
			BudgetMicro: c.Limits.BudgetMicro, NoCeiling: c.Limits.NoCeiling},
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
		sc.PerAsk = store.PerAsk{Declared: true, SpendMicro: c.PerAsk.SpendMicro, Tasks: c.PerAsk.Tasks, Access: c.PerAsk.Access, Depth: c.PerAsk.Depth}
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
