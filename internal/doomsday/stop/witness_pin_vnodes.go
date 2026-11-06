package stop

import (
	"fmt"
	"path/filepath"
)

// The macOS form of the pinned binding (note 13.5 sections 3A and 3B), portable so its decisions run on every OS; the
// real calls (witness_pin_darwin.go) sit behind vnodeSys.
//
// macOS has no inotify. The host registers a kqueue EVFILT_VNODE watch (NOTE_DELETE | NOTE_RENAME | NOTE_REVOKE) on
// the vnode of the database file, of -wal and -shm once they exist, and of the database directory and every directory
// above it. A name can only be made to lead to another file by deleting or renaming the file or directory it led to
// (a rename over it deletes the old vnode), and that raises an event on the vnode the host holds. Events stay
// queued until read, so a swap that is undone again is still seen at the next look (as on Linux). Each look also
// resolves every watched path afresh and compares its identity (device and inode) with the held one, so a
// path that leads elsewhere without an event fails as well.
//
// Differences from Linux, which watches the four names and each ancestor's entry:
//   - the macOS watch is on the vnodes that were found at pin time, not on the names, so a file CREATED at a name that
//     was empty (no vnode to watch) is not reported by an event; the path-identity comparison covers the names that
//     matter (database, -wal, -shm) at every look, but a create-and-delete between two looks of a name that was absent
//     (the -journal name, unused in WAL mode) is not seen;
//   - the dispatch of NOTE_DELETE for the target of a rename-over is XNU behaviour that has NOT BEEN VERIFIED here;
//     the path comparison is the independent second detection;
//   - an in-place overwrite of the same inode (T43) and a swap through a mount are not detected, as on Linux.
// The weaker guarantee is stated in the entry dialog for macOS.

type vnodeSys interface {
	// Watch opens path for events only and registers the vnode events; it returns the identity of the opened vnode.
	Watch(path string) (FileID, error)
	// IDOf resolves path afresh and returns the identity of what it leads to.
	IDOf(path string) (FileID, error)
	// Events returns, without waiting, the paths whose vnode raised an event since the last call.
	Events() ([]string, error)
	// Owns says fd is one of the watch's own descriptors (so the holder check does not count it as SQLite's).
	Owns(fd int) bool
	Close()
}

type vnodeEntry struct {
	path string
	id   FileID
}

type vnodeWatch struct {
	sys  vnodeSys
	list []vnodeEntry
	done bool
}

func newVnodeWatch(sys vnodeSys, main string) (*vnodeWatch, error) {
	w := &vnodeWatch{sys: sys}
	if _, err := w.hold(main); err != nil {
		w.close()
		return nil, err
	}
	for dir := filepath.Dir(main); filepath.Dir(dir) != dir; dir = filepath.Dir(dir) {
		if _, err := w.hold(dir); err != nil {
			w.close()
			return nil, err
		}
	}
	return w, nil
}

// hold registers the path and then checks that it still leads to the vnode that was registered: a swap before the
// registration cannot raise an event, so it is caught here.
func (w *vnodeWatch) hold(path string) (FileID, error) {
	id, err := w.sys.Watch(path)
	if err != nil {
		return FileID{}, fmt.Errorf("stop: %s cannot be watched, so the binding cannot be proven: %w", filepath.Base(path), err)
	}
	w.list = append(w.list, vnodeEntry{path: path, id: id})
	if now, err := w.sys.IDOf(path); err != nil || now != id {
		return FileID{}, fmt.Errorf("%w: %s changed while it was being watched (%v)", ErrPinSwapped, filepath.Base(path), err)
	}
	return id, nil
}

func (w *vnodeWatch) adopt(path string, id FileID) error {
	got, err := w.hold(path)
	if err != nil {
		return err
	}
	if got != id {
		return fmt.Errorf("%w: %s is not the file that was identified", ErrPinSwapped, filepath.Base(path))
	}
	return nil
}

// drain: any event on a watched vnode is a swap (nothing the host does raises one), an unreadable queue fails closed,
// and every watched path must still lead to the vnode that was registered.
func (w *vnodeWatch) drain(bool) error {
	if w.done {
		return nil
	}
	evs, err := w.sys.Events()
	if err != nil {
		return fmt.Errorf("%w: the event queue cannot be read: %v", ErrPinSwapped, err)
	}
	if len(evs) > 0 {
		return fmt.Errorf("%w: vnode event on %s", ErrPinSwapped, filepath.Base(evs[0]))
	}
	for _, e := range w.list {
		if now, err := w.sys.IDOf(e.path); err != nil || now != e.id {
			return fmt.Errorf("%w: %s no longer leads to the watched file (%v)", ErrPinSwapped, filepath.Base(e.path), err)
		}
	}
	return nil
}

func (w *vnodeWatch) close() {
	if !w.done {
		w.done = true
		w.sys.Close()
	}
}

func (w *vnodeWatch) ownsFD(fd int) bool { return w.sys.Owns(fd) }
