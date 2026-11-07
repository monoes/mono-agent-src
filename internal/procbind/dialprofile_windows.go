//go:build windows

package procbind

import (
	"errors"
	"net"
	"os"
)

// DialProfile connects to the binding service of a profile the way a verifier does: the runtime root is derived
// exactly as the host derived it, and nothing is created, so a host that never started leaves nothing to connect to.
// The checks of DialSecure apply; the authentication of the service is the keyed answer (Client).
func DialProfile(profile string) (*net.UnixConn, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.New("procbind: no home directory for the runtime root")
	}
	return DialSecure(SocketOptions{Root: ResolveRoot("", home, 0), Profile: profile})
}
