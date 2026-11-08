//go:build linux || darwin

package coderhost

import (
	"errors"
	"os"

	"github.com/monoes/mono-agent/internal/procbind"
)

// defaultSocketOptions places the socket under XDG_RUNTIME_DIR when it is usable, else under the home folder.
func defaultSocketOptions(profile string) (procbind.SocketOptions, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return procbind.SocketOptions{}, errors.New("the user's home folder is unknown")
	}
	xdg := os.Getenv("XDG_RUNTIME_DIR")
	root := procbind.ResolveRoot(xdg, home, os.Getuid())
	return procbind.SocketOptions{Root: root, Profile: profile, CreateRoot: root != xdg}, nil
}

// newBindingServer is the service for one profile: it serves only this process's own uid.
func newBindingServer(profile, generation string, key []byte, leases procbind.LeaseRegistry, procs procbind.Procs, logf func(string, ...any)) (*procbind.Server, error) {
	return &procbind.Server{Profile: profile, Generation: generation, Key: key, Leases: leases, Procs: procs, UID: os.Getuid(), Logf: logf}, nil
}
