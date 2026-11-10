package datatable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/dbconn"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// dateLayouts are the date forms a date column accepts.
var dateLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}

// coerce converts v to a stored value of column type typ.
func coerce(typ string, v interface{}) (interface{}, error) {
	if v == nil {
		return nil, nil
	}
	switch typ {
	case TypeString:
		switch x := v.(type) {
		case string:
			return x, nil
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64), nil
		case bool, int, int64:
			return fmt.Sprint(x), nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case TypeNumber:
		switch x := v.(type) {
		case float64:
			return x, nil
		case int:
			return float64(x), nil
		case int64:
			return float64(x), nil
		case string:
			if strings.TrimSpace(x) == "" {
				return nil, nil
			}
			if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
				return f, nil
			}
		}
	case TypeBoolean:
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			if b, err := strconv.ParseBool(strings.TrimSpace(x)); err == nil {
				return b, nil
			}
		}
	case TypeDate:
		switch x := v.(type) {
		case time.Time:
			return x.UTC().Format(timeLayout), nil
		case string:
			if strings.TrimSpace(x) == "" {
				return nil, nil
			}
			for _, layout := range dateLayouts {
				if t, err := time.Parse(layout, strings.TrimSpace(x)); err == nil {
					return t.UTC().Format(timeLayout), nil
				}
			}
		}
	}
	return nil, fmt.Errorf("%v (%T) is not a %s", v, v, typ)
}

// inferType is the type of a column added by a value of an unknown one.
func inferType(v interface{}) string {
	switch v.(type) {
	case float64, int, int64:
		return TypeNumber
	case bool:
		return TypeBoolean
	case time.Time:
		return TypeDate
	}
	return TypeString
}

// prepare converts data to the table's column types. A key the table has
// no column for adds one, typed by its value; system columns are dropped.
func prepare(t *Table, data map[string]interface{}) (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(data))
	for k, v := range data {
		if _, sys := SystemColumns[k]; sys {
			continue
		}
		typ, ok := t.ColumnType(k)
		if !ok {
			if strings.TrimSpace(k) == "" {
				continue
			}
			typ = inferType(v)
			t.Columns = append(t.Columns, Column{Name: k, Type: typ})
		}
		cv, err := coerce(typ, v)
		if err != nil {
			return nil, fmt.Errorf("column %q: %v", k, err)
		}
		out[k] = cv
	}
	return out, nil
}

type execQuerier interface {
	querier
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

func (s *Store) saveColumns(ctx context.Context, x execQuerier, t Table) error {
	raw, _ := json.Marshal(t.Columns)
	_, err := x.ExecContext(ctx, `UPDATE data_tables SET columns = ?, updated_at = ? WHERE id = ? AND profile_id = ?`, string(raw), now(), t.ID, s.profile)
	return err
}

func insertRow(ctx context.Context, x execQuerier, t Table, data map[string]interface{}) (row, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return row{}, err
	}
	r := row{data: data, createdAt: now()}
	r.updatedAt = r.createdAt
	res, err := x.ExecContext(ctx, `INSERT INTO data_table_rows (table_id, data, created_at, updated_at) VALUES (?, ?, ?, ?)`, t.ID, string(raw), r.createdAt, r.updatedAt)
	if err != nil {
		return row{}, fmt.Errorf("inserting into data table %q: %w", t.Name, err)
	}
	r.id, err = res.LastInsertId()
	return r, err
}

// write runs fn in a transaction and saves columns fn added to t.
// The write lock is taken first (dbconn.WithWriteTx): Update, Upsert and Delete read the rows before they write,
// and a writer committing in between would otherwise fail them with SQLITE_BUSY_SNAPSHOT (#1460).
func (s *Store) write(ctx context.Context, t *Table, fn func(tx dbconn.ConnTx) error) error {
	return dbconn.WithWriteTx(ctx, s.db, func(conn *sql.Conn) error {
		tx := dbconn.ConnTx{Ctx: ctx, Conn: conn}
		cols := len(t.Columns)
		if err := fn(tx); err != nil {
			return err
		}
		if len(t.Columns) != cols {
			return s.saveColumns(ctx, tx, *t)
		}
		return nil
	})
}

// Insert adds rows and returns them as stored.
func (s *Store) Insert(ctx context.Context, t *Table, rows []map[string]interface{}) ([]map[string]interface{}, error) {
	if s.profile == "" {
		return nil, profiledir.ErrNoProfile
	}
	out := make([]map[string]interface{}, 0, len(rows))
	work := *t
	work.Columns = append([]Column(nil), t.Columns...)
	err := s.write(ctx, &work, func(tx dbconn.ConnTx) error {
		for i, data := range rows {
			d, err := prepare(&work, data)
			if err != nil {
				return fmt.Errorf("row %d: %w", i, err)
			}
			r, err := insertRow(ctx, tx, work, d)
			if err != nil {
				return err
			}
			out = append(out, r.out(work))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	*t = work
	return out, nil
}

// DryRunState marks the before and after rows of a dry run, as in n8n.
const DryRunState = "dryRunState"

// Update sets data on the rows matching f and returns them updated; a dry
// run returns each row before and after without writing.
func (s *Store) Update(ctx context.Context, t *Table, f Filter, data map[string]interface{}, dryRun bool) ([]map[string]interface{}, error) {
	if s.profile == "" {
		return nil, profiledir.ErrNoProfile
	}
	return s.update(ctx, t, f, data, dryRun, false)
}

// Upsert updates the rows matching f, or inserts data when none match.
func (s *Store) Upsert(ctx context.Context, t *Table, f Filter, data map[string]interface{}, dryRun bool) ([]map[string]interface{}, error) {
	if s.profile == "" {
		return nil, profiledir.ErrNoProfile
	}
	return s.update(ctx, t, f, data, dryRun, true)
}

func (s *Store) update(ctx context.Context, t *Table, f Filter, data map[string]interface{}, dryRun, upsert bool) ([]map[string]interface{}, error) {
	match, err := f.compile(*t)
	if err != nil {
		return nil, err
	}
	var out []map[string]interface{}
	work := *t
	work.Columns = append([]Column(nil), t.Columns...)
	err = s.write(ctx, &work, func(tx dbconn.ConnTx) error {
		d, err := prepare(&work, data)
		if err != nil {
			return err
		}
		rows, err := loadRows(ctx, tx, work)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if !match(r) {
				continue
			}
			before := r.out(work)
			merged := make(map[string]interface{}, len(r.data)+len(d))
			for k, v := range r.data {
				merged[k] = v
			}
			for k, v := range d {
				merged[k] = v
			}
			r.data, r.updatedAt = merged, now()
			if dryRun {
				before[DryRunState], out = "before", append(out, before)
				after := r.out(work)
				after[DryRunState] = "after"
				out = append(out, after)
				continue
			}
			raw, _ := json.Marshal(merged)
			if _, err := tx.ExecContext(ctx, `UPDATE data_table_rows SET data = ?, updated_at = ? WHERE id = ?`, string(raw), r.updatedAt, r.id); err != nil {
				return fmt.Errorf("updating data table %q: %w", work.Name, err)
			}
			out = append(out, r.out(work))
		}
		if len(out) > 0 || !upsert {
			return nil
		}
		if dryRun {
			r := row{data: d, createdAt: now()}
			r.updatedAt = r.createdAt
			before := map[string]interface{}{DryRunState: "before"}
			after := r.out(work)
			after["id"], after[DryRunState] = nil, "after"
			out = append(out, before, after)
			return nil
		}
		r, err := insertRow(ctx, tx, work, d)
		if err != nil {
			return err
		}
		out = append(out, r.out(work))
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !dryRun {
		*t = work
	}
	if out == nil {
		out = []map[string]interface{}{}
	}
	return out, nil
}

// Delete deletes the rows matching f and returns them; a dry run only
// returns them.
func (s *Store) Delete(ctx context.Context, t Table, f Filter, dryRun bool) ([]map[string]interface{}, error) {
	if s.profile == "" {
		return nil, profiledir.ErrNoProfile
	}
	match, err := f.compile(t)
	if err != nil {
		return nil, err
	}
	out := []map[string]interface{}{}
	err = s.write(ctx, &t, func(tx dbconn.ConnTx) error {
		rows, err := loadRows(ctx, tx, t)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if !match(r) {
				continue
			}
			o := r.out(t)
			if dryRun {
				o[DryRunState] = "before"
			} else if _, err := tx.ExecContext(ctx, `DELETE FROM data_table_rows WHERE id = ?`, r.id); err != nil {
				return fmt.Errorf("deleting from data table %q: %w", t.Name, err)
			}
			out = append(out, o)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
