package policy

import (
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// SystemClock is the production governor.Clock: Go's monotonic reading since construction, so a wall-clock change
// (NTP step, manual set, DST) never moves a backoff window.
type SystemClock struct{ start time.Time }

var _ governor.Clock = SystemClock{}

// NewSystemClock returns a monotonic clock starting now.
func NewSystemClock() SystemClock { return SystemClock{start: time.Now()} }

// Now implements governor.Clock (time.Since uses the monotonic reading embedded in start).
func (c SystemClock) Now() time.Duration { return time.Since(c.start) }
