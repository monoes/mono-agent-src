package stop

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// settleEIO stub (red commit).
func settleEIO[T any](pid int, read func() (T, error), probe func(pid int) error, pause func()) (T, error) {
	var zero T
	return zero, errSessGone
}

var _ = errors.Is
var _ = fmt.Sprintf
var _ = syscall.EIO
var _ = time.Second
