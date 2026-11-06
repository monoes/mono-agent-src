package stop

import (
	"errors"
	"path/filepath"
	"testing"
)

// fakeVnodes models names leading to vnodes and the kqueue: an event is queued on the VNODE when it is renamed or
// deleted, whatever name it had, and stays queued until read.
type fakeVnode struct {
	id     FileID
	events bool
}

type fakeVnodeSys struct {
	ns        map[string]*fakeVnode
	watched   map[string]*fakeVnode
	evErr     error
	watchErr  map[string]error
	closed    bool
	afterOpen func(path string) // runs between "open the vnode" and "register" (a swap in that gap)
}

func newFakeVnodeSys() *fakeVnodeSys {
	f := &fakeVnodeSys{ns: map[string]*fakeVnode{}, watched: map[string]*fakeVnode{}, watchErr: map[string]error{}}
	for i, p := range []string{"/Users/u/.monoagent/mono.db", "/Users/u/.monoagent", "/Users/u", "/Users"} {
		f.ns[p] = &fakeVnode{id: FileID{Dev: 1, Ino: uint64(i + 1)}}
	}
	return f
}

func (f *fakeVnodeSys) Watch(path string) (FileID, error) {
	path = filepath.ToSlash(path) // keys are written with slashes; filepath.Dir gives backslashes on Windows
	if err := f.watchErr[path]; err != nil {
		return FileID{}, err
	}
	v := f.ns[path]
	if v == nil {
		return FileID{}, errors.New("no such file")
	}
	if f.afterOpen != nil {
		f.afterOpen(path)
	}
	f.watched[path] = v
	return v.id, nil
}

func (f *fakeVnodeSys) IDOf(path string) (FileID, error) {
	path = filepath.ToSlash(path)
	if v := f.ns[path]; v != nil {
		return v.id, nil
	}
	return FileID{}, errors.New("no such file")
}

func (f *fakeVnodeSys) Events() ([]string, error) {
	if f.evErr != nil {
		return nil, f.evErr
	}
	var out []string
	for p, v := range f.watched {
		if v.events {
			v.events = false
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeVnodeSys) Owns(fd int) bool { return fd == 99 }
func (f *fakeVnodeSys) Close()           { f.closed = true }

// rename moves the vnode to another name: the vnode raises the event, the old name leads nowhere.
func (f *fakeVnodeSys) rename(from, to string) {
	v := f.ns[from]
	v.events = true
	delete(f.ns, from)
	f.ns[to] = v
}

const macMain = "/Users/u/.monoagent/mono.db"

func newMacWatch(t *testing.T) (*vnodeWatch, *fakeVnodeSys) {
	t.Helper()
	f := newFakeVnodeSys()
	w, err := newVnodeWatch(f, macMain)
	mustNoErr(t, err)
	return w, f
}

func TestVnodeWatchQuietWhenNothingHappens(t *testing.T) {
	w, f := newMacWatch(t)
	if len(f.watched) != 4 { // the file, .monoagent, u, Users; not /
		t.Fatalf("watched: %d", len(f.watched))
	}
	mustNoErr(t, w.drain(false))
	mustNoErr(t, w.drain(true))
}

func TestVnodeWatchSeesASwapThatWasUndoneAgain(t *testing.T) {
	w, f := newMacWatch(t)
	f.rename(macMain, macMain+".moved")
	f.rename(macMain+".moved", macMain) // the name leads to the same vnode again: only the queued event remains
	if err := w.drain(false); !errors.Is(err, ErrPinSwapped) {
		t.Fatalf("an undone rename was forgotten: %v", err)
	}
}

func TestVnodeWatchDetectsEveryWayTheNameStopsLeadingToTheVnode(t *testing.T) {
	cases := map[string]func(f *fakeVnodeSys){
		"renamed away":                func(f *fakeVnodeSys) { f.rename(macMain, macMain+".moved") },
		"unlinked (event)":            func(f *fakeVnodeSys) { f.ns[macMain].events = true; delete(f.ns, macMain) },
		"replaced, no event":          func(f *fakeVnodeSys) { f.ns[macMain] = &fakeVnode{id: FileID{Dev: 1, Ino: 77}} },
		"directory moved":             func(f *fakeVnodeSys) { f.rename("/Users/u/.monoagent", "/Users/u/.old") },
		"ancestor replaced, no event": func(f *fakeVnodeSys) { f.ns["/Users/u"] = &fakeVnode{id: FileID{Dev: 1, Ino: 78}} },
		"queue unreadable":            func(f *fakeVnodeSys) { f.evErr = errors.New("bad descriptor") },
	}
	for name, mutate := range cases {
		w, f := newMacWatch(t)
		mutate(f)
		if err := w.drain(false); !errors.Is(err, ErrPinSwapped) {
			t.Errorf("%s: not detected: %v", name, err)
		}
	}
}

func TestVnodeWatchRefusesWhenAnythingCannotBeWatched(t *testing.T) {
	for _, bad := range []string{macMain, "/Users/u/.monoagent", "/Users/u", "/Users"} {
		f := newFakeVnodeSys()
		f.watchErr[bad] = errors.New("denied")
		if w, err := newVnodeWatch(f, macMain); err == nil || w != nil {
			t.Errorf("a watch started although %s cannot be watched", bad)
		}
		if !f.closed {
			t.Errorf("the queue was left open after the refusal (%s)", bad)
		}
	}
}

func TestVnodeWatchCatchesASwapBeforeTheRegistration(t *testing.T) {
	f := newFakeVnodeSys()
	f.afterOpen = func(p string) {
		if p == macMain { // another file takes the name after the vnode was opened
			f.afterOpen = nil
			f.watched[p] = f.ns[p]
			f.ns[p] = &fakeVnode{id: FileID{Dev: 1, Ino: 66}}
		}
	}
	if w, err := newVnodeWatch(f, macMain); !errors.Is(err, ErrPinSwapped) || w != nil {
		t.Fatalf("a swap before the registration passed: %v", err)
	}
}

func TestVnodeWatchAdoptsSiblingsOfTheRecordedIdentityOnly(t *testing.T) {
	w, f := newMacWatch(t)
	wal := macMain + "-wal"
	f.ns[wal] = &fakeVnode{id: FileID{Dev: 1, Ino: 40}}
	mustNoErr(t, w.adopt(wal, FileID{Dev: 1, Ino: 40}))
	if err := w.adopt(wal, FileID{Dev: 1, Ino: 41}); !errors.Is(err, ErrPinSwapped) {
		t.Fatalf("a sibling of another identity was adopted: %v", err)
	}
	if err := w.adopt(macMain+"-shm", FileID{}); err == nil {
		t.Fatal("a missing sibling was adopted")
	}
}

func TestVnodeWatchCloseIsOnceAndOwnsItsDescriptors(t *testing.T) {
	w, f := newMacWatch(t)
	if !w.ownsFD(99) || w.ownsFD(5) {
		t.Fatal("ownership is not passed through")
	}
	w.close()
	w.close()
	if !f.closed {
		t.Fatal("not closed")
	}
	f.evErr = errors.New("closed")
	mustNoErr(t, w.drain(false)) // the clean-close path: the caller compares identities itself
}
