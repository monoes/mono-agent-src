//go:build !linux && !darwin && !windows

package stop

import (
	"errors"
	"net"
)

// peerUID has no implementation off Linux: the mode refuses to start there (Probe), so the emergency channel
// never accepts a peer.
func peerUID(net.Conn) (uint32, error) {
	return 0, errors.New("stop: peer credentials are only implemented on Linux, macOS and Windows")
}
