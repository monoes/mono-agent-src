//go:build !linux

package procbind

import (
	"errors"
	"net"
)

// DialProfile is not available off Linux: there is no binding service, so every v2 verification is a deny.
func DialProfile(profile string) (*net.UnixConn, error) {
	return nil, errors.New("procbind: the binding service is available on Linux only")
}
