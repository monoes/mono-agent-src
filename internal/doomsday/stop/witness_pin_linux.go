package stop

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// The pinned connection of note 13.5 sections 3A and 3B, fallback outcome.
//
// The pinned driver (modernc.org/sqlite) offers no host-owned VFS: its vfs package only serves a read-only
// fs.FS, so the preferred outcome of 3B (the host opens the files itself) is not available. The fallback of the
// note was "discard the connection after an error", which the review (13.5 P1) showed cannot exclude a silent
// library reopen of a swapped file. This implementation replaces the after-the-error rule with an exclusion
// argument that does not depend on SQLite reporting anything:
//
//   - Whatever file a SQLite reopen reads is the file the path resolves to at that moment. Making a path
//     resolve to another file requires a namespace event on the file's name (create, delete, rename in or out)
//     or on one of the directories above it. The host watches exactly those names with inotify from before the
//     first stat of the pin until the end of its life; events are queued by the kernel before the system call
//     that caused them returns, and the queue survives a swap that is undone again.
//   - Every Do reads the queue before the transaction, before the commit (precommit) and after it. Any event
//     on the database, -wal, -shm or -journal name, on a watched ancestor's entry for the path, or a queue
//     overflow, discards the pinned connection and fails closed; the result of the operation is not accepted
//     (the caller must not append "committed").
//   - With no event, the path still resolves to the file that was identified at pin time, so any library reopen
//     reads the pinned file. The descriptor comparison (fstat of every descriptor against stat of its logical
//     path, identity not name) stays as a second, independent detection.
//
// Stated limits: a swap through a mount (outside the directory tree being watched), and an in-place overwrite
// of the same inode (T43), are not namespace events and are not detected. Linux only; elsewhere the mode does
// not start.

// Errors of the pinned connection.
var (
	ErrPinSwapped = errors.New("stop: a file of the database was swapped, moved or replaced; the pinned connection is discarded")
	ErrPinBroken  = errors.New("stop: the pinned connection was discarded and is never reopened automatically")
	ErrNotClean   = errors.New("stop: the pinned connection did not close cleanly; no clean record may be written")
)

// FileID is the identity of a file (device and inode); it is what the witness records as the dbid.
type FileID struct{ Dev, Ino uint64 }

func (f FileID) String() string { return fmt.Sprintf("%x:%x", f.Dev, f.Ino) }

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

// PinOptions configures Pin.
type PinOptions struct {
	DBPath string
	// Open opens the database handle for the pinned connection (nil: the pure-Go sqlite driver). It must return
	// a handle on exactly this path.
	Open func(path string) (*sql.DB, error)
	Hook Hook
}

// Pinned is one connection held open for the host's life through which every chained table is read and
// written. Nothing else uses its *sql.DB.
type Pinned struct {
	mu     sync.Mutex
	db     *sql.DB
	conn   *sql.Conn
	paths  [4]string // main, -wal, -shm, -journal (logical paths, canonical directory)
	ids    [3]FileID // identities at pin time: main, -wal, -shm
	watch  *nsWatch
	broken error
	hook   Hook
}

// Identity is the main file's identity recorded at pin time (written to the witness as the dbid).
func (p *Pinned) Identity() FileID { return p.ids[0] }

// Pin runs the pinning protocol of 3A: watch first, stat, open, WAL, force the sibling files open, stat again,
// identify the connection's descriptors by identity. Anything else fails closed.
func Pin(ctx context.Context, opt PinOptions) (*Pinned, error) {
	if strings.ContainsAny(opt.DBPath, "?#%") {
		return nil, fmt.Errorf("stop: refusing a database path with a URI metacharacter: %q", opt.DBPath)
	}
	abs, err := filepath.Abs(opt.DBPath)
	if err != nil {
		return nil, err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	main := filepath.Join(dir, filepath.Base(abs))
	if fi, lerr := os.Lstat(main); lerr != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("stop: the database must be an existing regular file (not a link): %s", main)
	}
	p := &Pinned{hook: opt.Hook, paths: [4]string{main, main + "-wal", main + "-shm", main + "-journal"}}
	if p.watch, err = newNSWatch(main); err != nil {
		return nil, fmt.Errorf("stop: cannot watch the database names, so the binding cannot be proven: %w", err)
	}
	fail := func(err error) (*Pinned, error) {
		p.discardLocked()
		return nil, err
	}
	i0, err := statID(main)
	if err != nil {
		return fail(err)
	}
	opener := opt.Open
	if opener == nil {
		opener = func(path string) (*sql.DB, error) {
			return sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
		}
	}
	if p.db, err = opener(main); err != nil {
		return fail(err)
	}
	p.db.SetMaxOpenConns(1) // the pool can never open a second connection beside the pinned one
	p.db.SetConnMaxLifetime(0)
	p.db.SetConnMaxIdleTime(0)
	if p.conn, err = p.db.Conn(ctx); err != nil {
		return fail(err)
	}
	var mode string
	if err = p.conn.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || !strings.EqualFold(mode, "wal") {
		return fail(fmt.Errorf("stop: the journal mode is %q, not wal: %v", mode, err))
	}
	var n int
	if err = p.conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		return fail(err)
	}
	for _, q := range []string{`BEGIN IMMEDIATE`, `COMMIT`} { // force -wal and -shm open now, never lazily later
		if _, err = p.conn.ExecContext(ctx, q); err != nil {
			return fail(err)
		}
	}
	if err = p.watch.drain(true); err != nil { // only the creation of -wal and -shm is expected during the pin
		return fail(err)
	}
	opt.Hook.at("pin.opened")
	i1, err := statID(main)
	if err != nil || i1 != i0 {
		return fail(fmt.Errorf("%w: the path identity changed while the database was opened", ErrPinSwapped))
	}
	p.ids[0] = i1
	for k := 1; k <= 2; k++ {
		if p.ids[k], err = statID(p.paths[k]); err != nil {
			return fail(fmt.Errorf("stop: the sibling %s was not forced open: %w", filepath.Base(p.paths[k]), err))
		}
	}
	if err = p.checkLocked(); err != nil { // finds each of the three identities among the descriptors
		return fail(err)
	}
	return p, nil
}

func (p *Pinned) discardLocked() {
	if p.watch != nil {
		p.watch.close()
	}
	if p.conn != nil {
		_ = p.conn.Close()
	}
	if p.db != nil {
		_ = p.db.Close()
	}
}

func (p *Pinned) breakLocked(err error) error {
	if p.broken == nil {
		p.broken = err
		p.discardLocked()
	}
	return err
}

// Check is the detection of 3A: the event queue first, then every descriptor by identity.
func (p *Pinned) Check() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.broken != nil {
		return fmt.Errorf("%w: %v", ErrPinBroken, p.broken)
	}
	if err := p.checkLocked(); err != nil {
		return p.breakLocked(err)
	}
	return nil
}

func (p *Pinned) checkLocked() error {
	if err := p.watch.drain(false); err != nil {
		return err
	}
	for k := 0; k < 3; k++ {
		now, err := statID(p.paths[k])
		if err != nil || now != p.ids[k] {
			return fmt.Errorf("%w: %s no longer resolves to the pinned file", ErrPinSwapped, filepath.Base(p.paths[k]))
		}
	}
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

// scanFDs lists the readable descriptors of this process. A descriptor that disappears while listing is
// skipped; one that exists and cannot be read fails closed.
func scanFDs() ([]fdEntry, error) {
	names, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil, fmt.Errorf("stop: cannot list the descriptors: %w", err)
	}
	var out []fdEntry
	for _, n := range names {
		p := "/proc/self/fd/" + n.Name()
		link, lerr := os.Readlink(p)
		fi, serr := os.Stat(p)
		if errors.Is(lerr, os.ErrNotExist) || errors.Is(serr, os.ErrNotExist) {
			continue
		}
		if lerr != nil || serr != nil {
			if strings.HasPrefix(link, "socket:") || strings.HasPrefix(link, "anon_inode:") || strings.HasPrefix(link, "pipe:") {
				continue
			}
			return nil, fmt.Errorf("stop: descriptor %s cannot be read, so it cannot be identified", n.Name())
		}
		id, ok := idOfInfo(fi)
		if !ok {
			continue
		}
		out = append(out, fdEntry{link: link, id: id})
	}
	return out, nil
}

// reopenCausing: errors after which SQLite may reopen a file on its own (3B fallback). The connection is
// discarded and never reopened.
func reopenCausing(err error) bool {
	if errors.Is(err, driver.ErrBadConn) {
		return true
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		switch coded.Code() & 0xff {
		case 10, 14, 11, 26: // IOERR, CANTOPEN, CORRUPT, NOTADB
			return true
		}
		switch coded.Code() {
		case 261, 264: // BUSY_RECOVERY, READONLY_RECOVERY
			return true
		}
	}
	return false
}

// Do runs fn on the pinned connection between checks. precommit is the check to call just before COMMIT. If any
// check fails, or fn returns a reopen-causing error, the connection is discarded and the result must not be
// accepted, whatever fn returned.
func (p *Pinned) Do(ctx context.Context, fn func(c *sql.Conn, precommit func() error) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.broken != nil {
		return fmt.Errorf("%w: %v", ErrPinBroken, p.broken)
	}
	if err := p.checkLocked(); err != nil {
		return p.breakLocked(err)
	}
	pre := func() error {
		if err := p.checkLocked(); err != nil {
			return p.breakLocked(err)
		}
		return nil
	}
	err := fn(p.conn, pre)
	if err != nil && reopenCausing(err) {
		return errors.Join(p.breakLocked(fmt.Errorf("%w: %v", ErrPinBroken, err)), err)
	}
	if cerr := p.checkLocked(); cerr != nil {
		return errors.Join(p.breakLocked(cerr), err)
	}
	return err
}

// CloseClean closes the connection (SQLite checkpoints and may delete -wal and -shm) and verifies that the
// siblings are absent or the recorded files. Only a nil result allows a clean-shutdown record (T56).
func (p *Pinned) CloseClean() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.broken != nil {
		return fmt.Errorf("%w: %v", ErrNotClean, p.broken)
	}
	if err := p.checkLocked(); err != nil {
		return errors.Join(ErrNotClean, p.breakLocked(err))
	}
	p.watch.close()
	cerr := errors.Join(p.conn.Close(), p.db.Close())
	p.broken = ErrNotClean
	if cerr != nil {
		return errors.Join(ErrNotClean, cerr)
	}
	for k := 0; k < 3; k++ {
		id, err := statID(p.paths[k])
		switch {
		case errors.Is(err, os.ErrNotExist) && k > 0:
		case err != nil || id != p.ids[k]:
			return fmt.Errorf("%w: %s is not the recorded file", ErrNotClean, filepath.Base(p.paths[k]))
		}
	}
	return nil
}

// ----- the namespace watch -----

type watchSpec struct {
	names map[string]bool
	self  bool
}

type nsWatch struct {
	fd   int
	wds  map[int32]watchSpec
	buf  []byte
	done bool
}

// newNSWatch watches the database's directory (the four names, and the directory itself) and, for every
// ancestor, the entry that leads to the database.
func newNSWatch(main string) (*nsWatch, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	w := &nsWatch{fd: fd, wds: map[int32]watchSpec{}, buf: make([]byte, 64<<10)}
	base := filepath.Base(main)
	add := func(path string, mask uint32, spec watchSpec) error {
		wd, err := syscall.InotifyAddWatch(fd, path, mask)
		if err != nil {
			return err
		}
		w.wds[int32(wd)] = spec
		return nil
	}
	const ns = syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO
	dir := filepath.Dir(main)
	if err := add(dir, ns|syscall.IN_DELETE_SELF|syscall.IN_MOVE_SELF,
		watchSpec{names: map[string]bool{base: true, base + "-wal": true, base + "-shm": true, base + "-journal": true}, self: true}); err != nil {
		w.close()
		return nil, err
	}
	for child := dir; filepath.Dir(child) != child; child = filepath.Dir(child) {
		if err := add(filepath.Dir(child), ns, watchSpec{names: map[string]bool{filepath.Base(child): true}}); err != nil {
			w.close()
			return nil, err
		}
	}
	return w, nil
}

func (w *nsWatch) close() {
	if !w.done {
		w.done = true
		_ = syscall.Close(w.fd)
	}
}

// drain reads the queue. During the pin (allowCreate) the creation of the -wal and -shm names is expected.
func (w *nsWatch) drain(allowCreate bool) error {
	if w.done {
		return nil
	}
	for {
		n, err := syscall.Read(w.fd, w.buf)
		if err == syscall.EAGAIN || (err == nil && n == 0) {
			return nil
		}
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: the event queue cannot be read: %v", ErrPinSwapped, err)
		}
		if verr := w.scanEvents(w.buf[:n], allowCreate); verr != nil {
			return verr
		}
	}
}

func (w *nsWatch) scanEvents(b []byte, allowCreate bool) error {
	for len(b) >= syscall.SizeofInotifyEvent {
		// Decoded field by field in the host's byte order (the kernel's struct inotify_event): a cast to
		// syscall.InotifyEvent reads past a buffer that ends with an event, because Go pads the struct's zero-size Name.
		ev := struct {
			Wd        int32
			Mask, Len uint32
		}{int32(binary.NativeEndian.Uint32(b[0:])), binary.NativeEndian.Uint32(b[4:]), binary.NativeEndian.Uint32(b[12:])}
		size := syscall.SizeofInotifyEvent + int(ev.Len)
		if size > len(b) {
			break
		}
		name := strings.TrimRight(string(b[syscall.SizeofInotifyEvent:size]), "\x00")
		b = b[size:]
		if ev.Mask&syscall.IN_Q_OVERFLOW != 0 {
			return fmt.Errorf("%w: the event queue overflowed", ErrPinSwapped)
		}
		spec, known := w.wds[ev.Wd]
		if !known {
			continue
		}
		if ev.Mask&(syscall.IN_DELETE_SELF|syscall.IN_MOVE_SELF|syscall.IN_IGNORED) != 0 {
			return fmt.Errorf("%w: the database directory itself was moved or removed", ErrPinSwapped)
		}
		if !spec.names[name] {
			continue
		}
		if allowCreate && ev.Mask&syscall.IN_CREATE != 0 && (strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm")) {
			continue
		}
		return fmt.Errorf("%w: namespace event 0x%x on %s", ErrPinSwapped, ev.Mask, name)
	}
	return nil
}
