//go:build windows

package procbind

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
)

// Section 8 of the design: where the service socket lives and how the directory is made and
// opened. These checks stop accidents and shrink the attack surface; they are not the
// authentication. A same-user process can still replace a socket or a directory, which is why the
// service is authenticated by the keyed answer and a hostile listener only ever causes a deny.

const (
	runDirName  = "monoagent"
	sockName    = "bind.sock"
	lockName    = "bind.lock"
	maxLeafName = 64
)

// ErrAnotherHost: the socket directory's lock is held, so a host already serves this profile.
var ErrAnotherHost = errors.New("procbind: another host holds this profile's socket directory")

var errUnsafeDir = errors.New("procbind: unsafe socket directory")

var leafRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// socketLeaf turns a profile id into one safe path component: the id itself when it is plain and
// short, else "~" and the hex of its SHA-256 (a plain id can never start with "~", so the two
// cannot collide).
func socketLeaf(profile string) string {
	if len(profile) <= maxLeafName && leafRe.MatchString(profile) {
		return profile
	}
	h := sha256.Sum256([]byte(profile))
	return "~" + hex.EncodeToString(h[:16])
}
