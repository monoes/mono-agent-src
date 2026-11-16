// Package sshhosts builds the SSH host key callback both the ssh node and the
// connection check use. x/crypto 0.59.0 made knownhosts.New skip unparseable
// lines (0.58.0 refused the file), which would turn a damaged @revoked line
// into trust; a pre-scan restores the strict behaviour.
package sshhosts

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

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
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) > maxLines {
		return nil, fmt.Errorf("%s: too many lines (more than %d)", path, maxLines)
	}
	dir, err := os.MkdirTemp("", "knownhosts-scan-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	one := filepath.Join(dir, "line")
	for i, line := range lines {
		if !acceptable(line) || !libraryAccepts(one, line) {
			return nil, fmt.Errorf("%s: line %d is not a valid known_hosts entry", path, i+1)
		}
	}
	return knownhosts.New(path)
}

// maxLines bounds the per-line library check (one small file write each).
const maxLines = 20000

// libraryAccepts asks the library itself whether this line is valid: for a
// file whose only non-comment line is this one, knownhosts.New returns an
// error exactly when that line is invalid.
func libraryAccepts(tmp string, line []byte) bool {
	t := bytes.Trim(line, " \t\r")
	if len(t) == 0 || t[0] == '#' {
		return true
	}
	if err := os.WriteFile(tmp, append(bytes.Clone(line), '\n'), 0o600); err != nil {
		return false
	}
	_, err := knownhosts.New(tmp)
	return err == nil
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
