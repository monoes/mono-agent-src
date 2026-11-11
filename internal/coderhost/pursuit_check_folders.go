package coderhost

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Which folders may be the project of a goal check. The project is the lead's chat folder; a check copies it and runs the
// criteria's programs on the copy, so a folder that holds credentials or the host's own state must never be one.

// ForbiddenFolders are the folders no check may use as the project.
//   - Ancestors: a folder that IS one of them or CONTAINS one is refused (the person's home folder: a project under it is the
//     normal case and is fine; the home folder itself, /home and / are not).
//   - Trees: the folder, anything inside it and anything that contains it (the host's state folder: database, vault, scratch).
//   - Deny: the folder and everything inside it, but not what contains it (credential folders under home, system trees).
//   - Exact: only the folder itself (/tmp and /dev/shm themselves; a project under /tmp/x stays allowed).
//   - Siblings: a folder under any of these (/home, /Users) that is not inside the person's own home is refused (other users' homes).
type ForbiddenFolders struct {
	Ancestors, Trees, Deny, Exact, Siblings []string
	Home                                    string // for Siblings: the person's own home
}

// foldCase is true where paths compare without regard to case (macOS and Windows file systems are case-insensitive by default,
// so ~/.MonoAgent is ~/.monoagent). A variable only so that a test can run the comparison for both.
var foldCase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// pathWithin says whether child is parent or lies under it.
func pathWithin(parent, child string) bool { return pathWithinFold(parent, child, foldCase) }

func pathWithinFold(parent, child string, fold bool) bool {
	if fold {
		parent, child = strings.ToLower(parent), strings.ToLower(child)
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolvedAbs is a clean absolute path with its links resolved when it exists, or "" for a path that is not absolute.
func resolvedAbs(p string) string {
	if p = filepath.Clean(p); !filepath.IsAbs(p) {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// SubjectForbidden says why a resolved project folder may not be a check's subject under f, or "".
func SubjectForbidden(real string, f ForbiddenFolders) string {
	for _, a := range f.Ancestors {
		if a = resolvedAbs(a); a != "" && pathWithin(real, a) { // the folder is a, or contains a (a parent of the home folder)
			return "bad-subject: the project folder is the person's home folder or contains it"
		}
	}
	for _, t := range f.Trees {
		if t = resolvedAbs(t); t != "" && (pathWithin(t, real) || pathWithin(real, t)) {
			return "bad-subject: the project folder is, contains or lies inside the host's own state folder"
		}
	}
	for _, d := range f.Deny {
		if d = resolvedAbs(d); d != "" && pathWithin(d, real) {
			return "bad-subject: the project folder holds credentials or system files (" + d + ")"
		}
	}
	for _, e := range f.Exact {
		if e = resolvedAbs(e); e != "" && pathWithinFold(e, real, foldCase) && pathWithinFold(real, e, foldCase) {
			return "bad-subject: the project folder is " + e + " itself; use a folder inside it"
		}
	}
	if f.Home != "" {
		home := resolvedAbs(f.Home)
		for _, s := range f.Siblings {
			if s = resolvedAbs(s); s != "" && home != "" && pathWithin(s, real) && !pathWithin(home, real) {
				return "bad-subject: the project folder is another user's folder"
			}
		}
	}
	return ""
}

// DefaultForbidden is the forbidden list of a host whose person's home folder is home and whose state is in stateDirs. The real
// state folder (~/.monoagent) is always in it, whatever the database path says. Deny entries that are, or contain, the home folder
// are left out (a person whose home is /root would otherwise forbid all of their own projects).
func DefaultForbidden(home string, stateDirs ...string) ForbiddenFolders {
	f := ForbiddenFolders{Exact: []string{"/tmp", "/dev/shm"}, Siblings: []string{"/home", "/Users"}, Home: home}
	f.Trees = append(f.Trees, stateDirs...)
	if home != "" {
		f.Ancestors = []string{home}
		f.Trees = append(f.Trees, filepath.Join(home, ".monoagent"))
		for _, d := range []string{".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".config", ".claude", ".monomind",
			filepath.Join(".local", "share", "keyrings"), ".password-store", filepath.Join("Library", "Keychains")} {
			f.Deny = append(f.Deny, filepath.Join(home, d))
		}
	}
	for _, d := range []string{"/etc", "/proc", "/sys", "/dev", "/run", "/var/run", "/boot", "/root", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/var/lib"} {
		if home == "" || !pathWithin(d, home) {
			f.Deny = append(f.Deny, d)
		}
	}
	return f
}
