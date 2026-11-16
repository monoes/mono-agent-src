// Package sshhosts builds the SSH host key callback both the ssh node and the
// connection check use. x/crypto 0.59.0 made knownhosts.New skip unparseable
// lines (0.58.0 refused the file), which would turn a damaged @revoked line
// into trust; a pre-scan restores the strict behaviour.
package sshhosts

import (
	"bytes"
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// New refuses the known_hosts file if any non-blank, non-comment line is
// one that knownhosts.New would skip or read differently from ssh.ParseKnownHosts:
// a line that fails to parse, a marker other than exactly "@revoked" or
// "@cert-authority" (the library skips unknown ones, and ParseKnownHosts
// accepts any), a single-token line, or a control character other than tab.
// The error names the file and the line number, never the line.
func New(path string) (ssh.HostKeyCallback, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for i, line := range bytes.Split(data, []byte("\n")) {
		if !acceptable(line) {
			return nil, fmt.Errorf("%s: line %d is not a valid known_hosts entry", path, i+1)
		}
	}
	return knownhosts.New(path)
}

func acceptable(line []byte) bool {
	line = bytes.TrimSuffix(line, []byte("\r"))
	for _, b := range line {
		if (b < 0x20 && b != '\t') || b == 0x7f {
			return false
		}
	}
	line = bytes.Trim(line, " \t")
	if len(line) == 0 || line[0] == '#' {
		return true
	}
	if line[0] == '@' {
		first := line
		if j := bytes.IndexAny(line, " \t"); j >= 0 {
			first = line[:j]
		}
		if string(first) != "@revoked" && string(first) != "@cert-authority" {
			return false
		}
	}
	_, _, _, _, _, err := ssh.ParseKnownHosts(line)
	return err == nil
}
