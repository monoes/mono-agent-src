package vector

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	_ "github.com/sijms/go-ora/v2" // registers the "oracle" driver (pure Go)
)

// oracleDriverName is the database/sql driver used for Oracle; tests swap
// it for a recording fake.
var oracleDriverName = "oracle"

// oracleDistances are Oracle 23ai's VECTOR_DISTANCE metrics (n8n's
// distanceStrategy values).
var oracleDistances = map[string]bool{
	"COSINE": true, "DOT": true, "EUCLIDEAN": true, "MANHATTAN": true, "EUCLIDEAN_SQUARED": true, "HAMMING": true,
}

// oracleMetaKey is a metadata key usable in a JSON path of a filter.
var oracleMetaKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// oracleBackend stores documents in an Oracle Database 23ai table with a
// VECTOR column: id VARCHAR2 primary key, text CLOB, metadata JSON text,
// embedding VECTOR. The table is created on first insert.
type oracleBackend struct {
	db                                  *sql.DB
	table, idCol, textCol, metaCol, vec string
	distance                            string
	createTable, prepared               bool
}

func newOracle(t storeTarget) (backend, error) {
	dsn := str(t.cfg, "connection_string")
	if dsn == "" {
		return nil, fmt.Errorf("connection_string is required (use an oracle connection, e.g. oracle://user:pass@host:1521/service)")
	}
	b := &oracleBackend{createTable: boolField(t.cfg, "create_table", true)}
	var err error
	if b.table, err = oracleIdent("collection (table)", t.collection); err != nil {
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
		if *c.dst, err = oracleIdent(c.key, strOr(t.cfg, c.def, c.key)); err != nil {
			return nil, err
		}
	}
	b.distance = strings.ToUpper(strOr(t.cfg, "COSINE", "distance"))
	if b.distance == "INNER_PRODUCT" {
		b.distance = "DOT"
	}
	if !oracleDistances[b.distance] {
		return nil, fmt.Errorf("distance %q is not supported for oracle (want COSINE, DOT, EUCLIDEAN, MANHATTAN, EUCLIDEAN_SQUARED or HAMMING)", b.distance)
	}
	db, err := sql.Open(oracleDriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open oracle: %w", err)
	}
	b.db = db
	return b, nil
}

// oracleIdent validates an identifier (optionally schema-qualified). It
// stays unquoted, so Oracle matches it case-insensitively: n8n's default
// table n8n_vectors is the N8N_VECTORS table LangChain creates.
func oracleIdent(field, name string) (string, error) {
	if !pgIdent.MatchString(name) {
		return "", fmt.Errorf("%s %q is not a valid identifier (letters, digits, underscore; optional schema prefix)", field, name)
	}
	return name, nil
}

func (b *oracleBackend) Close() error { return b.db.Close() }

func (b *oracleBackend) prepare(ctx context.Context) error {
	if b.prepared || !b.createTable {
		return nil
	}
	q := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s VARCHAR2(512) PRIMARY KEY, %s CLOB, %s CLOB CHECK (%s IS JSON), %s VECTOR)",
		b.table, b.idCol, b.textCol, b.metaCol, b.metaCol, b.vec)
	if _, err := b.db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	b.prepared = true
	return nil
}

func (b *oracleBackend) Upsert(ctx context.Context, docs []Document) error {
	if err := b.prepare(ctx); err != nil {
		return err
	}
	q := fmt.Sprintf("MERGE INTO %[1]s t USING (SELECT :1 AS id FROM dual) s ON (t.%[2]s = s.id) "+
		"WHEN MATCHED THEN UPDATE SET t.%[3]s = :2, t.%[4]s = :3, t.%[5]s = TO_VECTOR(:4) "+
		"WHEN NOT MATCHED THEN INSERT (%[2]s, %[3]s, %[4]s, %[5]s) VALUES (:5, :6, :7, TO_VECTOR(:8))",
		b.table, b.idCol, b.textCol, b.metaCol, b.vec)
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
		vec := vectorLiteral(d.Vector)
		if _, err := tx.ExecContext(ctx, q, d.ID, d.Content, string(meta), vec, d.ID, d.Content, string(meta), vec); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("upsert document %q: %w", d.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// oracleFilter turns a metadata filter into a WHERE clause of
// JSON_VALUE equality tests, numbering binds from first. It accepts an
// object {key: value} or n8n's list of {name, value} pairs.
func (b *oracleBackend) oracleFilter(filter interface{}, first int) (string, []interface{}, error) {
	pairs := map[string]interface{}{}
	switch f := filter.(type) {
	case nil:
		return "", nil, nil
	case map[string]interface{}:
		pairs = f
	case []interface{}:
		for _, e := range f {
			m := asMap(e)
			if m == nil {
				return "", nil, fmt.Errorf("oracle filter list entries must be {name, value} objects")
			}
			pairs[valueText(m["name"])] = m["value"]
		}
	default:
		return "", nil, fmt.Errorf("oracle filter must be a JSON object of metadata values, got %T", filter)
	}
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		if !oracleMetaKey.MatchString(k) {
			return "", nil, fmt.Errorf("oracle filter key %q must be letters, digits and underscores", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var conds []string
	var args []interface{}
	for i, k := range keys {
		conds = append(conds, fmt.Sprintf("JSON_VALUE(%s, '$.%s') = :%d", b.metaCol, k, first+i))
		args = append(args, valueText(pairs[k]))
	}
	if len(conds) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

func (b *oracleBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	where, fargs, err := b.oracleFilter(filter, 2)
	if err != nil {
		return nil, err
	}
	args := append([]interface{}{vectorLiteral(vector)}, fargs...)
	args = append(args, topK)
	q := fmt.Sprintf("SELECT %s, %s, %s, VECTOR_DISTANCE(%s, TO_VECTOR(:1), %s) AS distance FROM %s%s ORDER BY distance FETCH FIRST :%d ROWS ONLY",
		b.idCol, b.textCol, b.metaCol, b.vec, b.distance, b.table, where, len(args))
	rows, err := b.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var id interface{}
		var content, meta sql.NullString
		var dist float64
		if err := rows.Scan(&id, &content, &meta, &dist); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		d := Document{ID: oracleID(id), Content: content.String, Metadata: map[string]interface{}{}}
		if meta.Valid && meta.String != "" {
			if err := json.Unmarshal([]byte(meta.String), &d.Metadata); err != nil {
				return nil, fmt.Errorf("document %q metadata: %w", d.ID, err)
			}
		}
		if b.distance == "COSINE" {
			d.Score = 1 - dist
		} else {
			d.Score = -dist
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// oracleID renders an id column value. A RAW id (tables LangChain's
// OracleVS creates for n8n: RAW(16) DEFAULT SYS_GUID()) is shown as Oracle
// shows it, uppercase hex (RAWTOHEX), which Oracle converts back when the
// id is passed to Delete.
func oracleID(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return strings.ToUpper(hex.EncodeToString(t))
	case string:
		return t
	}
	return fmt.Sprint(v)
}

func (b *oracleBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	var q string
	var args []interface{}
	switch {
	case len(ids) > 0:
		marks := make([]string, len(ids))
		for i, id := range ids {
			marks[i] = fmt.Sprintf(":%d", i+1)
			args = append(args, id)
		}
		q = fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)", b.table, b.idCol, strings.Join(marks, ", "))
	case filter != nil:
		where, fargs, err := b.oracleFilter(filter, 1)
		if err != nil {
			return 0, err
		}
		if where == "" {
			return 0, nil
		}
		q, args = "DELETE FROM "+b.table+where, fargs
	case all:
		// "clear before insert" may run against a table not created yet.
		if err := b.prepare(ctx); err != nil {
			return 0, err
		}
		q = "DELETE FROM " + b.table
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
