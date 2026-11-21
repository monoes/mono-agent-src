//go:build darwin

package machine

import (
	"context"
	"testing"
	"time"
)

// Regression: macOS used to answer ErrUnsupported, pinning every run to the safe minimum ("sample-failed").
func TestRealDarwinSamplerIsValid(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := New(t.TempDir(), "/").Sample(ctx)
	if err != nil || !s.Valid || s.Cores < 1 || s.MemAvailFrac <= 0 || s.MemAvailFrac > 1 || s.DiskFreeFrac <= 0 || s.DiskFreeFrac > 1 {
		t.Fatalf("%+v %v", s, err)
	}
}
