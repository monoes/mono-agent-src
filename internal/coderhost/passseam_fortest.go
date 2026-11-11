package coderhost

import (
	"context"
	"testing"
)

// PursuitPassForTest runs one pursuit pass now, on the caller's goroutine, so a test in another package (the CLI's
// end-to-end tests, which cannot be in this package: the CLI imports it) drives the loop without a ticker or a sleep. It
// panics outside a test binary.
func (h *Host) PursuitPassForTest(t testing.TB) {
	t.Helper()
	if !testing.Testing() {
		panic("coderhost.PursuitPassForTest is a test seam and may only be used from a test binary")
	}
	h.pursuitPass(context.Background())
}
