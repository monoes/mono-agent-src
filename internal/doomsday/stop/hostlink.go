package stop

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

// The host and `doomsday stop` are different processes, so the CLI's Stopper holds no *Gate: the host holds it. A Stop
// from the CLI reaches the host's gate the way note 13.1 section 7 says: the marker and the database intent (read by
// HostWatcher), plus the emergency message as the fast path, and the host answers with an acknowledgement file that
// carries the gate's outcome (HostAck). Without a proven answer the Stop is not clean: it never declares quiescent on
// what it did not see.

// HostKind says what a Stop learnt of the host-side gate of its generation.
type HostKind int

const (
	// HostUnproven: no host answered and none was shown to be absent. The generation is not declared quiescent.
	HostUnproven HostKind = iota
	// HostNone: no host process holds a gate of this generation (nothing listens on its emergency socket).
	HostNone
	// HostClosed: the host's gate closed with nothing live (BARRIER).
	HostClosed
	// HostOpen: the host's gate closed with starters, unknown tickets or a running operation (see the ack).
	HostOpen
)

// HostOutcome is a Stop's knowledge of the host-side gate.
type HostOutcome struct {
	Kind    HostKind
	Ack     HostAck
	Note    string
	Pending []uint64 // begun operations still finishing: excluded from the unit's kill
}

// HostAck is what the host's gate wrote once it closed.
type HostAck struct {
	Generation uint64   `json:"gen"`
	Kind       int      `json:"kind"`
	Starters   []uint64 `json:"starters,omitempty"`
	Aborting   []uint64 `json:"aborting,omitempty"`
	Unknown    []uint64 `json:"unknown,omitempty"`
	Operations []string `json:"operations,omitempty"`
	Pending    []uint64 `json:"pending,omitempty"`
	// Nonce names the host incarnation that wrote the ack; MAC signs every field with the per-run secret (as the marker is).
	// An ack without a valid MAC is no ack: a process that can only create files in the run directory cannot say "closed".
	Nonce string `json:"nonce,omitempty"`
	MAC   string `json:"mac,omitempty"`
}

func ackMAC(secret []byte, a HostAck) string {
	a.MAC = ""
	body, _ := json.Marshal(a)
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("monoagent-doomsday-host-ack-v1\x00"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func newNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // no randomness: nothing safe can be signed
	}
	return hex.EncodeToString(b)
}

// RemoveAck deletes the generation's acknowledgement: a new host incarnation of a generation calls it before its gate is
// reachable, so nothing a previous incarnation (or anyone) left is read as this one's answer.
func (r RunDir) RemoveAck(gen uint64) error {
	err := r.fsys().Remove(r.AckPath(gen))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return r.fsys().SyncDir(r.Dir)
}

// AckPath is the acknowledgement file of one generation.
func (r RunDir) AckPath(gen uint64) string { return filepath.Join(r.Dir, fmt.Sprintf("%d.ack", gen)) }

// WriteAck replaces the generation's acknowledgement durably, signed with the per-run secret (an unreadable secret is an
// error: an unsigned ack would not be trusted by anyone).
func (r RunDir) WriteAck(a HostAck) error {
	fs := r.fsys()
	if err := fs.MkdirAll(r.Dir, 0o700); err != nil {
		return err
	}
	secret, err := r.ReadSecret(a.Generation)
	if err != nil {
		return err
	}
	if a.Nonce == "" {
		a.Nonce = newNonce()
	}
	a.MAC = ackMAC(secret, a)
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_ = fs.Remove(r.AckPath(a.Generation))
	f, err := fs.CreateExclusive(r.AckPath(a.Generation), 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return fs.SyncDir(r.Dir)
}

// ReadAck reads the acknowledgement; ok is false when there is none, it does not parse, it names another generation, it has
// no nonce, or its MAC does not verify under the per-run secret (or the secret cannot be read): all of those are none.
func (r RunDir) ReadAck(gen uint64) (a HostAck, ok bool) {
	b, err := r.fsys().ReadFile(r.AckPath(gen))
	if err != nil || json.Unmarshal(b, &a) != nil || a.Generation != gen || a.Nonce == "" {
		return HostAck{}, false
	}
	secret, err := r.ReadSecret(gen)
	if err != nil || !hmac.Equal([]byte(a.MAC), []byte(ackMAC(secret, a))) {
		return HostAck{}, false
	}
	return a, true
}

// HostLink is how a Stopper without a Gate learns the outcome of the host's gate.
type HostLink interface {
	CloseHost(ctx context.Context, gen uint64) HostOutcome
}

func outcomeOfAck(a HostAck) HostOutcome {
	o := HostOutcome{Ack: a, Pending: a.Pending}
	if OutcomeKind(a.Kind) == Barrier {
		o.Kind = HostClosed
		return o
	}
	o.Kind = HostOpen
	o.Note = fmt.Sprintf("the host's gate closed as %s (starters %v, unknown %v, operations %v)", OutcomeKind(a.Kind), a.Starters, a.Unknown, a.Operations)
	return o
}

// ProcessHostLink is the HostLink of a Stopper in another process than the host: the emergency message, then the ack.
type ProcessHostLink struct {
	Dir   RunDir
	Clock Clock
	// Wait is how long the ack is waited for (T_close of the host plus margin). Zero: 15 s.
	Wait time.Duration
	// Poll is the ack poll interval. Zero: 50 ms.
	Poll time.Duration
	// Lock takes the host lock of the run directory; nil is AcquireHost. A host that cannot be reached is proven absent only
	// when this lock can be taken (the host holds it for its life): a missing or refusing socket alone proves nothing, since
	// anyone who can write the run directory can unlink the socket.
	Lock func(RunDir) (release func(), err error)
}

// hostAbsent is the proof of absence: the host lock is free (and is released again at once).
func (l ProcessHostLink) hostAbsent() bool {
	lock := l.Lock
	if lock == nil {
		lock = AcquireHost
	}
	release, err := lock(l.Dir)
	if err != nil {
		return false
	}
	release()
	return true
}

// CloseHost asks the host to close and waits for its acknowledgement. A host that is not there (nothing listens, or a
// socket nobody holds) holds no gate: HostNone. Anything else without an ack is HostUnproven.
func (l ProcessHostLink) CloseHost(ctx context.Context, gen uint64) HostOutcome {
	if a, ok := l.Dir.ReadAck(gen); ok { // a repeated Stop, or a host that already closed on the marker
		return outcomeOfAck(a)
	}
	err := SendEmergency(l.Dir, gen)
	if errors.Is(err, ErrSecretUnreadable) { // cannot sign: the marker is the path, but an absent host is still provable
		if c, derr := net.DialTimeout("unix", l.Dir.SockPath(gen), 2*time.Second); derr != nil {
			err = derr
		} else {
			c.Close()
		}
	}
	if err != nil && absent(err) {
		if a, ok := l.Dir.ReadAck(gen); ok { // the host answered through the marker and exited
			return outcomeOfAck(a)
		}
		if l.hostAbsent() {
			return HostOutcome{Kind: HostNone, Note: "no host holds a gate of this generation (the host lock is free)"}
		}
		err = fmt.Errorf("the emergency socket is missing but the host lock is held: %w", err)
	}
	wait, poll := l.Wait, l.Poll
	if wait == 0 {
		wait = 15 * time.Second
	}
	if poll == 0 {
		poll = 50 * time.Millisecond
	}
	deadline := l.Clock.Now().Add(wait)
	for {
		if a, ok := l.Dir.ReadAck(gen); ok {
			return outcomeOfAck(a)
		}
		if !l.Clock.Now().Before(deadline) {
			why := "the host did not acknowledge that its gate closed"
			if err != nil {
				why += ": " + err.Error()
			}
			return HostOutcome{Kind: HostUnproven, Note: why}
		}
		select {
		case <-ctx.Done():
			return HostOutcome{Kind: HostUnproven, Note: "stopped waiting for the host: " + ctx.Err().Error()}
		case <-l.Clock.After(poll):
		}
	}
}

// absent says the dial failed because no process listens: the socket file is missing or nobody holds it.
func absent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, wsaConnRefused)
}

// wsaConnRefused is WSAECONNREFUSED (10061), what Windows answers for a dial to a socket file nobody listens on. It is not
// syscall.ECONNREFUSED there (a different number), and without it a stale socket reads as a host that refused.
const wsaConnRefused = syscall.Errno(10061)

// HostWatcher runs in the host, per generation: it polls the marker and the run state (250 ms, a target, not a
// guarantee), and on a stop (or an emergency message, Kick) closes the gate, writes the ack, and keeps the crash-only
// rule for a start call that does not return (EnforceHung).
type HostWatcher struct {
	Gate      *Gate
	Dir       RunDir
	Gen       uint64
	CharterID string
	// Stops says whether the run's state is no longer running (read from the database each poll; an error counts as a stop).
	Stops func(ctx context.Context) bool
	Clock Clock
	Tick  time.Duration
	// Exit is the crash-only exit (EnforceHung): the host terminates itself. Nil leaves a hung starter unenforced, which
	// the caller must not do in production.
	Exit func(starters []uint64)

	kick   chan struct{}
	kicked atomic.Bool
	nonce  string // this host incarnation, signed into the ack
}

// Kick is the emergency fast path (EmergencyServer.OnStop).
func (w *HostWatcher) Kick() {
	w.kicked.Store(true)
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// NewHostWatcher makes a watcher; call Run in a goroutine.
func NewHostWatcher(w *HostWatcher) *HostWatcher {
	w.kick = make(chan struct{}, 1)
	w.nonce = newNonce()
	if w.Tick == 0 {
		w.Tick = 250 * time.Millisecond
	}
	return w
}

// Run returns after the gate closed and the ack was written, or when ctx ends.
//
// The crash-only enforcement runs only while the gate is open: it ends with Run, which is when the gate has closed (a starter
// that is still draining then is the Stop's to report, not a reason for the host to exit). A starter's time inside the host's
// own registry write of Started does not count towards T_hung (Gate.HungStarters).
func (w *HostWatcher) Run(ctx context.Context) {
	ectx, stopEnforce := context.WithCancel(ctx)
	defer stopEnforce()
	if w.Exit != nil {
		go func() {
			for ectx.Err() == nil {
				EnforceHung(ectx, w.Gate, w.Clock, w.Tick, w.Exit)
				select {
				case <-ectx.Done():
					return
				case <-w.Clock.After(w.Tick):
				}
			}
		}()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
		case <-w.Clock.After(w.Tick):
		}
		if w.kicked.Load() || w.Gate.Sealed() || w.Dir.ReadMarker(w.Gen, w.CharterID).Stops() || (w.Stops != nil && w.Stops(ctx)) {
			stopEnforce() // the gate closes now: a starter still draining is reported by the Stop, it does not end the host
			w.closeAndAck()
			return
		}
	}
}

func (w *HostWatcher) closeAndAck() {
	o := w.Gate.Close()
	a := HostAck{Generation: w.Gen, Kind: int(o.Kind), Starters: o.Starters, Aborting: o.Aborting, Unknown: o.Unknown,
		Operations: o.Operations, Pending: w.Gate.ContinuingTickets(), Nonce: w.nonce}
	for i := 0; i < 5; i++ { // an ack that cannot be written is no ack: the Stop reports the host as unproven
		if w.WriteAck(a) == nil {
			return
		}
	}
}

// WriteAck is the ack write (a method so a test can see it).
func (w *HostWatcher) WriteAck(a HostAck) error { return w.Dir.WriteAck(a) }
