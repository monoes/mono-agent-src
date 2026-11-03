package update

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"time"
)

// installBinary atomically replaces target with data. The temp file is
// created next to target, not in os.TempDir: /tmp is often a separate
// filesystem (tmpfs), and a rename across filesystems fails with
// "invalid cross-device link".
func installBinary(data []byte, target string) error { return installBinaryChecked(data, target, nil) }

// installBinaryChecked is installBinary that first runs check on the new file, next to the target and executable, and
// installs it only if check passes: a new binary that cannot run never takes the old one's place.
func installBinaryChecked(data []byte, target string, check func(tmp string) error) error {
	r, err := installBinaryKeep(data, target, check)
	if err != nil {
		return err
	}
	r.Commit()
	return nil
}

// replaced is a binary that was swapped in, with the one it replaced kept under a name of its own (never a file of the
// user's) until the caller has seen the new one work: Commit then drops it, Restore puts it back.
type replaced struct {
	target, bak string
	done        bool // settled: committed, put back, or deliberately kept
}

// Commit forgets the old binary.
func (r *replaced) Commit() { _ = os.Remove(r.bak); r.done = true }

// Restore puts the old binary back in the new one's place (one rename, so the target is never missing).
func (r *replaced) Restore() error {
	if err := os.Rename(r.bak, r.target); err != nil {
		return fmt.Errorf("restoring %s from %s: %w", r.target, r.bak, err)
	}
	return nil
}

// installBinaryKeep is installBinaryChecked that keeps the old binary until Commit or Restore is called.
func installBinaryKeep(data []byte, target string, check func(tmp string) error) (*replaced, error) {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".monoagentcli-update-*")
	if err != nil {
		return nil, fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("write download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("write download: %w", err)
	}
	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("chmod: %w", err)
	}
	if check != nil {
		if err := check(tmpPath); err != nil {
			os.Remove(tmpPath)
			return nil, fmt.Errorf("the new binary did not pass its check: %w", err)
		}
	}

	sweepPrevious(filepath.Dir(target))
	// the old binary moves to a name nobody else uses: a file the user keeps as <target>.bak is not ours to delete
	// (the name carries the time it was made: a hard link keeps the old file's own modification time, which says nothing
	// about when the backup was kept)
	bakFile, err := os.CreateTemp(filepath.Dir(target), fmt.Sprintf("%s%d-*", previousPrefix, time.Now().Unix()))
	if err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("backup: %w", err)
	}
	bak := bakFile.Name()
	bakFile.Close()
	if runtime.GOOS == "windows" {
		// a running exe can be renamed but not replaced: the old one moves aside first
		if err := os.Rename(target, bak); err != nil {
			os.Remove(tmpPath)
			os.Remove(bak)
			return nil, fmt.Errorf("backup: %w", err)
		}
		if err := os.Rename(tmpPath, target); err != nil {
			os.Rename(bak, target) // rollback
			os.Remove(tmpPath)
			return nil, fmt.Errorf("install: %w", err)
		}
		return &replaced{target: target, bak: bak}, nil
	}
	// The target is never missing: the old file is kept by a hard link (a copy where the file system has none), then one
	// rename puts the new file in its place, atomically, so a program starting meanwhile gets the old or the new one whole,
	// and a crash leaves one of them in place.
	os.Remove(bak)
	if err := linkFile(target, bak); err != nil {
		if err := copyFileMode(target, bak); err != nil {
			os.Remove(tmpPath)
			os.Remove(bak)
			return nil, fmt.Errorf("backup: %w", err)
		}
	}
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		os.Remove(bak)
		return nil, fmt.Errorf("install: %w", err)
	}
	return &replaced{target: target, bak: bak}, nil
}

// previousMaxAge is how long a kept previous binary is trusted to still be wanted. One left by an update that was killed
// (or that kept it for the operator) is removed by the next update that works in the same folder.
var previousMaxAge = 24 * time.Hour

// previousName is exactly the shape of a kept previous binary's name: the prefix, ten digits of unix time, a dash and the
// digits os.CreateTemp adds. A user's own file with a similar name is not matched.
var previousName = regexp.MustCompile(`^\.monoagentcli-previous-([0-9]{10})-[0-9]+$`)

// previousPrefix starts the name of a kept previous binary: previousPrefix + unix seconds it was made + "-" + random.
const previousPrefix = ".monoagentcli-previous-"

// sweepPrevious removes the previous binaries (regular files of this user named previousPrefix<seconds>-<random>) that were
// made more than previousMaxAge ago, by the time in their name: nothing else, nothing newer, nothing whose name does not
// carry a time.
func sweepPrevious(dir string) {
	matches, _ := filepath.Glob(filepath.Join(dir, previousPrefix+"*"))
	for _, m := range matches {
		sm := previousName.FindStringSubmatch(filepath.Base(m))
		if sm == nil {
			continue // not a name this code makes
		}
		secs, err := strconv.ParseInt(sm[1], 10, 64)
		if err != nil {
			continue
		}
		made := time.Unix(secs, 0)
		// a time from the future, or before this code existed, is not one it wrote
		if made.After(time.Now().Add(time.Hour)) || secs < 1_700_000_000 || time.Since(made) < previousMaxAge {
			continue
		}
		if fi, err := os.Lstat(m); err != nil || !fi.Mode().IsRegular() || !ownedByUser(fi) {
			continue
		}
		_ = os.Remove(m)
	}
}

// linkFile makes a hard link; a variable so a test can stand in for a file system that has none.
var linkFile = os.Link

// copyFileMode copies src to dst, a file that does not exist yet, with src's permission bits.
func copyFileMode(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Chmod(fi.Mode().Perm()); err != nil { // the umask must not narrow it
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
