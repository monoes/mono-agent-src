//go:build !linux

package procbind

import "errors"

// Ready is never nil here: the binding service needs Linux.
func Ready() error {
	return errors.New("the binding service needs Linux")
}
