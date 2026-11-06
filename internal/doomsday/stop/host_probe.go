package stop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// The host steps of the probe on Windows and macOS: the pieces of the mode that are not the process containment. Each is
// a RUNTIME proof on a scratch directory, not a build-time claim, and a platform where one of them cannot be done
// refuses to start. On Linux the probe has its own steps. hostChecksNow is a seam for tests.

const (
	stepHostLock = "host and witness lock"
	stepPeer     = "peer credentials of the emergency channel"
	stepPin      = "pinned database binding"
)

// hostChecks are the three self-tests. A nil one is a step that does not exist here, and fails.
type hostChecks struct{ Lock, Peer, Pin func() error }

var hostChecksNow = func() hostChecks {
	return hostChecks{
		Lock: func() error { return lockSelfTest(AcquireHost) },
		Peer: func() error { return peerSelfTest(peerUID) },
		Pin:  func() error { return pinSelfTest(realPinner) },
	}
}

func hostProbeSteps(h hostChecks) []ProbeStep {
	step := func(name string, f func() error) ProbeStep {
		if f == nil {
			f = func() error { return errors.New("not implemented on this platform") }
		}
		return ProbeStep{Name: name, Check: f}
	}
	return []ProbeStep{step(stepHostLock, h.Lock), step(stepPeer, h.Peer), step(stepPin, h.Pin)}
}

func scratchDir(prefix string) (string, func(), error) {
	d, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", nil, err
	}
	return d, func() { _ = os.RemoveAll(d) }, nil
}

// lockSelfTest: a second host on one run directory is refused, a released lock can be taken again, and the witness
// lock excludes a second holder.
func lockSelfTest(acquire func(RunDir) (func(), error)) error {
	dir, done, err := scratchDir("ma-lock")
	if err != nil {
		return err
	}
	defer done()
	r := RunDir{Dir: dir}
	rel, err := acquire(r)
	if err != nil {
		return fmt.Errorf("the host lock cannot be taken: %w", err)
	}
	if rel2, err := acquire(r); !errors.Is(err, ErrSecondHost) {
		if rel2 != nil {
			rel2()
		}
		rel()
		return fmt.Errorf("a second host on one run directory was not refused (%v)", err)
	}
	rel()
	rel3, err := acquire(r)
	if err != nil {
		return fmt.Errorf("a released host lock cannot be taken again: %w", err)
	}
	rel3()
	w := Witness{Dir: r}
	unlock, err := w.lockBounded()
	if err != nil {
		return fmt.Errorf("the witness lock cannot be taken: %w", err)
	}
	if u2, err := flock(w.lockPath(), false); !errors.Is(err, ErrSecondHost) {
		if u2 != nil {
			u2()
		}
		unlock()
		return fmt.Errorf("a second holder of the witness lock was not refused (%v)", err)
	}
	unlock()
	return nil
}

// peerSelfTest: a connection from this process to a listener of ours is identified as this user.
func peerSelfTest(peer func(net.Conn) (uint32, error)) error {
	dir, done, err := scratchDir("ma-peer")
	if err != nil {
		return err
	}
	defer done()
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("a unix socket cannot be made: %w", err)
	}
	defer ln.Close()
	type acc struct {
		c   net.Conn
		err error
	}
	got := make(chan acc, 1)
	go func() { c, err := ln.Accept(); got <- acc{c, err} }()
	cl, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return err
	}
	defer cl.Close()
	var a acc
	select {
	case a = <-got:
	case <-time.After(5 * time.Second):
		return errors.New("the probe connection was not accepted")
	}
	if a.err != nil {
		return a.err
	}
	defer a.c.Close()
	uid, err := peer(a.c)
	if err != nil {
		return fmt.Errorf("the peer of a connection cannot be identified: %w", err)
	}
	if uid != uint32(os.Getuid()) {
		return fmt.Errorf("the peer was identified as %d, not as this user", uid)
	}
	return nil
}

// pinner is what pinSelfTest needs of a pinned connection.
type pinner interface {
	Check() error
	Do(ctx context.Context, fn func(c *sql.Conn, precommit func() error) error) error
	discard()
}

func realPinner(ctx context.Context, o PinOptions) (pinner, error) {
	p, err := Pin(ctx, o)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Pinned) discard() { p.mu.Lock(); p.discardLocked(); p.mu.Unlock() }

// pinSelfTest pins a scratch database and proves the binding does what the mode relies on: the pin reports nothing while
// the file is untouched, the connection works, and a rename of the database away, and a rename of another file over
// it, are each either refused by the system or reported by the pin. A rename that happened and was NOT reported fails
// the step.
func pinSelfTest(mk func(context.Context, PinOptions) (pinner, error)) error {
	scenarios := map[string]func(db, other string) error{
		"moving the database away": func(db, _ string) error { return os.Rename(db, db+".moved") },
		"replacing the database":   func(db, other string) error { return os.Rename(other, db) },
	}
	for name, op := range scenarios {
		if err := pinScenario(mk, op); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func pinScenario(mk func(context.Context, PinOptions) (pinner, error), op func(db, other string) error) error {
	dir, done, err := scratchDir("ma-pin")
	if err != nil {
		return err
	}
	defer done()
	db, other := filepath.Join(dir, "p.db"), filepath.Join(dir, "o.db")
	if err := makeScratchDB(db); err != nil {
		return err
	}
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := mk(ctx, PinOptions{DBPath: db})
	if err != nil {
		return fmt.Errorf("the database cannot be pinned: %w", err)
	}
	defer p.discard()
	if err := p.Check(); err != nil {
		return fmt.Errorf("an untouched pinned database is reported as swapped: %w", err)
	}
	if err := p.Do(ctx, func(c *sql.Conn, _ func() error) error {
		_, err := c.ExecContext(ctx, `INSERT INTO t(x) VALUES (1)`)
		return err
	}); err != nil {
		return fmt.Errorf("the pinned connection does not work: %w", err)
	}
	if err := op(db, other); err != nil { // refused by the system: the binding holds, and the pin must stay quiet
		if cerr := p.Check(); cerr != nil {
			return fmt.Errorf("the system refused the change but the pin reports a swap anyway: %w", cerr)
		}
		return nil
	}
	if err := p.Check(); !errors.Is(err, ErrPinSwapped) {
		return fmt.Errorf("the database was changed and the pin did not report it (%v)", err)
	}
	return nil
}

func makeScratchDB(path string) error {
	db, err := sql.Open("sqlite", pinDSN(path)+"?_pragma=journal_mode(WAL)")
	if err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS t(x INTEGER)`); err != nil {
		_ = db.Close()
		return err
	}
	return db.Close()
}
