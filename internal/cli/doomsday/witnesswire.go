package doomsday

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// WitnessSetup is what the production engine needs to run the witness of note 13.5: the database file the pinned
// connection binds, the vault-derived MAC keys, and the OS secret store of the anchor.
type WitnessSetup struct {
	DBPath string
	Keys   store.Keyer
	// Secrets is the anchor's store (stop.OSSecretStore{} in production). Nil, or a store that cannot be written,
	// makes the run `anchor-bound: no` and the entry says rollback detection is weaker.
	Secrets stop.SecretStore
	// Warn receives the best-effort failures that are shown and never block.
	Warn func(string)
}

// PinFunc makes the pinned database connection (stop.Pin). It is a seam for tests, which count the attempts or refuse them.
var PinFunc = stop.Pin

var (
	pinsMu sync.Mutex
	pins   = map[string]*stop.Pinned{}
)

// Sequencer builds the profile's sequencer. The pinned connection is made once per process and database file: the
// host holds it for its life, a CLI command for its run. Where the pin cannot be made (no Linux file identities, a
// database that is not a regular file) the sequencer has no pin and every chained operation refuses.
func (w Wiring) Sequencer(ctx context.Context) (*stop.Sequencer, error) {
	ws := w.Witness
	if ws == nil {
		return nil, errNoWitness
	}
	key := ws.DBPath + "\x00" + w.RunDir.Dir
	pinsMu.Lock()
	defer pinsMu.Unlock()
	pin := pins[key]
	if pin == nil {
		var err error
		if pin, err = PinFunc(ctx, stop.PinOptions{DBPath: ws.DBPath}); err != nil {
			return nil, fmt.Errorf("the pinned database connection cannot be made, so freshness cannot be proven: %w", err)
		}
		pins[key] = pin
	}
	seq := &stop.Sequencer{
		W:       stop.Witness{Dir: w.RunDir, Keys: ws.Keys},
		Pin:     pin,
		Counter: stop.GenCounter{Dir: w.RunDir, Keys: ws.Keys},
		Profile: w.Profile,
		Warn:    ws.Warn,
	}
	if ws.Secrets != nil {
		seq.Anchor = &stop.Anchor{Store: ws.Secrets, Keys: ws.Keys, Profile: w.Profile}
	}
	return seq, nil
}

// ClosePins releases the pinned connections of this process (tests; the host keeps them for its life).
func ClosePins() {
	pinsMu.Lock()
	defer pinsMu.Unlock()
	for k, p := range pins {
		_ = p.CloseClean()
		delete(pins, k)
	}
}

var errNoWitness = errors.New("no witness is wired on this install, so freshness cannot be proven and no run can be entered")

// SeqStore is the store with every chained write routed through the sequencer (pending before, committed after,
// freshness first) and every admission checked. Everything it does not override is the store's own.
type SeqStore struct {
	*store.SQLStore
	Seq *stop.Sequencer
	// Err is why a store with neither Seq nor Lazy refuses (fail closed). It is not read when Lazy is set: the build's own error
	// is returned instead.
	Err error
	// Lazy, when set and Seq is nil, builds the sequencer on the first chained write or admission (Wiring.Chained): a store
	// that is only constructed, as every coder host does, pins nothing. A failure is not remembered; the next use tries again.
	Lazy func(ctx context.Context) (*stop.Sequencer, error)

	mu     sync.Mutex
	flight *seqFlight // the build in progress, shared by every caller that arrives meanwhile
}

type seqFlight struct {
	done chan struct{}
	seq  *stop.Sequencer
	err  error
}

// seqBuildTimeout bounds one build of the sequencer (the pinned connection on a possibly busy database). A caller's own
// cancellation does not cancel a build other callers wait on; the timeout does.
const seqBuildTimeout = 10 * time.Second

// seq is the sequencer, built on first use when the store is lazy. The build runs outside the lock and once at a time: callers
// that arrive meanwhile wait for it, or leave when their own context ends.
func (s *SeqStore) seq(ctx context.Context) (*stop.Sequencer, error) {
	s.mu.Lock()
	if s.Seq != nil {
		defer s.mu.Unlock()
		return s.Seq, nil
	}
	if s.Lazy == nil {
		defer s.mu.Unlock()
		return nil, s.unwired()
	}
	if f := s.flight; f != nil {
		s.mu.Unlock()
		select {
		case <-f.done:
			return f.seq, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &seqFlight{done: make(chan struct{})}
	s.flight = f
	s.mu.Unlock()

	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), seqBuildTimeout)
	f.seq, f.err = s.Lazy(bctx)
	cancel()
	s.mu.Lock()
	if f.err == nil {
		s.Seq = f.seq
	}
	s.flight = nil
	s.mu.Unlock()
	close(f.done)
	return f.seq, f.err
}

func (s *SeqStore) sequence(ctx context.Context, gen int64, kind string, ending, ended bool, fn func(context.Context) error) error {
	seq, err := s.seq(ctx)
	if err != nil {
		return err
	}
	return seq.Sequence(ctx, gen, kind, ending, ended, fn)
}

func (s *SeqStore) check(ctx context.Context, gen int64) error {
	seq, err := s.seq(ctx)
	if err != nil {
		return err
	}
	return seq.Check(ctx, gen)
}

func (s *SeqStore) unwired() error {
	if s.Err != nil {
		return s.Err
	}
	return errNoWitness
}

var (
	_ store.Store         = (*SeqStore)(nil)
	_ store.MultiConsumer = (*SeqStore)(nil)
)

func (s *SeqStore) Enter(ctx context.Context, c store.Charter, confirmed [32]byte, by string) (r store.Run, err error) {
	seq, err := s.seq(ctx)
	if err != nil {
		return r, err
	}
	if err = seq.BeginEntry(ctx, c.Generation); err != nil {
		return r, err
	}
	err = s.sequence(ctx, c.Generation, stop.KindEnter, false, false, func(ctx context.Context) (e error) {
		r, e = s.SQLStore.Enter(ctx, c, confirmed, by)
		return e
	})
	return r, err
}

func (s *SeqStore) Advance(ctx context.Context, profile string, gen int64, to store.RunState, by, cause string) (r store.Run, err error) {
	err = s.sequence(ctx, gen, "advance", true, to == store.StateQuiescent, func(ctx context.Context) (e error) {
		r, e = s.SQLStore.Advance(ctx, profile, gen, to, by, cause)
		return e
	})
	return r, err
}

func (s *SeqStore) AppendAudit(ctx context.Context, profile string, gen int64, kind, payload string) (row store.AuditRow, err error) {
	err = s.sequence(ctx, gen, "audit", false, false, func(ctx context.Context) (e error) {
		row, e = s.SQLStore.AppendAudit(ctx, profile, gen, kind, payload)
		return e
	})
	return row, err
}

func (s *SeqStore) ConsumeCumulative(ctx context.Context, profile string, gen int64, kind, approvalID string, n, limit int64) error {
	return s.sequence(ctx, gen, "counter", false, false, func(ctx context.Context) error {
		return s.SQLStore.ConsumeCumulative(ctx, profile, gen, kind, approvalID, n, limit)
	})
}

func (s *SeqStore) ConsumeMany(ctx context.Context, profile string, gen int64, approvalID string, charges []store.Charge, audit store.AuditEntry) error {
	return s.sequence(ctx, gen, "consume", false, false, func(ctx context.Context) error {
		return s.SQLStore.ConsumeMany(ctx, profile, gen, approvalID, charges, audit)
	})
}

func (s *SeqStore) ConsumeApproval(ctx context.Context, profile string, gen int64, approvalID, op, argsHash string, charges []store.Charge, audit store.AuditEntry) error {
	return s.sequence(ctx, gen, "consume", false, false, func(ctx context.Context) error {
		return s.SQLStore.ConsumeApproval(ctx, profile, gen, approvalID, op, argsHash, charges, audit)
	})
}

func (s *SeqStore) IssueApproval(ctx context.Context, in store.IssueInput) (a store.Approval, err error) {
	err = s.sequence(ctx, in.Generation, "approval", false, false, func(ctx context.Context) (e error) {
		a, e = s.SQLStore.IssueApproval(ctx, in)
		return e
	})
	return a, err
}

func (s *SeqStore) RescindApproval(ctx context.Context, profile string, gen int64, id, by string) error {
	return s.sequence(ctx, gen, "rescind", false, false, func(ctx context.Context) error {
		return s.SQLStore.RescindApproval(ctx, profile, gen, id, by)
	})
}

// Admit and Register check freshness (the witness against the chained head) before the store's own admission. A
// stale database admits nothing.
func (s *SeqStore) Admit(ctx context.Context, q store.DBTX, profile string, gen int64) error {
	if err := s.check(ctx, gen); err != nil {
		return &store.Refusal{Reason: err.Error()}
	}
	return s.SQLStore.Admit(ctx, q, profile, gen)
}

func (s *SeqStore) Register(ctx context.Context, m store.Member) error {
	if err := s.check(ctx, m.Generation); err != nil {
		return &store.Refusal{Reason: err.Error()}
	}
	return s.SQLStore.Register(ctx, m)
}

// Chained is the store with its chained writes sequenced. When the sequencer cannot be built the store still
// reads, and everything that would write a chained row or admit work refuses with the reason.
//
// The sequencer (and with it the pinned connection) is built when the store first needs it, not here: a coder host builds this
// store whether or not a run exists, and with no run nothing may be pinned.
func (w Wiring) Chained() *SeqStore {
	return &SeqStore{SQLStore: w.Store, Lazy: func(ctx context.Context) (*stop.Sequencer, error) {
		return w.Sequencer(ctx)
	}}
}

// Fresh is HostGate.Fresh: the freshness check of one admission over this wiring.
func (w Wiring) Fresh() func(ctx context.Context, gen int64) error {
	return func(ctx context.Context, gen int64) error {
		seq, err := w.Sequencer(ctx)
		if err != nil {
			return err
		}
		return seq.Check(ctx, gen)
	}
}
