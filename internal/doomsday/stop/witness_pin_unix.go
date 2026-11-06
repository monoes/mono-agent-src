//go:build linux || darwin

package stop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func idOfInfo(fi os.FileInfo) (FileID, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return FileID{}, false
	}
	return FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}, true //nolint:unconvert // width differs per arch
}

func statID(path string) (FileID, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return FileID{}, err
	}
	id, ok := idOfInfo(fi)
	if !ok {
		return FileID{}, fmt.Errorf("stop: no file identity for %s", path)
	}
	return id, nil
}

// checkHolders: every descriptor the process holds on one of the pinned names must be the file now at that path, and
// each of the three pinned identities must be held by some descriptor (the connection really is on the pinned files).
func (p *Pinned) checkHolders() error {
	entries, err := scanFDs()
	if err != nil {
		return err
	}
	found := map[FileID]bool{}
	for _, e := range entries {
		if logical := p.logicalOf(e.link); logical != "" {
			now, serr := statID(logical)
			if serr != nil || now != e.id { // a swapped file: also what a " (deleted)" link shows
				return fmt.Errorf("%w: a descriptor on %s is not the file at that path", ErrPinSwapped, filepath.Base(logical))
			}
		}
		found[e.id] = true
	}
	for k := 0; k < 3; k++ { // an expected descriptor that cannot be identified fails closed
		if !found[p.ids[k]] {
			return fmt.Errorf("%w: no descriptor holds %s", ErrPinSwapped, filepath.Base(p.paths[k]))
		}
	}
	return nil
}

// logicalOf maps a descriptor's link text to its logical file, tolerating the " (deleted)" suffix as a hint
// only: the caller compares identity.
func (p *Pinned) logicalOf(link string) string {
	link = strings.TrimSuffix(link, " (deleted)")
	for _, lp := range p.paths {
		if link == lp {
			return lp
		}
	}
	return ""
}

type fdEntry struct {
	link string
	id   FileID
}
