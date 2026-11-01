//go:build !linux

package procbind

import "errors"

// Ready is never nil here: the binding service needs Linux.
func Ready() error {
	return errors.New("the binding service needs Linux")
}

// PeerPidfdSupported is false off Linux: the kernel facility the binding service needs does not exist there.
func PeerPidfdSupported() bool { return false }
