package httpnodes

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/monoes/mono-agent/internal/sshhosts"
)

// tofuMu serialises the read-check-append of the trust-on-first-use file across the nodes of
// one process.
var tofuMu sync.Mutex

// tofuKnownHostsPath is the file the ssh nodes remember first-seen host keys in when no
// known_hosts is configured.
func tofuKnownHostsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home folder to keep known_hosts in: %w", err)
	}
	return filepath.Join(home, ".monoagent", "known_hosts"), nil
}

// tofuHostKeyCallback trusts a host key the first time it is seen, records it in path, and from
// then on accepts only that key for that host (as ssh does with StrictHostKeyChecking=accept-new).
// A changed key is refused, as is a revoked one or a damaged file.
func tofuHostKeyCallback(path string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		tofuMu.Lock()
		defer tofuMu.Unlock()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		check, err := sshhosts.New(path)
		if err != nil {
			return fmt.Errorf("known_hosts: %w", err)
		}
		err = check(hostname, remote, key)
		var ke *knownhosts.KeyError
		if err == nil || !errors.As(err, &ke) || len(ke.Want) > 0 {
			return err // known and matching, or a changed key (refused)
		}
		line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
		_, err = f.WriteString(line + "\n")
		return err
	}
}
