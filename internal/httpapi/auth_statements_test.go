package httpapi

import (
	"database/sql"
	"database/sql/driver"
	"sync"
	"sync/atomic"
	"testing"
)

// countingDriver wraps the sqlite driver and counts the statements its
// connections prepare. Its connections expose Prepare only, so database/sql
// runs every query and exec through Prepare and each one is counted.
type countingDriver struct {
	base driver.Driver
	n    *atomic.Int64
}

func (d countingDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return countingConn{c, d.n}, nil
}

type countingConn struct {
	driver.Conn
	n *atomic.Int64
}

func (c countingConn) Prepare(query string) (driver.Stmt, error) {
	c.n.Add(1)
	return c.Conn.Prepare(query)
}

var (
	registerCounting sync.Once
	statements       atomic.Int64
)

// countStatements points s at a counting connection to its own database for
// the rest of the test.
func countStatements(t *testing.T, s *Server) *atomic.Int64 {
	t.Helper()
	registerCounting.Do(func() {
		sql.Register("sqlite-counting", countingDriver{base: s.rt.db.DB.Driver(), n: &statements})
	})
	db, err := sql.Open("sqlite-counting", "file:"+expandHome(s.opts.DBPath)+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	real := s.rt.db.DB
	s.rt.db.DB = db
	t.Cleanup(func() {
		s.rt.db.DB = real
		db.Close()
	})
	return &statements
}

// Every 401 of the profile-scoped bearer check runs the same statements: a
// wrong token for a profile named by id or by name, and a profile that does
// not exist. Counted, not timed: a count that differs is latency an
// unauthenticated caller could use to learn which profiles exist.
func TestUnauthorizedPathsRunTheSameStatements(t *testing.T) {
	s := newTestServer(t, false)
	addProfile(t, s, "other", "Other")
	testToken(t, s, "default")
	testToken(t, s, "other")
	n := countStatements(t, s)
	count := func(path string) int64 {
		n.Store(0)
		wantUnauthorized(t, path, doReq(t, s, "GET", path, "wrong-token", nil))
		return n.Load()
	}
	paths := []string{"/profiles/other/workflows", "/profiles/Other/workflows", "/profiles/ghost/workflows"}
	for _, p := range paths { // warm the per-profile key caches the token reads fill
		count(p)
	}
	byID, byName, unknown := count(paths[0]), count(paths[1]), count(paths[2])
	if byID == 0 || byName != byID || unknown != byID {
		t.Errorf("statements: wrong token by id %d, by name %d, unknown profile %d; want equal and non-zero", byID, byName, unknown)
	}
}
