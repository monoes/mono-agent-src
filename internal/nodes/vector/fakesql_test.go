package vector

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
)

// fakeSQL is a minimal database/sql driver that records every statement
// and answers queries from a script. Each DSN has its own state.
type fakeSQLCall struct {
	Query string
	Args  []interface{}
}

type fakeSQLState struct {
	mu       sync.Mutex
	calls    []fakeSQLCall
	cols     []string
	rows     [][]driver.Value
	affected int64
	commits  int
}

var fakeSQLStates = struct {
	sync.Mutex
	m map[string]*fakeSQLState
}{m: map[string]*fakeSQLState{}}

func init() { sql.Register("vector-fakesql", fakeSQLDriver{}) }

// newFakeSQL returns a fresh state for dsn.
func newFakeSQL(dsn string) *fakeSQLState {
	s := &fakeSQLState{}
	fakeSQLStates.Lock()
	fakeSQLStates.m[dsn] = s
	fakeSQLStates.Unlock()
	return s
}

func (s *fakeSQLState) record(q string, args []driver.NamedValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vals := make([]interface{}, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	s.calls = append(s.calls, fakeSQLCall{Query: q, Args: vals})
}

type fakeSQLDriver struct{}

func (fakeSQLDriver) Open(dsn string) (driver.Conn, error) {
	fakeSQLStates.Lock()
	s := fakeSQLStates.m[dsn]
	fakeSQLStates.Unlock()
	if s == nil {
		return nil, errors.New("fakesql: unknown dsn " + dsn)
	}
	return &fakeSQLConn{s: s}, nil
}

type fakeSQLConn struct{ s *fakeSQLState }

func (c *fakeSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fakesql: Prepare not supported")
}
func (c *fakeSQLConn) Close() error              { return nil }
func (c *fakeSQLConn) Begin() (driver.Tx, error) { return fakeSQLTx{c.s}, nil }

func (c *fakeSQLConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.s.record(q, args)
	return driver.RowsAffected(c.s.affected), nil
}

func (c *fakeSQLConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.s.record(q, args)
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	return &fakeSQLRows{cols: c.s.cols, rows: c.s.rows}, nil
}

// CheckNamedValue accepts every argument type unchanged after the default
// Valuer conversion database/sql already applied.
func (c *fakeSQLConn) CheckNamedValue(nv *driver.NamedValue) error {
	if v, ok := nv.Value.(driver.Valuer); ok {
		val, err := v.Value()
		if err != nil {
			return err
		}
		nv.Value = val
	}
	return nil
}

type fakeSQLTx struct{ s *fakeSQLState }

func (t fakeSQLTx) Commit() error {
	t.s.mu.Lock()
	t.s.commits++
	t.s.mu.Unlock()
	return nil
}
func (t fakeSQLTx) Rollback() error { return nil }

type fakeSQLRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *fakeSQLRows) Columns() []string { return r.cols }
func (r *fakeSQLRows) Close() error      { return nil }
func (r *fakeSQLRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}
