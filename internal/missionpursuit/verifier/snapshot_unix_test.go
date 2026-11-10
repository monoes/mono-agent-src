//go:build !windows

package verifier

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestASubjectThatCannotBeSnapshottedCannotBeChecked(t *testing.T) {
	r := newRig(t, true)
	if err := syscall.Mkfifo(filepath.Join(r.root, "pipe"), 0o600); err != nil {
		t.Skip("no fifo here")
	}
	got := r.run(fileExists("c1"))
	if got.Outcome != OutcomeCannotCheck || got.Reason != ReasonSnapshotRefused || len(scratchEntries(t, r.base)) != 0 {
		t.Fatalf("%s", dump(got))
	}
}
