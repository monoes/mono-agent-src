//go:build !linux && !darwin && !windows

package stop

import (
	"errors"
)

// ErrPinUnsupported: the binding is proven on Linux, macOS and Windows only; elsewhere the mode does not start.
var ErrPinUnsupported = errors.New("stop: the pinned database binding is available on Linux, macOS and Windows only; the mode does not start here")

func statID(string) (FileID, error) { return FileID{}, ErrPinUnsupported }

func newPinWatch(string) (pinWatch, error) { return nil, ErrPinUnsupported }

func (*Pinned) checkHolders() error { return ErrPinUnsupported }
