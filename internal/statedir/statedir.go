// Package statedir holds the permission rules for monoagent's state folder
// (~/.monoagent). It keeps the user's people, messages, chat history and
// session state, so the folder and the files in it are readable by the user
// alone: folders 0700, files 0600.
package statedir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

const (
	// DirPerm is the mode of a folder under ~/.monoagent.
	DirPerm fs.FileMode = 0o700
	// FilePerm is the mode of a file under ~/.monoagent.
	FilePerm fs.FileMode = 0o600
)

// othersBits are the group and world permission bits.
const othersBits fs.FileMode = 0o077

// MkdirAll creates path and any missing parents as private folders. Folders
// that already exist keep their mode.
func MkdirAll(path string) error {
	return os.MkdirAll(path, DirPerm)
}

// WriteFile writes data to path so only the owner can read it, creating the
// parent folder privately. A file that already exists is tightened to
// FilePerm too, since os.WriteFile keeps an existing file's mode.
func WriteFile(path string, data []byte) error {
	if err := MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	// The mode is fixed before the content goes in: writing first and chmodding after left the new
	// content in a group- or world-readable file for a moment.
	if err := os.Chmod(path, FilePerm); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(path, data, FilePerm); err != nil {
		return err
	}
	return os.Chmod(path, FilePerm)
}

// Tighten removes the group and world permission bits from path, keeping the
// owner's. It is a no-op on Windows, where Unix modes do not apply.
func Tighten(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&othersBits == 0 {
		return nil
	}
	return os.Chmod(path, info.Mode().Perm()&^othersBits)
}

// Exposed is a path other users on the machine can read or enter.
type Exposed struct {
	Path string
	Mode fs.FileMode
}

// PrivateSubdirs are the folders under the state folder that hold the
// user's own data (workflow definitions, page captures, recordings and
// their drafts, logs, crash reports, database backups). FindExposed walks
// them recursively. Other folders are left out on purpose: the automation
// registry (automations/) and the node install trees (node runtimes,
// npm packages, native libraries) hold installed programs rather than the
// user's data, can run to tens of thousands of files, and are protected by
// the 0700 state folder around them anyway.
var PrivateSubdirs = []string{"workflows", "captures", "recordings", "recording-drafts", "logs", "crashes", "backups"}

// maxWalk bounds how many entries FindExposed visits in each private
// subfolder, so a huge capture folder cannot stall doctor (a var so tests
// can lower it).
var maxWalk = 50000

// errWalkLimit stops a walk that reached maxWalk.
var errWalkLimit = errors.New("walk limit reached")

// FindExposed lists dir, the files directly inside it, everything inside
// the PrivateSubdirs of dir (at most maxWalk entries each), and the extra
// paths (such as a database outside dir) that carry group or world
// permission bits. Symlinks and missing paths are skipped. It returns
// nothing on Windows.
func FindExposed(dir string, extra ...string) ([]Exposed, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	var out []Exposed
	seen := map[string]bool{}
	add := func(p string, info fs.FileInfo) {
		p = filepath.Clean(p)
		if seen[p] {
			return
		}
		seen[p] = true
		if info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&othersBits == 0 {
			return
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return
		}
		out = append(out, Exposed{Path: p, Mode: info.Mode()})
	}
	info, err := os.Lstat(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		add(dir, info)
		if info.IsDir() {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if !e.Type().IsRegular() {
					continue
				}
				if fi, err := e.Info(); err == nil {
					add(filepath.Join(dir, e.Name()), fi)
				}
			}
			for _, sub := range PrivateSubdirs {
				if err := walkPrivate(filepath.Join(dir, sub), add); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, p := range extra {
		if p == "" {
			continue
		}
		if fi, err := os.Lstat(p); err == nil {
			add(p, fi)
		}
	}
	return out, nil
}

// walkPrivate passes root and everything below it to add, without
// following symlinks, stopping quietly after maxWalk entries. A missing
// root, or an entry that vanishes mid-walk, is not an error.
func walkPrivate(root string, add func(string, fs.FileInfo)) error {
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
				return nil // gone meanwhile, or a folder we cannot list
			}
			return err
		}
		if n++; n > maxWalk {
			return errWalkLimit
		}
		if fi, err := d.Info(); err == nil {
			add(p, fi)
		}
		return nil
	})
	if errors.Is(err, errWalkLimit) {
		return nil
	}
	return err
}

// Repair removes the group and world bits from everything FindExposed
// reports for the same arguments, and returns what it changed.
func Repair(dir string, extra ...string) ([]Exposed, error) {
	found, err := FindExposed(dir, extra...)
	if err != nil {
		return nil, err
	}
	var errs []error
	var fixed []Exposed
	for _, e := range found {
		if err := Tighten(e.Path); err != nil {
			errs = append(errs, err)
			continue
		}
		fixed = append(fixed, e)
	}
	return fixed, errors.Join(errs...)
}

// DBFiles returns the SQLite database at dbPath with its -wal and -shm
// companions ("" yields nothing).
func DBFiles(dbPath string) []string {
	if dbPath == "" {
		return nil
	}
	return []string{dbPath, dbPath + "-wal", dbPath + "-shm"}
}
