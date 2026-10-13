package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lib/pq"
)

// pgDriverName is the database/sql driver used for pgvector; tests swap it
// for a recording fake.
var pgDriverName = "postgres"

var pgIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// quoteIdent validates an identifier (optionally schema-qualified) and
// double-quotes each part.
func quoteIdent(field, name string) (string, error) {
	if !pgIdent.MatchString(name) {
		return "", fmt.Errorf("%s %q is not a valid identifier (letters, digits, underscore; optional schema prefix)", field, name)
	}
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = `"` + p + `"`
	}
	return strings.Join(parts, "."), nil
}

type pgBackend struct {
	db                                  *sql.DB
	table, idCol, textCol, metaCol, vec string
	op                                  string // <=> | <-> | <#>
	distance                            string
	createTable                         bool
	prepared                            bool
}

func newPGVector(ctx context.Context, t storeTarget) (backend, error) {
	dsn := str(t.cfg, "connection_string")
	if dsn == "" {
		return nil, fmt.Errorf("connection_string is required (use a postgresql connection)")
	}
	b := &pgBackend{createTable: boolField(t.cfg, "create_table", true)}
	var err error
	if b.table, err = quoteIdent("collection (table)", t.collection); err != nil {
		return nil, err
	}
	cols := []struct {
		dst      *string
		key, def string
	}{
		{&b.idCol, "id_column", "id"},
		{&b.textCol, "content_column", "text"},
		{&b.metaCol, "metadata_column", "metadata"},
		{&b.vec, "vector_column", "embedding"},
	}
	for _, c := range cols {
		if *c.dst, err = quoteIdent(c.key, strOr(t.cfg, c.def, c.key)); err != nil {
			return nil, err
		}
	}
	b.distance = strings.ToLower(strOr(t.cfg, "cosine", "distance"))
	switch b.distance {
	case "cosine":
		b.op = "<=>"
	case "euclidean":
		b.op = "<->"
	case "inner_product":
		b.op = "<#>"
	default:
		return nil, fmt.Errorf("distance %q is not supported (want cosine, euclidean or inner_product)", b.distance)
	}
	db, err := sql.Open(pgDriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	b.db = db
	return b, nil
}

func (b *pgBackend) Close() error { return b.db.Close() }

func vectorLiteral(v []float64) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = strconv.FormatFloat(f, 'f', -1, 64)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (b *pgBackend) prepare(ctx context.Context) error {
	if b.prepared || !b.createTable {
		return nil
	}
	if _, err := b.db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return fmt.Errorf("create extension vector: %w", err)
	}
	q := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s text PRIMARY KEY, %s text, %s jsonb, %s vector)",
		b.table, b.idCol, b.textCol, b.metaCol, b.vec)
	if _, err := b.db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	b.prepared = true
	return nil
}

func (b *pgBackend) Upsert(ctx context.Context, docs []Document) error {
	if err := b.prepare(ctx); err != nil {
		return err
	}
	q := fmt.Sprintf("INSERT INTO %s (%s, %s, %s, %s) VALUES ($1, $2, $3::jsonb, $4::vector) ON CONFLICT (%s) DO UPDATE SET %s = EXCLUDED.%s, %s = EXCLUDED.%s, %s = EXCLUDED.%s",
		b.table, b.idCol, b.textCol, b.metaCol, b.vec, b.idCol,
		b.textCol, b.textCol, b.metaCol, b.metaCol, b.vec, b.vec)
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	for _, d := range docs {
		meta, err := json.Marshal(nonNilMap(d.Metadata))
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("document %q metadata: %w", d.ID, err)
		}
		if _, err := tx.ExecContext(ctx, q, d.ID, d.Content, string(meta), vectorLiteral(d.Vector)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert document %q: %w", d.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// pgFilterJSON encodes a containment filter; it must be a JSON object.
func pgFilterJSON(filter interface{}) (string, error) {
	m, ok := filter.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("pgvector filter must be a JSON object matched against metadata (@>), got %T", filter)
	}
	b, err := json.Marshal(m)
	return string(b), err
}

func (b *pgBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	args := []interface{}{vectorLiteral(vector)}
	where := ""
	if filter != nil {
		f, err := pgFilterJSON(filter)
		if err != nil {
			return nil, err
		}
		args = append(args, f)
		where = fmt.Sprintf(" WHERE %s @> $2::jsonb", b.metaCol)
	}
	args = append(args, topK)
	q := fmt.Sprintf("SELECT %s, %s, %s, %s %s $1::vector AS distance FROM %s%s ORDER BY distance LIMIT $%d",
		b.idCol, b.textCol, b.metaCol, b.vec, b.op, b.table, where, len(args))
	rows, err := b.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var id, content, meta sql.NullString
		var dist float64
		if err := rows.Scan(&id, &content, &meta, &dist); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		d := Document{ID: id.String, Content: content.String, Metadata: map[string]interface{}{}}
		if meta.Valid && meta.String != "" {
			if err := json.Unmarshal([]byte(meta.String), &d.Metadata); err != nil {
				return nil, fmt.Errorf("document %q metadata: %w", d.ID, err)
			}
		}
		if b.distance == "cosine" {
			d.Score = 1 - dist
		} else {
			d.Score = -dist
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (b *pgBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	var q string
	var args []interface{}
	switch {
	case len(ids) > 0:
		q = fmt.Sprintf("DELETE FROM %s WHERE %s = ANY($1)", b.table, b.idCol)
		args = []interface{}{pq.Array(ids)}
	case filter != nil:
		f, err := pgFilterJSON(filter)
		if err != nil {
			return 0, err
		}
		q = fmt.Sprintf("DELETE FROM %s WHERE %s @> $1::jsonb", b.table, b.metaCol)
		args = []interface{}{f}
	case all:
		// "clear before insert" may run against a table not created yet.
		if err := b.prepare(ctx); err != nil {
			return 0, err
		}
		q = fmt.Sprintf("DELETE FROM %s", b.table)
	default:
		return 0, nil
	}
	res, err := b.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return -1, nil
	}
	return int(n), nil
}
