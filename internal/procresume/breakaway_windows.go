//go:build windows

package procresume

import (
	"errors"

	"golang.org/x/sys/windows"
)

// BreakawayRefused says whether err is CreateProcess refusing CREATE_BREAKAWAY_FROM_JOB: a job the creator is in does
// not allow its processes' children to leave it (no JOB_OBJECT_LIMIT_BREAKAWAY_OK), and the answer is ERROR_ACCESS_DENIED.
// The caller then starts without the flag, the child staying in the creator's job (internal/monomind startDetached,
// internal/doomsday/stop's turn placement).
func BreakawayRefused(err error) bool { return errors.Is(err, windows.ERROR_ACCESS_DENIED) }
