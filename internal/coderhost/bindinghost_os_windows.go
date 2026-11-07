//go:build windows

package coderhost

import (
	"errors"
	"os"

	"github.com/monoes/mono-agent/internal/procbind"
)

// defaultSocketOptions places the socket under <home>\.monoagent\run.
func defaultSocketOptions(profile string) (procbind.SocketOptions, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return procbind.SocketOptions{}, errors.New("the user's home folder is unknown")
	}
	return procbind.SocketOptions{Root: procbind.ResolveRoot("", home, 0), Profile: profile, CreateRoot: true}, nil
}

// newBindingServer is the service for one profile: it serves only the user this process runs as.
func newBindingServer(profile, generation string, key []byte, leases procbind.LeaseRegistry, procs procbind.Procs, logf func(string, ...any)) (*procbind.Server, error) {
	sid, err := procbind.OwnSID()
	if err != nil {
		return nil, errors.New("the binding service cannot read its own user")
	}
	return &procbind.Server{Profile: profile, Generation: generation, Key: key, Leases: leases, Procs: procs, SID: sid, Logf: logf}, nil
}
