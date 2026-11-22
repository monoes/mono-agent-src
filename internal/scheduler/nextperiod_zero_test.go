package scheduler

import (
	"testing"
	"time"
)

func TestNextPeriod_NonPositiveIntervalReturnsNil(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	end := time.Now().Add(time.Hour)
	for _, n := range []int{0, -5} {
		if got := NextPeriod(start, end, n); got != nil {
			t.Fatalf("interval %d = %v, want nil", n, got)
		}
	}
}
