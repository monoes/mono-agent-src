//go:build linux || darwin || windows

package stop

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// ErrWitnessBusy: the witness lock stayed taken for the whole bounded wait. Waits on witness.lock are bounded
// retries, never an unbounded wait (note 13.5 section 3): the caller refuses this attempt and may try again.
var ErrWitnessBusy = errors.New("stop: the witness lock stayed busy")

const (
	lockTries = 100
	lockEvery = 20 * time.Millisecond
)

// lockBounded takes the witness lock with non-blocking attempts, jittered, for about two seconds.
func (w Witness) lockBounded() (func(), error) {
	if err := w.Dir.fsys().MkdirAll(w.Dir.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("stop: the run directory cannot be created: %w", err)
	}
	if err := VerifyHostLock(w.Dir); err != nil { // the host lock this process holds must still be the file at its name
		return nil, err
	}
	for i := 0; i < lockTries; i++ {
		unlock, err := flock(w.lockPath(), false)
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, ErrSecondHost) { // ErrSecondHost is what flock (LockFileEx on Windows) reports for a taken lock
			return nil, fmt.Errorf("stop: the witness lock cannot be taken: %w", err)
		}
		time.Sleep(lockEvery/2 + time.Duration(rand.Int64N(int64(lockEvery))))
	}
	return nil, ErrWitnessBusy
}
