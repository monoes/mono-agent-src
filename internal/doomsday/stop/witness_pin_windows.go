//go:build windows

package stop

import "errors"

// ErrPinUnsupported: placeholder until the Windows binding lands (next commits); the mode does not start here.
var ErrPinUnsupported = errors.New("stop: the pinned database binding is not available on Windows yet; the mode does not start here")

func statID(string) (FileID, error) { return FileID{}, ErrPinUnsupported }

func newPinWatch(string) (pinWatch, error) { return nil, ErrPinUnsupported }

func (*Pinned) checkHolders() error { return ErrPinUnsupported }
