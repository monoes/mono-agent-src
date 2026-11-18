package stop

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/inheritenv"
)

// passInherited is how a command created under the anchor keeps the handles it passes on to its child (#1723). A
// child created with another process as its parent inherits handles of THAT process: Go duplicates the standard
// handles into the parent itself but passes SysProcAttr.AdditionalInheritedHandles as they are, so a handle of this
// process does not exist there and CreateProcess refuses the whole list ("The parameter is incorrect"). Each handle is
// duplicated into the anchor (dup), the returned list carries the duplicates in the same order, and the variable that
// announces a handle to the child (inheritenv.Names) is rewritten to its duplicate's number.
//
// The contract is one to one and fails closed, checked before anything is duplicated: every handle is announced by
// exactly one of the named variables, every named variable present announces a handle of the list, and no name is
// set twice (Windows names ignore case). On a failed duplicate the ones already made are closed (closeDup). The
// returned slices are new: the caller's list and environment are never written to. A command that passes no handle
// is returned as it is.
func passInherited(handles []uintptr, env []string, dup func(uintptr) (uintptr, error), closeDup func(uintptr)) (dups []uintptr, out []string, err error) {
	if len(handles) == 0 {
		return nil, env, nil
	}
	at := make([]int, len(handles)) // the env entry that announces each handle
	for i := range at {
		at[i] = -1
	}
	for _, name := range inheritenv.Names() {
		idx := -1
		for i, e := range env {
			if k, _, _ := strings.Cut(e, "="); strings.EqualFold(k, name) {
				if idx >= 0 {
					return nil, nil, fmt.Errorf("%s is set twice in the command's environment", name)
				}
				idx = i
			}
		}
		if idx < 0 {
			continue
		}
		_, v, _ := strings.Cut(env[idx], "=")
		n, perr := strconv.ParseUint(v, 10, 64)
		h := slices.Index(handles, uintptr(n))
		if perr != nil || h < 0 {
			return nil, nil, fmt.Errorf("%s does not name a handle the command passes on", name)
		}
		if at[h] >= 0 {
			return nil, nil, errors.New("one inherited handle is announced twice")
		}
		at[h] = idx
	}
	if slices.Contains(at, -1) {
		return nil, nil, errors.New("the command passes on a handle that no variable announces to the child")
	}
	out = slices.Clone(env)
	for i, h := range handles {
		d, err := dup(h)
		if err != nil {
			for _, d := range dups {
				closeDup(d)
			}
			return nil, nil, fmt.Errorf("an inherited handle cannot be passed through the anchor: %w", err)
		}
		dups = append(dups, d)
		k, _, _ := strings.Cut(env[at[i]], "=")
		out[at[i]] = k + "=" + strconv.FormatUint(uint64(d), 10)
	}
	return dups, out, nil
}

// announcesInherited says whether env announces an inherited handle (any of inheritenv.Names, case ignored).
func announcesInherited(env []string) bool {
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		for _, name := range inheritenv.Names() {
			if strings.EqualFold(k, name) {
				return true
			}
		}
	}
	return false
}
