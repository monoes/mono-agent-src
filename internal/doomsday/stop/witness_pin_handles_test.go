package stop

import (
	"errors"
	"testing"
)

// fakeHandleFS models a Windows namespace (paths written with slashes so the Linux filepath splits them): names lead to objects; a held handle belongs to an object, not to a name.
type fakeNode struct {
	id      FileID
	pending bool
}

type fakeHandle struct {
	fs     *fakeHandleFS
	obj    *fakeNode
	closed bool
	idErr  error
}

func (h *fakeHandle) ID() (FileID, error)          { return h.obj.id, h.idErr }
func (h *fakeHandle) DeletePending() (bool, error) { return h.obj.pending, nil }
func (h *fakeHandle) Close() error                 { h.closed = true; return nil }

type fakeHandleFS struct {
	ns      map[string]*fakeNode
	handles []*fakeHandle
	openErr map[string]error
	idErr   error
	// openIDErr makes every handle that is opened fail to tell its identity
	openIDErr error
}

func (f *fakeHandleFS) OpenHeld(path string, dir bool) (heldFile, error) {
	if err := f.openErr[path]; err != nil {
		return nil, err
	}
	o := f.ns[path]
	if o == nil {
		return nil, errors.New("file not found")
	}
	h := &fakeHandle{fs: f, obj: o, idErr: f.openIDErr}
	f.handles = append(f.handles, h)
	return h, nil
}

func (f *fakeHandleFS) IDOf(path string) (FileID, error) {
	if f.idErr != nil {
		return FileID{}, f.idErr
	}
	if o := f.ns[path]; o != nil {
		return o.id, nil
	}
	return FileID{}, errors.New("file not found")
}

const winMain = "/w/Users/u/.monoagent/mono.db"

func newFakeHandleFS() *fakeHandleFS {
	f := &fakeHandleFS{ns: map[string]*fakeNode{}, openErr: map[string]error{}}
	for i, p := range []string{winMain, "/w/Users/u/.monoagent", "/w/Users/u", "/w/Users", "/w"} {
		f.ns[p] = &fakeNode{id: FileID{Dev: 7, Ino: uint64(i + 1)}}
	}
	return f
}

func newFakeWatch(t *testing.T) (*handleWatch, *fakeHandleFS) {
	t.Helper()
	f := newFakeHandleFS()
	w, err := newHandleWatch(f, winMain)
	mustNoErr(t, err)
	return w, f
}

func TestHandleWatchHoldsTheFileAndEveryDirectoryAboveWithoutTheRoot(t *testing.T) {
	_, f := newFakeWatch(t)
	if len(f.handles) != 5 { // the file, .monoagent, u, Users, w; not the root
		t.Fatalf("held handles: %d", len(f.handles))
	}
}

func TestHandleWatchConsistentIsQuiet(t *testing.T) {
	w, _ := newFakeWatch(t)
	mustNoErr(t, w.drain(false))
}

func TestHandleWatchDetectsWhatLeavesAPathPointingElsewhere(t *testing.T) {
	cases := map[string]func(f *fakeHandleFS){
		"file replaced":          func(f *fakeHandleFS) { f.ns[winMain] = &fakeNode{id: FileID{Dev: 7, Ino: 99}} },
		"file unlinked":          func(f *fakeHandleFS) { delete(f.ns, winMain) },
		"delete pending":         func(f *fakeHandleFS) { f.ns[winMain].pending = true },
		"directory replaced":     func(f *fakeHandleFS) { f.ns["/w/Users/u/.monoagent"] = &fakeNode{id: FileID{Dev: 7, Ino: 98}} },
		"ancestor replaced":      func(f *fakeHandleFS) { f.ns["/w/Users/u"] = &fakeNode{id: FileID{Dev: 7, Ino: 97}} },
		"path cannot be read":    func(f *fakeHandleFS) { f.idErr = errors.New("access denied") },
		"held handle unreadable": func(f *fakeHandleFS) { f.handles[0].idErr = errors.New("invalid handle") },
		"held identity changed":  func(f *fakeHandleFS) { f.handles[0].obj = &fakeNode{id: FileID{Dev: 8, Ino: 1}} },
	}
	for name, mutate := range cases {
		w, f := newFakeWatch(t)
		mutate(f)
		if err := w.drain(false); !errors.Is(err, ErrPinSwapped) {
			t.Errorf("%s: not detected: %v", name, err)
		}
	}
}

func TestHandleWatchRefusesToStartWhenAnythingCannotBeHeld(t *testing.T) {
	for _, bad := range []string{winMain, "/w/Users/u/.monoagent", "/w/Users/u", "/w/Users", "/w"} {
		f := newFakeHandleFS()
		f.openErr[bad] = errors.New("sharing violation")
		if w, err := newHandleWatch(f, winMain); err == nil || w != nil {
			t.Errorf("a watch started although %s cannot be held", bad)
		}
		for _, h := range f.handles {
			if !h.closed {
				t.Errorf("a handle was left open after the refusal (%s)", bad)
			}
		}
	}
}

func TestHandleWatchAdoptsSiblingsOfTheRecordedIdentityOnly(t *testing.T) {
	w, f := newFakeWatch(t)
	wal := winMain + "-wal"
	f.ns[wal] = &fakeNode{id: FileID{Dev: 7, Ino: 50}}
	mustNoErr(t, w.adopt(wal, FileID{Dev: 7, Ino: 50}))
	if err := w.adopt(wal, FileID{Dev: 7, Ino: 51}); !errors.Is(err, ErrPinSwapped) {
		t.Fatalf("a sibling of another identity was adopted: %v", err)
	}
	if err := w.adopt(winMain+"-shm", FileID{}); err == nil {
		t.Fatal("a missing sibling was adopted")
	}
	f.ns[wal] = &fakeNode{id: FileID{Dev: 7, Ino: 52}} // the adopted name now leads elsewhere
	if err := w.drain(false); !errors.Is(err, ErrPinSwapped) {
		t.Fatalf("a replaced sibling passed: %v", err)
	}
}

func TestHandleWatchCloseReleasesEverythingOnceAndIsQuietAfter(t *testing.T) {
	w, f := newFakeWatch(t)
	w.close()
	w.close()
	for _, h := range f.handles {
		if !h.closed {
			t.Fatal("a handle stayed open")
		}
	}
	delete(f.ns, winMain)
	mustNoErr(t, w.drain(false)) // a closed watch is the clean-close path; the caller compares identities itself
}

// The documented weaker guarantee: no queue of past events, so a swap undone between two looks is not remembered. The
// system's sharing mode is what refuses it; if this ever becomes detectable, the Windows text in render.go changes with it.
func TestHandleWatchDoesNotSeeASwapThatWasUndone(t *testing.T) {
	w, f := newFakeWatch(t)
	orig := f.ns[winMain]
	f.ns[winMain] = &fakeNode{id: FileID{Dev: 7, Ino: 99}}
	f.ns[winMain] = orig
	mustNoErr(t, w.drain(false))
}

func TestSqliteURIOnWindowsIsAFileURIWithForwardSlashes(t *testing.T) {
	if got := sqliteURI("windows", `C:\Users\u\.monoagent\mono.db`); got != "file:///C:/Users/u/.monoagent/mono.db" {
		t.Fatalf("%s", got)
	}
	if got := sqliteURI("linux", "/home/u/mono.db"); got != "file:/home/u/mono.db" {
		t.Fatalf("%s", got)
	}
}

func TestHandleWatchClosesAHandleWhoseIdentityCannotBeRead(t *testing.T) {
	f := newFakeHandleFS()
	f.openIDErr = errors.New("invalid handle")
	if w, err := newHandleWatch(f, winMain); err == nil || w != nil {
		t.Fatal("a watch started on a handle with no identity")
	}
	for _, h := range f.handles {
		if !h.closed {
			t.Fatal("a handle was left open after its identity failed")
		}
	}
}
