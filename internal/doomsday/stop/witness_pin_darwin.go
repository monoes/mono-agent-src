//go:build darwin

package stop

import "errors"

// ErrPinUnsupported: placeholder until the macOS binding lands (next commits); the mode does not start here.
var ErrPinUnsupported = errors.New("stop: the pinned database binding is not available on macOS yet; the mode does not start here")

func newPinWatch(string) (pinWatch, error) { return nil, ErrPinUnsupported }

func scanFDs() ([]fdEntry, error) { return nil, ErrPinUnsupported }
