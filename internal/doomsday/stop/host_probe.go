package stop

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
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
	d := currentProbeDirs()
	return hostChecks{
		Lock: func() error { return lockSelfTest(d.Run, AcquireHost) },
		Peer: func() error { return peerSelfTest(d.Run, peerUID) },
		Pin:  func() error { return pinSelfTest(d.DB, realPinner, defaultPinScenarios, pinRefusalExpected) },
	}
}

// probeDirs are where the self-tests run: next to the real database (pin) and in the real run directory (lock, peer
// socket), so what is proven is proven on the volume the run uses. Empty means the system temp directory, which only
// tests and a bare Probe() use; the production wiring calls ProbeAt.
type probeDirs struct{ DB, Run string }

var (
	probeMu   sync.Mutex
	probeCur  probeDirs
	probeCurM sync.Mutex
)

func currentProbeDirs() probeDirs { probeCurM.Lock(); defer probeCurM.Unlock(); return probeCur }

// ProbeAt is Probe with the self-tests run in dbDir (the directory of the database file) and runDir.
func ProbeAt(dbDir, runDir string) error {
	probeMu.Lock()
	defer probeMu.Unlock()
	probeCurM.Lock()
	probeCur = probeDirs{DB: dbDir, Run: runDir}
	probeCurM.Unlock()
	defer func() { probeCurM.Lock(); probeCur = probeDirs{}; probeCurM.Unlock() }()
	return Probe()
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

// scratchDir makes a private directory under parent (the system temp directory when parent is empty), after the
// volume of parent was found fit for the binding. The returned function removes it, and anything a scenario renamed it
// to (extra names), whatever the outcome.
func scratchDir(parent, prefix string) (string, func(...string), error) {
	if parent == "" {
		parent = os.TempDir()
	} else if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", nil, err
	}
	if err := checkVolume(parent); err != nil {
		return "", nil, err
	}
	d, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return "", nil, err
	}
	return d, func(extra ...string) {
		_ = os.RemoveAll(d)
		for _, e := range extra {
			_ = os.RemoveAll(e)
		}
	}, nil
}

// lockSelfTest: a second host on one run directory is refused, a released lock can be taken again, and the witness
// lock excludes a second holder.
func lockSelfTest(parent string, acquire func(RunDir) (func(), error)) error {
	dir, done, err := scratchDir(parent, ".ma-lock-")
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
func peerSelfTest(parent string, peer func(net.Conn) (uint32, error)) error {
	dir, done, err := scratchDir(parent, ".ma-peer-")
	if err != nil {
		return err
	}
	defer done()
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("a unix socket cannot be made at %q (%d bytes; a socket path is limited to about 104, so a long run directory or TMPDIR refuses the mode): %w", path, len(path), err)
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
