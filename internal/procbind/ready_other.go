//go:build !linux && !darwin && !darwin

package procbind

import "errors"

// Ready is never nil here: the binding service needs Linux or macOS.
func Ready() error {
	return errors.New("the binding service needs Linux or macOS")
}

// PeerPidfdSupported is false off Linux: the kernel facility the binding service needs does not exist there.
func PeerPidfdSupported() bool { return false }
