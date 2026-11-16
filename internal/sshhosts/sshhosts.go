// Package sshhosts builds the SSH host key callback both the ssh node and the
// connection check use. x/crypto 0.59.0 made knownhosts.New skip unparseable
// lines (0.58.0 refused the file), which would turn a damaged @revoked line
// into trust; a pre-scan restores the strict behaviour.
package sshhosts

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// New refuses the known_hosts file if any non-blank, non-comment line fails to
// parse, then returns the knownhosts callback for it. The error names the file
// and the line number, never the line.
func New(path string) (ssh.HostKeyCallback, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for i, line := range bytes.Split(data, []byte("\n")) {
		if _, _, _, _, _, err := ssh.ParseKnownHosts(line); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: line %d is not a valid known_hosts entry", path, i+1)
		}
	}
	return knownhosts.New(path)
}
