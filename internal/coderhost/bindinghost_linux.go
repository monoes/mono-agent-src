//go:build linux

package coderhost

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

// BindingHost owns the peer-credential binding service of the coder host: one service per profile, started when
// the profile first needs it, each on its own checked socket directory with its own generation. A service starts
// only after procbind.Ready, and the host row (migration 148) that names its generation is written before it
// answers anything. Shutdown revokes every registration and closes every socket; the next start has a new
// generation, so an answer of the old one never verifies again.
//
// Nothing here issues a token: the gate (coder_host_gate.go) asks Ensure only when issuance is allowed.
type BindingHost struct {
	DB     *sql.DB
	Binder *procbind.Binder
	// Root is the runtime root. Empty picks procbind.ResolveRoot (XDG_RUNTIME_DIR, else ~/.monoagent/run).
	Root string
	Logf func(format string, args ...any)
	Now  func() time.Time

	mu     sync.Mutex
	svcs   map[string]*bindingService
	closed bool
}

type bindingService struct {
	l    *procbind.SecureListener
	done chan struct{}
}

func (h *BindingHost) logf(f string, a ...any) {
	if h.Logf != nil {
		h.Logf(f, a...)
	}
}

func (h *BindingHost) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *BindingHost) options(profile string) (procbind.SocketOptions, error) {
	if h.Root != "" {
		return procbind.SocketOptions{Root: h.Root, Profile: profile}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return procbind.SocketOptions{}, errors.New("the user's home folder is unknown")
	}
	xdg := os.Getenv("XDG_RUNTIME_DIR")
	root := procbind.ResolveRoot(xdg, home, os.Getuid())
	return procbind.SocketOptions{Root: root, Profile: profile, CreateRoot: root != xdg}, nil
}

// Running says whether the profile's service is up.
func (h *BindingHost) Running(profile string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.svcs[profile]
	if !ok {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// Ensure starts the profile's service if it is not running. It fails closed: any step that does not work leaves
// nothing listening. A host row (migration 148) written before a later step failed stays: it is signed, names a
// generation nobody serves, and so can only make a verifier's question go unanswered (a deny) until the next start
// writes a newer row; it grants nothing.
func (h *BindingHost) Ensure(ctx context.Context, profile string) error {
	if profile == "" {
		return errors.New("a binding service needs a profile")
	}
	if h.Binder == nil { // checked before the nil pointer could land in the service's lease interface (a non-nil interface holding nil)
		return errors.New("the binding host has no registry")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("the binding host is shut down")
	}
	if s, ok := h.svcs[profile]; ok {
		select {
		case <-s.done: // the serve loop ended on its own: start over below with a new generation
			_ = s.l.Close()
			delete(h.svcs, profile)
		default:
			return nil
		}
	}
	if err := procbind.Ready(); err != nil {
		return fmt.Errorf("the binding service cannot run here: %w", err)
	}
	opts, err := h.options(profile)
	if err != nil {
		return err
	}
	l, err := procbind.ListenSecure(opts)
	if err != nil {
		return fmt.Errorf("the binding service socket was refused: %w", err)
	}
	fail := func(err error) error {
		_ = l.Close()
		return err
	}
	// the key is had before the write transaction (deriving it can write)
	rowKey, err := procbind.RowKey(ctx, h.DB, profile) // the host row is signed under the profile's key (a mint site: rowKeyMintSites)
	if err != nil {
		return fail(fmt.Errorf("the binding service could not get its row key: %w", err))
	}
	var g [16]byte
	if _, err := rand.Read(g[:]); err != nil {
		return fail(err)
	}
	generation := hex.EncodeToString(g[:])
	if err := h.writeHostRow(ctx, rowKey, profile, generation); err != nil {
		return fail(fmt.Errorf("the binding service could not record its start: %w", err))
	}
	answerKey, err := procbind.AnswerKey(ctx, h.DB, profile, generation)
	if err != nil {
		return fail(fmt.Errorf("the binding service could not get its answer key: %w", err))
	}
	procs, err := procbind.NewLinuxProcs()
	if err != nil {
		return fail(errors.New("the binding service cannot read the process table"))
	}
	srv := &procbind.Server{Profile: profile, Generation: generation, Key: answerKey, Leases: h.Binder, Procs: procs, UID: os.Getuid(),
		Logf: func(f string, a ...any) { h.logf("binding service: "+f, a...) }}
	s := &bindingService{l: l, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		_ = srv.Serve(l)
	}()
	if h.svcs == nil {
		h.svcs = map[string]*bindingService{}
	}
	h.svcs[profile] = s
	h.logf("binding service for a profile started")
	return nil
}

func (h *BindingHost) writeHostRow(ctx context.Context, key []byte, profile, generation string) error {
	conn, err := h.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if _, err := procbind.InsertHost(ctx, conn, key, profile, generation, h.now()); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

// Shutdown revokes every registration, closes every socket and waits for the serve loops. It is idempotent; after
// it Ensure fails.
func (h *BindingHost) Shutdown() {
	h.mu.Lock()
	h.closed = true
	svcs := h.svcs
	h.svcs = nil
	h.mu.Unlock()
	for _, s := range svcs {
		_ = s.l.Close()
	}
	for _, s := range svcs {
		<-s.done
	}
	if h.Binder != nil {
		h.Binder.Close() // revokes and closes every entry's handle
	}
}
