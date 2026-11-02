//go:build linux

package procbind

import (
	"errors"
	"net"
	"os"
)

// DialProfile connects to the binding service of a profile the way a verifier does: the runtime root is derived
// exactly as the host derived it (ResolveRoot over XDG_RUNTIME_DIR, the home and the user id), and nothing is
// created, so a host that never started leaves nothing to connect to. The checks of DialSecure apply; the
// authentication of the service is the keyed answer (Client).
func DialProfile(profile string) (*net.UnixConn, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.New("procbind: no home directory for the runtime root")
	}
	uid := os.Getuid()
	return DialSecure(SocketOptions{Root: ResolveRoot(os.Getenv("XDG_RUNTIME_DIR"), home, uid), Profile: profile, UID: uid})
}
