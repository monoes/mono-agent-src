//go:build !linux

package stop

import "errors"

// ErrSecondHost: another host already holds this run directory.
var ErrSecondHost = errors.New("stop: another host already holds this run directory")

var errLockUnsupported = errors.New("stop: host and witness locks are only implemented on Linux; the mode does not start here")

func (w Witness) LockWitness() (func(), error) { return nil, errLockUnsupported }

func AcquireHost(RunDir) (func(), error) { return nil, errLockUnsupported }
