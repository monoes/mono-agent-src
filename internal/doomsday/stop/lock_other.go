//go:build !linux && !darwin && !windows

package stop

import "errors"

// ErrSecondHost: another host already holds this run directory.
var ErrSecondHost = errors.New("stop: another host already holds this run directory")

var errLockUnsupported = errors.New("stop: host and witness locks are only implemented on Linux, macOS and Windows; the mode does not start here")

func (w Witness) LockWitness() (func(), error) { return nil, errLockUnsupported }

func AcquireHost(RunDir) (func(), error) { return nil, errLockUnsupported }

// flock has no implementation here (host_probe.go and the lock helpers are compiled everywhere).
func flock(string, bool) (func(), error) { return nil, errLockUnsupported }

// ErrHostLockLost and VerifyHostLock exist for the callers that compile everywhere; no lock is ever held here.
var ErrHostLockLost = errors.New("stop: the host lock file (host.lock in the run directory) was removed or replaced, for example by a backup or cleanup tool or by a worker, so another host could now be running; restart the host")

func VerifyHostLock(RunDir) error { return nil }
