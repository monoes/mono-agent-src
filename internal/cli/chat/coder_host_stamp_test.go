//go:build linux

package chat

import (
	"os"
	"testing"
)

// The exited-child fallback reads only a zombie: a live process (this one) has no such stamp, so it can never stand in for one.
func TestExitedChildStampIsEmptyForALiveProcess(t *testing.T) {
	if got := exitedChildStamp(os.Getpid()); got != "" {
		t.Errorf("a live process got the exited-child stamp %q", got)
	}
}
