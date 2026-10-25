//go:build linux

package agentbox

import (
	"os"
	"path/filepath"
	"strings"
)

func lookPathIn(pathVar, name string) string {
	if strings.Contains(name, "/") {
		return name
	}
	for _, d := range filepath.SplitList(pathVar) {
		if d == "" {
			continue
		}
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// underScratch reports whether p lies where the strict profile hides what the host has:
// the home directory, /tmp, /var/tmp or /run.
func underScratch(home, p string) bool {
	for _, d := range []string{home, "/tmp", "/var/tmp", "/run"} {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// binaryBinds are the read-only binds that make one executable runnable in a strict box:
// the file as found on PATH and the file it really is, and, for a script in a package, the
// package root (a directory with a package.json) when nothing sensitive sits in it. Never
// the parent directory or install prefix: what lies beside a binary is not needed to run it.
func (b Box) binaryBinds(home string, forbidden []string, found string, plan *Plan) []string {
	var binds []string
	add := func(p string) {
		if p != "" && exists(p) && underScratch(home, p) {
			binds = append(binds, p)
		}
	}
	real, err := filepath.EvalSymlinks(found)
	if err != nil {
		add(found)
		return binds
	}
	if real != found {
		// A symlink stays one, so a script that finds its package through $0 still can.
		if underScratch(home, found) {
			binds = append(binds, "symlink:"+real+"\x00"+found)
		}
	} else {
		add(found)
	}
	add(real)
	for d, i := filepath.Dir(real), 0; i < 6 && d != home && d != "/" && d != "."; d, i = filepath.Dir(d), i+1 {
		if !exists(filepath.Join(d, "package.json")) {
			continue
		}
		switch {
		case !underScratch(home, d):
		case checkAllowed(home, forbidden, d) != nil || !verifiedPackage(d):
			plan.Notes = append(plan.Notes, "the package directory "+d+" is not a verified runtime package or holds credential-like entries: not bound, "+filepath.Base(found)+" may not start")
		default:
			add(d)
		}
		break
	}
	return binds
}
