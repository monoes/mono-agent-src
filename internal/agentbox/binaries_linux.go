//go:build linux

package agentbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
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

// packageManifest are the top-level entries of a verified package that are bound, besides the
// ones its package.json names as main or bin. Everything else in the package directory (its
// configuration, sources, data) stays out of the box.
var packageManifest = []string{"package.json", "bin", "dist", "lib", "build", "node_modules"}

// bindBinary returns the read-only binds that make one executable runnable in a strict box.
// Everything is pinned first and judged on the pinned object:
//   - the executable must not be, or be under, a credential, state or startup location;
//   - the executable must be a regular file (bound as the file it really is; a symlink to it
//     stays a symlink, so a script that finds its package through $0 still can);
//   - for a script inside a package under a hidden location, the package directory must be a
//     verified runtime package (verifiedPackage), and only its manifest entries are bound.
//
// Never the parent directory or install prefix.
func (b Box) bindBinary(bd *binder, home string, forbidden []string, found string, seen map[string]bool, plan *Plan) ([]string, error) {
	var out []string
	real0, err := filepath.EvalSymlinks(found)
	if err != nil {
		plan.Notes = append(plan.Notes, found+" cannot be resolved: not bound into the box")
		return nil, nil
	}
	if !underScratch(home, real0) && !underScratch(home, found) {
		return nil, nil // visible through the read-only root anyway
	}
	if underScratch(home, real0) {
		real, idx, err := bd.pin(real0, wantFile)
		if err != nil {
			return nil, err
		}
		if err := checkAllowed(home, forbidden, real); err != nil {
			return nil, err // never bind a credential, state or startup file back as "the binary"
		}
		if !seen[real] {
			seen[real] = true
			out = append(out, fdBind("--ro-bind", idx, real)...)
		}
		real0 = real
	}
	if real0 != found && underScratch(home, found) && !seen[found] {
		seen[found] = true
		out = append(out, "--symlink", real0, found)
	}
	for d, i := filepath.Dir(real0), 0; i < 6 && d != home && d != "/" && d != "."; d, i = filepath.Dir(d), i+1 {
		if _, err := os.Lstat(filepath.Join(d, "package.json")); err != nil {
			continue
		}
		if !underScratch(home, d) {
			break
		}
		pkg, err := b.bindPackage(bd, home, forbidden, d, seen, plan)
		if err != nil {
			return nil, err
		}
		return append(out, pkg...), nil
	}
	return out, nil
}

// bindPackage binds the manifest entries of the package directory dir, judged through the
// pinned directory: its package.json must name a verified package, the directory itself must
// not be a protected location, and the bound entries must hold nothing credential-like.
func (b Box) bindPackage(bd *binder, home string, forbidden []string, dir string, seen map[string]bool, plan *Plan) ([]string, error) {
	skip := func(why string) ([]string, error) {
		plan.Notes = append(plan.Notes, "the package directory "+dir+" "+why+": not bound, the tool may not start")
		return nil, nil
	}
	real, idx, err := bd.pin(dir, wantDir)
	if err != nil {
		return skip("cannot be pinned")
	}
	if checkAllowed(home, forbidden, real) != nil {
		return skip("is a protected location")
	}
	names, ok := packageEntryNames(bd.procPath(idx))
	if !ok {
		return skip("is not a verified runtime package")
	}
	var out []string
	var pinned []int
	var pinnedNames []string
	for _, n := range names {
		pr, eidx, err := bd.pinAt(idx, real, n, wantAny)
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, unix.ENOENT) {
				continue
			}
			return skip("holds an entry that is not a plain file or directory (" + n + ")")
		}
		_ = pr
		pinned, pinnedNames = append(pinned, eidx), append(pinnedNames, n)
	}
	for i, eidx := range pinned {
		if pinnedNames[i] == "node_modules" {
			continue // installed third-party code
		}
		if !entryClean(bd.procPath(eidx), real) {
			return skip("holds credential-like files or links out of the directory (" + pinnedNames[i] + ")")
		}
	}
	for i, eidx := range pinned {
		dest := filepath.Join(real, pinnedNames[i])
		if !seen[dest] {
			seen[dest] = true
			out = append(out, fdBind("--ro-bind", eidx, dest)...)
		}
	}
	return out, nil
}
