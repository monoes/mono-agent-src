//go:build linux

package stop

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

func makeDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	mustNoErr(t, err)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS t(x INTEGER)`)
	mustNoErr(t, err)
	mustNoErr(t, db.Close())
}

func pinAt(t *testing.T, path string) *Pinned {
	t.Helper()
	makeDB(t, path)
	p, err := Pin(bg, PinOptions{DBPath: path})
	mustNoErr(t, err)
	t.Cleanup(func() { p.mu.Lock(); p.discardLocked(); p.mu.Unlock() })
	return p
}

func insert(p *Pinned) error {
	return p.Do(bg, func(c *sql.Conn, pre func() error) error {
		_, err := c.ExecContext(bg, `INSERT INTO t VALUES (1)`)
		return err
	})
}

func mv(t *testing.T, from, to string) {
	t.Helper()
	mustNoErr(t, os.Rename(from, to))
}

func TestPinHappyPathAndForeignActivityIsNotAFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dd.db")
	p := pinAt(t, path)
	mustNoErr(t, insert(p))
	// T48: another process (here a second handle) writes and truncates the WAL; unrelated files come and go
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	mustNoErr(t, err)
	_, err = other.Exec(`INSERT INTO t VALUES (2)`)
	mustNoErr(t, err)
	_, err = other.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	mustNoErr(t, err)
	mustNoErr(t, os.WriteFile(filepath.Join(filepath.Dir(path), "unrelated.log"), []byte("x"), 0o600))
	mustNoErr(t, os.Remove(filepath.Join(filepath.Dir(path), "unrelated.log")))
	mustNoErr(t, p.Check())
	mustNoErr(t, insert(p))
	mustNoErr(t, other.Close())
	if p.Identity() != func() FileID { id, _ := statID(path); return id }() {
		t.Fatal("identity is not the main file's")
	}
}

// 13.5 P1 (fallback): a sibling replaced between two checks and restored before the next one must not pass
// silently, because a library reopen in between would have read the replacement. T45.
func TestPinSwapAndRestoreBetweenChecksIsDetected(t *testing.T) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		t.Run("swap"+suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dd.db")
			p := pinAt(t, path)
			f := path + suffix
			mv(t, f, f+".orig")
			mustNoErr(t, os.WriteFile(f, []byte("decoy"), 0o600))
			mustNoErr(t, os.Remove(f))
			mv(t, f+".orig", f) // identical path identity again: only the event queue can tell
			if now, _ := statID(f); now != p.ids[map[string]int{"": 0, "-wal": 1, "-shm": 2}[suffix]] {
				t.Fatal("test setup: identity was not restored")
			}
			if err := p.Check(); !errors.Is(err, ErrPinSwapped) {
				t.Fatalf("a swap that was undone passed the check: %v", err)
			}
			if err := insert(p); !errors.Is(err, ErrPinBroken) {
				t.Fatalf("the discarded connection was used again: %v", err)
			}
		})
	}
}

func TestPinRenameWithoutUnlinkAndAncestorSwap(t *testing.T) {
	t.Run("sibling renamed away", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "dd.db")
		p := pinAt(t, path)
		mv(t, path+"-wal", path+"-wal.bak")
		if err := p.Check(); !errors.Is(err, ErrPinSwapped) {
			t.Fatal(err)
		}
	})
	t.Run("ancestor renamed and restored", func(t *testing.T) {
		root := t.TempDir()
		mustNoErr(t, os.MkdirAll(filepath.Join(root, "a", "b"), 0o700))
		p := pinAt(t, filepath.Join(root, "a", "b", "dd.db"))
		mv(t, filepath.Join(root, "a"), filepath.Join(root, "a2"))
		mv(t, filepath.Join(root, "a2"), filepath.Join(root, "a"))
		if err := p.Check(); !errors.Is(err, ErrPinSwapped) {
			t.Fatalf("an ancestor swap that was undone passed: %v", err)
		}
	})
}

// T40c/T40d: a swap inside the operation is never accepted, whatever the operation returned.
func TestPinSwapInsideOperationIsNotAccepted(t *testing.T) {
	for _, where := range []string{"before-commit", "after-commit"} {
		t.Run(where, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dd.db")
			p := pinAt(t, path)
			var preErr error
			err := p.Do(bg, func(c *sql.Conn, pre func() error) error {
				swap := func() { mv(t, path+"-wal", path+"-wal.x"); mv(t, path+"-wal.x", path+"-wal") }
				if where == "before-commit" {
					swap()
					preErr = pre()
				}
				_, err := c.ExecContext(bg, `INSERT INTO t VALUES (1)`)
				if where == "after-commit" {
					swap()
				}
				return err
			})
			if !errors.Is(err, ErrPinSwapped) {
				t.Fatalf("operation result was accepted: %v", err)
			}
			if where == "before-commit" && !errors.Is(preErr, ErrPinSwapped) {
				t.Fatalf("precommit did not refuse: %v", preErr)
			}
		})
	}
}

type codeErr struct{ code int }

func (e codeErr) Error() string { return "sqlite error" }
func (e codeErr) Code() int     { return e.code }

// T51: each reopen-causing error discards the connection, and it is never reopened.
func TestPinReopenCausingErrorsDiscardAndNeverReopen(t *testing.T) {
	for name, e := range map[string]error{"ioerr": codeErr{10}, "ioerr-ext": codeErr{10 | 3<<8}, "cantopen": codeErr{14},
		"busy-recovery": codeErr{261}, "readonly-recovery": codeErr{264}, "badconn": driver.ErrBadConn} {
		t.Run(name, func(t *testing.T) {
			p := pinAt(t, filepath.Join(t.TempDir(), "dd.db"))
			if err := p.Do(bg, func(*sql.Conn, func() error) error { return e }); !errors.Is(err, ErrPinBroken) {
				t.Fatalf("not discarded: %v", err)
			}
			if err := insert(p); !errors.Is(err, ErrPinBroken) {
				t.Fatalf("reopened or used again: %v", err)
			}
		})
	}
	t.Run("a constraint error is ordinary", func(t *testing.T) {
		p := pinAt(t, filepath.Join(t.TempDir(), "dd.db"))
		_ = p.Do(bg, func(*sql.Conn, func() error) error { return codeErr{19} })
		mustNoErr(t, insert(p))
	})
}

func TestPinCloseClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dd.db")
	p := pinAt(t, path)
	mustNoErr(t, insert(p))
	mustNoErr(t, p.CloseClean())
	if err := insert(p); err == nil {
		t.Fatal("used after close")
	}
	path2 := filepath.Join(t.TempDir(), "dd.db")
	p2 := pinAt(t, path2)
	mv(t, path2+"-wal", path2+"-wal.x")
	mv(t, path2+"-wal.x", path2+"-wal")
	if err := p2.CloseClean(); !errors.Is(err, ErrNotClean) {
		t.Fatalf("a swapped wal still allowed a clean record: %v", err)
	}
}

func TestPinRefusesLinksAndMetacharacters(t *testing.T) {
	dir := t.TempDir()
	makeDB(t, filepath.Join(dir, "real.db"))
	mustNoErr(t, os.Symlink(filepath.Join(dir, "real.db"), filepath.Join(dir, "link.db")))
	if _, err := Pin(bg, PinOptions{DBPath: filepath.Join(dir, "link.db")}); err == nil {
		t.Fatal("a symlinked database was pinned")
	}
	if _, err := Pin(bg, PinOptions{DBPath: filepath.Join(dir, "x?y.db")}); err == nil {
		t.Fatal("a URI metacharacter was accepted")
	}
}

func TestWatchOverflowFailsClosed(t *testing.T) {
	w := &nsWatch{wds: map[int32]watchSpec{}}
	ev := syscall.InotifyEvent{Wd: -1, Mask: syscall.IN_Q_OVERFLOW}
	b := (*[syscall.SizeofInotifyEvent]byte)(unsafe.Pointer(&ev))[:]
	if err := w.scanEvents(append([]byte{}, b...), false); !errors.Is(err, ErrPinSwapped) {
		t.Fatalf("overflow passed: %v", err)
	}
}

// handPinned builds a Pinned over plain files with open descriptors, to test the descriptor layer alone.
func handPinned(t *testing.T) (*Pinned, [3]*os.File, string) {
	t.Helper()
	dir := t.TempDir()
	main := filepath.Join(dir, "dd.db")
	p := &Pinned{paths: [4]string{main, main + "-wal", main + "-shm", main + "-journal"}}
	var fs [3]*os.File
	for k := 0; k < 3; k++ {
		f, err := os.OpenFile(p.paths[k], os.O_CREATE|os.O_RDWR, 0o600)
		mustNoErr(t, err)
		t.Cleanup(func() { f.Close() })
		fs[k] = f
		p.ids[k], err = statID(p.paths[k])
		mustNoErr(t, err)
	}
	var err error
	p.watch, err = newNSWatch(main)
	mustNoErr(t, err)
	t.Cleanup(p.watch.close)
	return p, fs, main
}

// T42/T47 on the descriptor layer alone (the event queue is drained first, so only identities are compared).
func TestPinDescriptorLayerAlone(t *testing.T) {
	t.Run("consistent", func(t *testing.T) {
		p, _, _ := handPinned(t)
		mustNoErr(t, p.checkLocked())
	})
	t.Run("expected descriptor missing", func(t *testing.T) {
		p, fs, _ := handPinned(t)
		fs[1].Close()
		if err := p.checkLocked(); !errors.Is(err, ErrPinSwapped) {
			t.Fatalf("a missing descriptor passed: %v", err)
		}
	})
	t.Run("descriptor on an unlinked file whose path is a new file", func(t *testing.T) {
		p, fs, main := handPinned(t)
		mustNoErr(t, os.Remove(main+"-wal"))
		mustNoErr(t, os.WriteFile(main+"-wal", nil, 0o600))
		_ = p.watch.drain(true) // consume the events; compare identities only
		p.ids[1], _ = statID(main + "-wal")
		keep, err := os.Open(main + "-wal") // the expected identity is held, so only the stale descriptor can fail
		mustNoErr(t, err)
		t.Cleanup(func() { keep.Close() })
		// the path identity now equals the recorded one, but fs[1] reads "(deleted)" and differs from the path
		_ = fs
		if err := p.checkLocked(); !errors.Is(err, ErrPinSwapped) {
			t.Fatalf("a descriptor on a swapped file passed: %v", err)
		}
	})
	t.Run("a file whose name ends in (deleted) is compared by identity", func(t *testing.T) {
		dir := t.TempDir()
		name := filepath.Join(dir, "x (deleted)")
		mustNoErr(t, os.WriteFile(name, nil, 0o600))
		p := &Pinned{paths: [4]string{name, "", "", ""}}
		if p.logicalOf(name) != "" && p.logicalOf(name) != name {
			t.Fatal("classification wrong")
		}
	})
}

// a watch that owns descriptors (macOS: its event-only descriptors) must not satisfy the holder check by itself
type ownsFDWatch struct {
	pinWatch
	owned map[int]bool
}

func (o ownsFDWatch) ownsFD(fd int) bool { return o.owned[fd] }

func TestPinHolderCheckIgnoresTheWatchsOwnDescriptors(t *testing.T) {
	p, fs, _ := handPinned(t)
	mustNoErr(t, p.checkLocked())
	p.watch = ownsFDWatch{pinWatch: p.watch, owned: map[int]bool{int(fs[1].Fd()): true}}
	if err := p.checkLocked(); !errors.Is(err, ErrPinSwapped) {
		t.Fatalf("a descriptor owned by the watch counted as the connection's: %v", err)
	}
}
