//go:build windows

package verifier

import (
	"errors"
	"os/exec"
)

// There is no process group containment on Windows, and the finish sandbox does not run programs there: a check says
// cannot-check (no-containment) before it gets here.
const platformOK = false

type GroupPlace struct{}

func (*GroupPlace) Place(*exec.Cmd) (func(), error) {
	return nil, errors.New("no process-group containment on this platform")
}

func (*GroupPlace) Close() {}
