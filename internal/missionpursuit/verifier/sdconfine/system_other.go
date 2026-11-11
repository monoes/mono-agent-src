//go:build !linux

package sdconfine

import (
	"errors"
	"runtime"
)

// There is no systemd here: newWith refuses on the platform before anything else is asked.
func defaultSystem() system {
	no := errors.New("no systemd on this platform")
	return system{goos: runtime.GOOS, trusted: func(string) (string, error) { return "", no }}
}
