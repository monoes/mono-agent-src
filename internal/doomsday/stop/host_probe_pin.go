package stop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// pinner is what pinSelfTest needs of a pinned connection.
type pinner interface {
	Check() error
	Do(ctx context.Context, fn func(c *sql.Conn, precommit func() error) error) error
	discard()
	// closeSQLite closes the SQLite connection and leaves the host's own holds in place.
	closeSQLite()
}

func realPinner(ctx context.Context, o PinOptions) (pinner, error) {
	p, err := Pin(ctx, o)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Pinned) discard() { p.mu.Lock(); p.discardLocked(); p.mu.Unlock() }

func (p *Pinned) closeSQLite() {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.conn.Close()
	_ = p.db.Close()
}

func makeScratchDB(path string) error {
	db, err := sql.Open("sqlite", pinDSN(path)+"?_pragma=journal_mode(WAL)")
	if err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS t(x INTEGER)`); err != nil {
		_ = db.Close()
		return err
	}
	return db.Close()
}

// pinScenarioDef is one change a hostile or careless process could make, tried on a scratch database that is pinned.
// op gets the database path, a spare file next to it, and the scratch directory.
type pinScenarioDef struct {
	Name string
	// Away says the change is the rename-away-and-back kind: the names lead to the pinned files again afterwards, so only
	// a memory of the event (inotify queue, kqueue) can report it, not a comparison of paths.
	Away bool
	Op   func(db, other, dir string) error
}

var errScenarioBroke = errors.New("the second half of the scenario failed")

var defaultPinScenarios = []pinScenarioDef{
	{Name: "moving the database away", Op: func(db, _, _ string) error { return os.Rename(db, db+".moved") }},
	{Name: "replacing the database by another file", Op: func(db, other, _ string) error { return os.Rename(other, db) }},
	{Name: "removing the database", Op: func(db, _, _ string) error { return os.Remove(db) }},
	{Name: "moving the -wal file away", Op: func(db, _, _ string) error { return os.Rename(db+"-wal", db+"-wal.moved") }},
	{Name: "removing the -shm file", Op: func(db, _, _ string) error { return os.Remove(db + "-shm") }},
	{Name: "renaming the database directory", Op: func(_, _, dir string) error { return os.Rename(dir, dir+".moved") }},
	{Name: "moving the database away and back", Away: true, Op: func(db, _, _ string) error {
		if err := os.Rename(db, db+".moved"); err != nil {
			return err
		}
		if err := os.Rename(db+".moved", db); err != nil {
			return fmt.Errorf("%w: %v", errScenarioBroke, err)
		}
		return nil
	}},
}

// pinSelfTest pins a scratch database next to the real one and tries every scenario. For each, either the system REFUSED
// the change with an error of the class refused says is the expected one (and the pin stays quiet and the database is
// still there), or the change happened and the pin REPORTED it (ErrPinSwapped). Any other error from the change, and a
// change that happened and was not reported, fail the step. The rename-away-and-back scenario can only be reported by an
// event memory, so it shows that the queue works on this machine.
//
// On Windows every scenario runs a second time with SQLite's own connection closed and only the host's holds in place,
// so a refusal there is the host's. What remains unproven on any OS: a refusal that comes from a third party's handle
// (antivirus, an indexer, a sync client) on a scratch file is told from the host's by the control operation only for
// plain files, and the real database's own state (open handles of other programs) is not looked at.
func pinSelfTest(parent string, mk func(context.Context, PinOptions) (pinner, error), scenarios []pinScenarioDef, refused func(error) bool) error {
	for _, closed := range pinVariantsNow() {
		for _, sc := range scenarios {
			if err := pinScenario(parent, mk, sc, refused, closed); err != nil {
				name := sc.Name
				if closed {
					name += " (SQLite connection closed, host holds only)"
				}
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}

// pinVariants: with SQLite open; and, on Windows, with only the host's holds.
func pinVariants(goos string) []bool {
	if goos == "windows" {
		return []bool{false, true}
	}
	return []bool{false}
}

var pinVariantsNow = func() []bool { return pinVariants(hostGOOS) }

// pinControl is an operation on a plain scratch file in the pinned directory that holds nothing, which must succeed:
// if the volume, a virus scanner or the directory refuses a plain rename and remove, a refusal of the pinned files proves
// nothing about the binding.
var pinControl = func(dir string) error {
	c := filepath.Join(dir, "c.db")
	if err := os.WriteFile(c, []byte("x"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(c, c+".moved"); err != nil {
		return err
	}
	if err := os.Rename(c+".moved", c); err != nil {
		return err
	}
	return os.Remove(c)
}

func pinScenario(parent string, mk func(context.Context, PinOptions) (pinner, error), sc pinScenarioDef, refused func(error) bool, closed bool) error {
	dir, done, err := scratchDir(parent, ".ma-pin-")
	if err != nil {
		return err
	}
	defer done(dir + ".moved")
	db, other := filepath.Join(dir, "p.db"), filepath.Join(dir, "o.db")
	if err := makeScratchDB(db); err != nil {
		return err
	}
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := mk(ctx, PinOptions{DBPath: db})
	if err != nil {
		return fmt.Errorf("the database cannot be pinned: %w", err)
	}
	defer p.discard()
	if err := p.Check(); err != nil {
		return fmt.Errorf("an untouched pinned database is reported as swapped: %w", err)
	}
	if err := p.Do(ctx, func(c *sql.Conn, _ func() error) error {
		_, err := c.ExecContext(ctx, `INSERT INTO t(x) VALUES (1)`)
		return err
	}); err != nil {
		return fmt.Errorf("the pinned connection does not work: %w", err)
	}
	if err := pinControl(dir); err != nil {
		return fmt.Errorf("the control operation on a plain file next to the pinned database failed, so a refusal would prove nothing about the binding: %w", err)
	}
	if closed {
		p.closeSQLite()
		if err := p.Check(); err != nil {
			return fmt.Errorf("the host's holds alone report a swap after SQLite closed: %w", err)
		}
	}
	if err := sc.Op(db, other, dir); err != nil {
		if errors.Is(err, errScenarioBroke) || refused == nil || !refused(err) {
			return fmt.Errorf("the change failed in a way that is not the system refusing it: %w", err)
		}
		if cerr := p.Check(); cerr != nil {
			return fmt.Errorf("the system refused the change but the pin reports a swap anyway: %w", cerr)
		}
		if _, serr := os.Stat(db); serr != nil {
			return fmt.Errorf("the system refused the change but the database is not at its name: %w", serr)
		}
		return nil
	}
	if err := p.Check(); !errors.Is(err, ErrPinSwapped) {
		return fmt.Errorf("the change happened and the pin did not report it (%v)", err)
	}
	return nil
}
