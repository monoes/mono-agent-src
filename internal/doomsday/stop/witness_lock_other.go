//go:build !linux

package stop

import "errors"

// ErrWitnessBusy: see the Linux file; no lock exists here.
var ErrWitnessBusy = errors.New("stop: the witness lock stayed busy")

func (w Witness) lockBounded() (func(), error) { return nil, errLockUnsupported }
