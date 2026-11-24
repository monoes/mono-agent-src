// Package datatable keeps data tables: n8n-style tables of typed columns a
// profile keeps across workflow runs (tables data_tables and
// data_table_rows, migration 079). The core.data_table node and
// `monoagentcli table` work through it.
package datatable

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// Column types, as n8n names them.
const (
	TypeString  = "string"
	TypeNumber  = "number"
	TypeBoolean = "boolean"
	TypeDate    = "date"
)

// SystemColumns are the columns every row has; they cannot be set.
var SystemColumns = map[string]string{"id": TypeNumber, "createdAt": TypeDate, "updatedAt": TypeDate}

// ErrNotFound is returned for a table that does not exist.
var ErrNotFound = errors.New("data table not found")

const timeLayout = "2006-01-02T15:04:05.000Z"

func now() string { return time.Now().UTC().Format(timeLayout) }

// Column is one typed column of a table.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Table is a data table without its rows.
type Table struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Columns   []Column `json:"columns"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

// ColumnType returns the type of a column (system columns included).
func (t Table) ColumnType(name string) (string, bool) {
	if typ, ok := SystemColumns[name]; ok {
		return typ, true
	}
	for _, c := range t.Columns {
		if c.Name == name {
			return c.Type, true
		}
	}
	return "", false
}

// Store reads and writes one profile's data tables.
type Store struct {
	db      *sql.DB
	profile string
}

// New returns the store of profile. An empty profile is kept as is: every
// method then returns profiledir.ErrNoProfile (there is no default profile).
func New(db *sql.DB, profile string) *Store {
	return &Store{db: db, profile: profile}
}

// ValidType reports whether typ is a column type.
func ValidType(typ string) bool {
	switch typ {
	case TypeString, TypeNumber, TypeBoolean, TypeDate:
		return true
	}
	return false
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateTable creates a table with columns (a column type defaults to
// string).
func (s *Store) CreateTable(ctx context.Context, name string, cols []Column) (Table, error) {
	if s.profile == "" {
		return Table{}, profiledir.ErrNoProfile
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Table{}, errors.New("a data table needs a name")
	}
	seen := map[string]bool{}
	for i, c := range cols {
		if c.Type == "" {
			cols[i].Type = TypeString
		}
		if err := checkColumn(cols[i], seen); err != nil {
			return Table{}, err
		}
	}
	if cols == nil {
		cols = []Column{}
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM data_tables WHERE profile_id = ? AND lower(name) = lower(?)`, s.profile, name).Scan(&n); err != nil {
		return Table{}, err
	}
	if n > 0 {
		return Table{}, fmt.Errorf("a data table named %q already exists", name)
	}
	t := Table{ID: newID(), Name: name, Columns: cols, CreatedAt: now()}
	t.UpdatedAt = t.CreatedAt
	raw, _ := json.Marshal(cols)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO data_tables (id, profile_id, name, columns, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, s.profile, t.Name, string(raw), t.CreatedAt, t.UpdatedAt); err != nil {
		// A concurrent create passed the check above; UNIQUE (profile_id, name) catches the exact-name race.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Table{}, fmt.Errorf("a data table named %q already exists", name)
		}
		return Table{}, fmt.Errorf("creating data table %q: %w", name, err)
	}
	return t, nil
}

func checkColumn(c Column, seen map[string]bool) error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("a column needs a name")
	}
	if _, sys := SystemColumns[c.Name]; sys {
		return fmt.Errorf("column %q is a system column of every data table", c.Name)
	}
	if !ValidType(c.Type) {
		return fmt.Errorf("column %q: type %q (use string, number, boolean or date)", c.Name, c.Type)
	}
	if seen[c.Name] {
		return fmt.Errorf("column %q is listed twice", c.Name)
	}
	seen[c.Name] = true
	return nil
}

const tableCols = `id, name, columns, created_at, updated_at`

func scanTable(sc interface{ Scan(...interface{}) error }) (Table, error) {
	var t Table
	var cols string
	if err := sc.Scan(&t.ID, &t.Name, &cols, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return Table{}, err
	}
	if err := json.Unmarshal([]byte(cols), &t.Columns); err != nil || t.Columns == nil {
		t.Columns = []Column{}
	}
	return t, nil
}

func (s *Store) findByName(ctx context.Context, name string) (Table, error) {
	if s.profile == "" {
		return Table{}, profiledir.ErrNoProfile
	}
	t, err := scanTable(s.db.QueryRowContext(ctx, `SELECT `+tableCols+` FROM data_tables WHERE profile_id = ? AND name = ?`, s.profile, name))
	if errors.Is(err, sql.ErrNoRows) {
		// n8n looks names up case-insensitively.
		t, err = scanTable(s.db.QueryRowContext(ctx, `SELECT `+tableCols+` FROM data_tables WHERE profile_id = ? AND lower(name) = lower(?) ORDER BY name LIMIT 1`, s.profile, name))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Table{}, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return t, err
}

// FindTable finds a table by id or by name.
func (s *Store) FindTable(ctx context.Context, ref string) (Table, error) {
	if s.profile == "" {
		return Table{}, profiledir.ErrNoProfile
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Table{}, errors.New("no data table given (set its name or id)")
	}
	t, err := scanTable(s.db.QueryRowContext(ctx, `SELECT `+tableCols+` FROM data_tables WHERE profile_id = ? AND id = ?`, s.profile, ref))
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Table{}, err
	}
	return s.findByName(ctx, ref)
}

// ListTables lists the profile's tables by name.
func (s *Store) ListTables(ctx context.Context) ([]Table, error) {
	if s.profile == "" {
		return nil, profiledir.ErrNoProfile
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+tableCols+` FROM data_tables WHERE profile_id = ? ORDER BY name`, s.profile)
	if err != nil {
		return nil, fmt.Errorf("listing data tables: %w", err)
	}
	defer rows.Close()
	out := []Table{}
	for rows.Next() {
		t, err := scanTable(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountRows counts a table's rows.
func (s *Store) CountRows(ctx context.Context, t Table) (int, error) {
	if s.profile == "" {
		return 0, profiledir.ErrNoProfile
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM data_table_rows WHERE table_id = ? AND table_id IN (SELECT id FROM data_tables WHERE profile_id = ?)`, t.ID, s.profile).Scan(&n)
	return n, err
}

// RenameTable renames a table.
func (s *Store) RenameTable(ctx context.Context, t Table, name string) (Table, error) {
	if s.profile == "" {
		return Table{}, profiledir.ErrNoProfile
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return t, errors.New("the new name is empty")
	}
	if other, err := s.findByName(ctx, name); err == nil && other.ID != t.ID {
		return t, fmt.Errorf("a data table named %q already exists", name)
	}
	t.Name, t.UpdatedAt = name, now()
	_, err := s.db.ExecContext(ctx, `UPDATE data_tables SET name = ?, updated_at = ? WHERE id = ? AND profile_id = ?`, t.Name, t.UpdatedAt, t.ID, s.profile)
	return t, err
}

// DeleteTable deletes a table with its rows.
func (s *Store) DeleteTable(ctx context.Context, t Table) error {
	if s.profile == "" {
		return profiledir.ErrNoProfile
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The table row goes first and decides: a Table of another profile's matches nothing, so its rows are left alone.
	res, err := tx.ExecContext(ctx, `DELETE FROM data_tables WHERE id = ? AND profile_id = ?`, t.ID, s.profile)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %q", ErrNotFound, t.Name)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM data_table_rows WHERE table_id = ?`, t.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearTable deletes all rows of a table and returns how many.
func (s *Store) ClearTable(ctx context.Context, t Table) (int64, error) {
	if s.profile == "" {
		return 0, profiledir.ErrNoProfile
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM data_table_rows WHERE table_id = ? AND table_id IN (SELECT id FROM data_tables WHERE profile_id = ?)`, t.ID, s.profile)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// row is one stored row.
type row struct {
	id                   int64
	data                 map[string]interface{}
	createdAt, updatedAt string
}

// out is the row as nodes and the CLI show it: id, every column (null
// when unset), createdAt and updatedAt.
func (r row) out(t Table) map[string]interface{} {
	m := make(map[string]interface{}, len(t.Columns)+3)
	for k, v := range r.data {
		m[k] = v
	}
	for _, c := range t.Columns {
		if _, ok := m[c.Name]; !ok {
			m[c.Name] = nil
		}
	}
	m["id"], m["createdAt"], m["updatedAt"] = r.id, r.createdAt, r.updatedAt
	return m
}

func (r row) value(col string) interface{} {
	switch col {
	case "id":
		return float64(r.id)
	case "createdAt":
		return r.createdAt
	case "updatedAt":
		return r.updatedAt
	}
	return r.data[col]
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
}

// loadRowsFor loads the rows f can match. An equality on a text column narrows the read in SQL (json_extract), so a
// workflow looping Upsert over N rows does not decode the whole table N times; the caller still applies f to what it gets.
func loadRowsFor(ctx context.Context, q querier, profile string, t Table, f Filter) ([]row, error) {
	query, args := `SELECT id, data, created_at, updated_at FROM data_table_rows WHERE table_id = ? AND table_id IN (SELECT id FROM data_tables WHERE profile_id = ?)`, []interface{}{t.ID, profile}
	if col, val, ok := f.textEquality(t); ok {
		query += ` AND json_extract(data, ?) = ?`
		args = append(args, `$."`+col+`"`, val)
	}
	rs, err := q.QueryContext(ctx, query+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading data table %q: %w", t.Name, err)
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		var data string
		if err := rs.Scan(&r.id, &data, &r.createdAt, &r.updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &r.data); err != nil || r.data == nil {
			r.data = map[string]interface{}{}
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// Query selects rows: those matching Filter (all rows when it has no
// conditions), ordered by SortBy (default id), at most Limit (0: all).
type Query struct {
	Filter Filter
	SortBy string
	Desc   bool
	Limit  int
}

// Select returns the rows a query selects.
func (s *Store) Select(ctx context.Context, t Table, q Query) ([]map[string]interface{}, error) {
	if s.profile == "" {
		return nil, profiledir.ErrNoProfile
	}
	match, err := q.Filter.compile(t)
	if err != nil {
		return nil, err
	}
	sortType := TypeNumber
	if q.SortBy != "" && q.SortBy != "id" {
		var ok bool
		if sortType, ok = t.ColumnType(q.SortBy); !ok {
			return nil, fmt.Errorf("column %q does not exist in data table %q", q.SortBy, t.Name)
		}
	}
	rows, err := loadRowsFor(ctx, s.db, s.profile, t, q.Filter)
	if err != nil {
		return nil, err
	}
	var hits []row
	for _, r := range rows {
		if match(r) {
			hits = append(hits, r)
		}
	}
	if q.SortBy != "" && q.SortBy != "id" || q.Desc {
		col := q.SortBy
		if col == "" {
			col = "id"
		}
		sort.SliceStable(hits, func(i, j int) bool {
			c := compareValues(hits[i].value(col), hits[j].value(col), sortType)
			if q.Desc {
				return c > 0
			}
			return c < 0
		})
	}
	if q.Limit > 0 && len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	out := make([]map[string]interface{}, len(hits))
	for i, r := range hits {
		out[i] = r.out(t)
	}
	return out, nil
}
