package finish

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The subject of a finish (note 13.6 section 4): a sealed snapshot of one root, named by a tree digest. The digest covers
// content and structure (relative path, kind, the executable bit, content digest or link target), never times or owners.
// On Windows there is no executable bit (NTFS keeps no unix mode), so there it is not part of the digest: the digest covers
// content and structure only.
// Links are recorded and never followed; hard links, devices, sockets and pipes refuse the snapshot. A directory named
// ".git" is not part of the subject.

// Limits of a snapshot; a tree beyond them is refused, not truncated (note 13.6 section 14.7).
const (
	MaxSnapshotFiles = 100000
	MaxSnapshotBytes = 512 << 20
)

// ErrSnapshotRefused: the root cannot be made into a subject; the reason is in the message.
var ErrSnapshotRefused = errors.New("finish: the subject cannot be snapshotted")

// Entry is one line of the tree digest.
type Entry struct {
	Path string // slash separated, relative; "." is not an entry
	Kind byte   // 'f' file, 'd' directory, 'l' link
	Exec bool
	Sum  string // sha256 hex of the content, or the link target for a link, "" for a directory
	Size int64
}

// Tree is the entries of a root, sorted by path, with its digest.
type Tree struct {
	Entries []Entry
	Digest  string
	// Flags names the entries that are links leaving the root: recorded, never read.
	Flags []string
}

func (t Tree) line(e Entry) string {
	x := ""
	if e.Exec {
		x = "x"
	}
	return fmt.Sprintf("%s\x00%c%s\x00%s", e.Path, e.Kind, x, e.Sum)
}

func (t *Tree) seal() {
	sort.Slice(t.Entries, func(i, j int) bool { return t.Entries[i].Path < t.Entries[j].Path })
	h := sha256.New()
	h.Write([]byte("monoagent-finish-tree-v1\n"))
	for _, e := range t.Entries {
		io.WriteString(h, t.line(e)+"\n")
	}
	t.Digest = hex.EncodeToString(h.Sum(nil))
}

func refuse(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrSnapshotRefused, fmt.Sprintf(f, a...))
}

// DigestTree reads root (it is never modified) and returns its tree. When copyTo is not empty, every regular file is
// also copied to the same relative path under copyTo (which must exist), so the digest is of the bytes that were copied.
func DigestTree(root, copyTo string) (Tree, error) {
	var t Tree
	var files int
	var bytes int64
	st, err := os.Lstat(root)
	if err != nil || !st.IsDir() {
		return t, refuse("%s is not a directory", root)
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return refuse("%v", err)
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return refuse("%v", err)
		}
		if files++; files > MaxSnapshotFiles {
			return refuse("more than %d entries", MaxSnapshotFiles)
		}
		e := Entry{Path: rel}
		switch m := info.Mode(); {
		case m.IsDir():
			e.Kind = 'd'
			if copyTo != "" {
				if err := os.Mkdir(filepath.Join(copyTo, rel), 0o700); err != nil {
					return refuse("%v", err)
				}
			}
		case m&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return refuse("%v", err)
			}
			e.Kind, e.Sum = 'l', target
			if filepath.IsAbs(target) || strings.HasPrefix(filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(rel), target))), "../") {
				t.Flags = append(t.Flags, rel+" -> "+target)
			}
			if copyTo != "" {
				if err := os.Symlink(target, filepath.Join(copyTo, rel)); err != nil {
					return refuse("%v", err)
				}
			}
		case m.IsRegular():
			if hardLinked(p, info) {
				return refuse("%s is a hard link", rel)
			}
			if bytes += info.Size(); bytes > MaxSnapshotBytes {
				return refuse("more than %d bytes", int64(MaxSnapshotBytes))
			}
			e.Kind, e.Exec, e.Size = 'f', m&0o111 != 0, info.Size()
			sum, err := sumFile(p, copyTo, rel, e.Exec, info)
			if err != nil {
				return refuse("%s: %v", rel, err)
			}
			e.Sum = sum
		default:
			return refuse("%s is a device, socket or pipe", rel)
		}
		t.Entries = append(t.Entries, e)
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrSnapshotRefused) {
			return Tree{}, err
		}
		return Tree{}, refuse("%v", err)
	}
	t.seal()
	return t, nil
}

// sumFile hashes (and, with copyTo, copies) the regular file src that the walk saw as info. It opens the file itself without
// following a link or waiting on a pipe, and checks that what it opened is that same regular file: a file swapped for a link,
// a pipe or another file after the walk looked at it is refused, not read.
func sumFile(src, copyTo, rel string, exec bool, info fs.FileInfo) (string, error) {
	f, err := openRegular(src, info)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	var w io.Writer = h
	if copyTo != "" {
		mode := os.FileMode(0o400)
		if exec {
			mode = 0o500
		}
		out, err := os.OpenFile(filepath.Join(copyTo, rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return "", err
		}
		defer func() { out.Close(); os.Chmod(out.Name(), mode) }()
		w = io.MultiWriter(h, out)
	}
	if _, err := io.Copy(w, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Snapshot copies root into a new directory dst (created 0700, parent must exist) and returns the tree digest of what was
// copied. Files are read-only, directories are made read-only last. A refused root leaves nothing behind.
func Snapshot(root, dst string) (Tree, error) {
	if err := os.Mkdir(dst, 0o700); err != nil {
		return Tree{}, refuse("%v", err)
	}
	t, err := DigestTree(root, dst)
	if err != nil {
		RemoveTree(dst)
		return Tree{}, err
	}
	SealDirs(dst, t)
	return t, nil
}

// SealDirs makes the directories of a copied tree read-only, deepest first.
// It seals by mode bits only: on Windows (NTFS) that makes the FILES read-only but a directory stays writable, so a
// criterion could add files to its copy there (stated in the Windows entry text).
func SealDirs(dst string, t Tree) {
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if e := t.Entries[i]; e.Kind == 'd' {
			os.Chmod(filepath.Join(dst, filepath.FromSlash(e.Path)), 0o500)
		}
	}
	os.Chmod(dst, 0o500)
}

// CopyTree makes a fresh sealed copy of an already sealed snapshot (the per-criterion working copy) and checks that the
// copy has the digest of the source.
func CopyTree(src, dst string, want Tree) error {
	got, err := Snapshot(src, dst)
	if err != nil {
		return err
	}
	if got.Digest != want.Digest {
		RemoveTree(dst)
		return refuse("the copy digests differently from the snapshot")
	}
	return nil
}

// Diff lists up to max paths whose entries differ between two trees.
func Diff(a, b Tree, max int) []string {
	am := map[string]string{}
	for _, e := range a.Entries {
		am[e.Path] = a.line(e)
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range b.Entries {
		seen[e.Path] = true
		if am[e.Path] != b.line(e) {
			out = append(out, e.Path)
		}
	}
	for p := range am {
		if !seen[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// RemoveTree deletes a tree the harness made, file by file in a bounded walk (no recursive delete primitive): it makes
// directories writable on the way down and removes entries deepest first. Links are removed, never followed.
func RemoveTree(root string) error {
	var all []string
	var dirs int
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			os.Chmod(p, 0o700)
			dirs++
		}
		if len(all) < 4*MaxSnapshotFiles {
			all = append(all, p)
		}
		return nil
	})
	var first error
	for i := len(all) - 1; i >= 0; i-- {
		if err := os.Remove(all[i]); err != nil && !errors.Is(err, fs.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}
