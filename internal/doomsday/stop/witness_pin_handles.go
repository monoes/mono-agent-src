package stop

import (
	"fmt"
	"path/filepath"
)

// The Windows form of the pinned binding (note 13.5 sections 3A and 3B), portable so its decisions run on every OS; the
// real calls (witness_pin_windows.go) sit behind handleFS.
//
// What it guarantees, and how it differs from Linux (which watches the names with inotify and sees a swap that is
// undone again):
//
//   - The host holds an open handle on the database file, on -wal and -shm once they exist, and on the database
//     directory and every directory above it, each opened WITHOUT FILE_SHARE_DELETE. While such a handle is open the
//     system refuses every other open that asks for DELETE access, and a delete, a rename (also of a directory above)
//     and a replace-by-rename need DELETE access, so they fail with a sharing violation instead of happening. SQLite's
//     own handles on these files also omit FILE_SHARE_DELETE.
//   - Every check compares the identity of each held handle (volume serial number and file index) with the one recorded
//     at pin time, asks whether it is delete-pending, and resolves each path again and compares that identity with the
//     held one. A path that no longer leads to the held file (deleted, replaced, redirected) fails closed.
//
// Not detected: an in-place overwrite of the same file (its identity is unchanged; same as T43 on Linux); a swap that
// the system was made to allow (a handle opened earlier that holds DELETE access, whatever its share flags, makes the
// host's own open fail instead, so the pin is refused) and that is undone again between two checks (Windows has no queue of past name events here, so unlike Linux an undone
// swap is not remembered; the sharing mode is what makes it fail); a swap through a mount or junction above the
// database that is not a rename of a held directory; and volumes whose file ids do not fit 64 bits. The weaker
// guarantee is stated in the entry dialog for Windows.

type heldFile interface {
	// ID is the identity (volume serial number, file index) of the open handle.
	ID() (FileID, error)
	// DeletePending says the file was deleted and only the handles keeping it alive are left.
	DeletePending() (bool, error)
	Close() error
}

type handleFS interface {
	// OpenHeld opens path (a file, or a directory when dir) for reading with a share mode that omits delete.
	OpenHeld(path string, dir bool) (heldFile, error)
	// IDOf resolves path afresh and returns the identity of what it leads to.
	IDOf(path string) (FileID, error)
}

type heldEntry struct {
	path string
	id   FileID
	f    heldFile
}

type handleWatch struct {
	fs   handleFS
	held []heldEntry
	done bool
}

// newHandleWatch holds the database file first, then its directory and every directory above it (not the root).
func newHandleWatch(fs handleFS, main string) (*handleWatch, error) {
	w := &handleWatch{fs: fs}
	if err := w.hold(main, false); err != nil {
		w.close()
		return nil, err
	}
	for dir := filepath.Dir(main); filepath.Dir(dir) != dir; dir = filepath.Dir(dir) {
		if err := w.hold(dir, true); err != nil {
			w.close()
			return nil, err
		}
	}
	return w, nil
}

func (w *handleWatch) hold(path string, dir bool) error {
	f, err := w.fs.OpenHeld(path, dir)
	if err != nil {
		return fmt.Errorf("stop: %s cannot be held open without sharing delete, so the binding cannot be proven: %w", filepath.Base(path), err)
	}
	id, err := f.ID()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stop: the identity of %s cannot be read: %w", filepath.Base(path), err)
	}
	w.held = append(w.held, heldEntry{path: path, id: id, f: f})
	return nil
}

// adopt holds a file that only exists once the database is open (-wal, -shm); it must be the file with this identity.
func (w *handleWatch) adopt(path string, id FileID) error {
	if err := w.hold(path, false); err != nil {
		return err
	}
	if got := w.held[len(w.held)-1].id; got != id {
		return fmt.Errorf("%w: %s is not the file that was identified", ErrPinSwapped, filepath.Base(path))
	}
	return nil
}

// drain compares every held handle and every path with what was recorded; allowCreate does not matter here (no events).
func (w *handleWatch) drain(bool) error {
	if w.done {
		return nil
	}
	for _, h := range w.held {
		if id, err := h.f.ID(); err != nil || id != h.id {
			return fmt.Errorf("%w: the held handle on %s changed identity (%v)", ErrPinSwapped, filepath.Base(h.path), err)
		}
		if dp, err := h.f.DeletePending(); err != nil || dp {
			return fmt.Errorf("%w: %s is deleted or its state cannot be read (%v)", ErrPinSwapped, filepath.Base(h.path), err)
		}
		if now, err := w.fs.IDOf(h.path); err != nil || now != h.id {
			return fmt.Errorf("%w: %s no longer leads to the held file (%v)", ErrPinSwapped, filepath.Base(h.path), err)
		}
	}
	return nil
}

func (w *handleWatch) close() {
	if w.done {
		return
	}
	w.done = true
	for _, h := range w.held {
		_ = h.f.Close()
	}
}
