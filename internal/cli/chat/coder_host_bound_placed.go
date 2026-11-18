package chat

import (
	"errors"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// placedPipeKept checks the frame pipe of a bound turn after its placement ran (Windows, where the pipe is a handle in
// the process attributes the placement may touch). The placement is trusted host code, not a boundary: the check keeps
// a placement bug from starting a turn without its pipe, with a handle the launcher did not give it, or with an
// announcement (CredFDEnv) of another number. It accepts the launcher's own list (the one handle, own, announced) and one
// documented substitution: a placement that creates the child under another process (the open run's anchor,
// internal/doomsday/stop passInherited) passes a duplicate of the pipe made in that process, so the entry is that
// process's number, CredFDEnv carries it, and same says the entry, in that parent, is the pipe the launcher made. Under
// another parent the entry is checked even when its number is unchanged: there it would name whatever that process
// holds under the number. Anything else - a dropped or added handle, a missing or doubled announcement, another number,
// a changed entry without another parent - is refused.
func placedPipeKept(own uintptr, list []uintptr, reparented bool, env []string, same func(uintptr) bool) error {
	if len(list) != 1 {
		return errors.New("the placement dropped or added an inherited handle")
	}
	announced, n := "", 0
	for _, e := range env {
		if k, v, _ := strings.Cut(e, "="); strings.EqualFold(k, coderhost.CredFDEnv) {
			announced, n = v, n+1
		}
	}
	if n != 1 || announced != strconv.FormatUint(uint64(list[0]), 10) {
		return errors.New("the child's announcement of the frame pipe does not name the handle it inherits")
	}
	switch {
	case reparented && !same(list[0]):
		return errors.New("the handle passed through the new parent is not the frame pipe")
	case !reparented && list[0] != own:
		return errors.New("the placement replaced the frame pipe's handle")
	}
	return nil
}
