package profile

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// scopeQuerier is what the scope needs from a database or a transaction.
type scopeQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// scopeEntry is the rows one table holds that belong to a profile: those
// matching Where, a condition on the table that binds the profile id as @p.
type scopeEntry struct {
	Table string
	Where string
	depth int // how many parents it hangs under; children are deleted before parents
}

const (
	wfOfProfile   = `(SELECT id FROM workflows WHERE profile_id = @p)`
	execOfProfile = `(SELECT id FROM workflow_executions WHERE profile_id = @p)`
)

// derivedScope is what a profile owns that carries neither a profile_id nor
// a cascading foreign key: rows keyed by the profile's workflows and
// executions, its counters, and its settings entries.
var derivedScope = []scopeEntry{
	{Table: "app_webhooks", Where: `workflow_id IN ` + wfOfProfile},
	{Table: "webhook_deliveries", Where: `workflow_id IN ` + wfOfProfile},
	{Table: "workflow_static_data", Where: `workflow_id IN ` + wfOfProfile},
	{Table: "workflow_seen_keys", Where: `workflow_id IN ` + wfOfProfile},
	{Table: "workflow_publications", Where: `workflow_id IN ` + wfOfProfile + ` OR profile_id = @p`},
	{Table: "workflow_file_state", Where: `workflow_id IN ` + wfOfProfile},
	{Table: "wait_replies", Where: `workflow_id IN ` + wfOfProfile + ` OR execution_id IN ` + execOfProfile},
	{Table: "subworkflow_runs", Where: `parent_execution_id IN ` + execOfProfile + ` OR child_execution_id IN ` + execOfProfile},
	{Table: "workflow_daily_counters", Where: `profile_key = @p`},
	{Table: "settings", Where: `key = 'task_agent_access:' || @p OR key LIKE 'jev.' || replace(replace(replace(@p, '\', '\\'), '%', '\%'), '_', '\_') || '.%' ESCAPE '\'`},
}

// profileScope lists, for the database q reads, every table with rows of the
// profile: tables with a profile_id column, the tables that hang under them
// through ON DELETE CASCADE (so they are counted too), and derivedScope. The
// tables are ordered so that deleting them in order never leaves a child
// whose parent is already gone out of reach of its condition.
func profileScope(ctx context.Context, q scopeQuerier) ([]scopeEntry, error) {
	names, err := queryStrings(ctx, q, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	var out []scopeEntry
	seen := map[string]bool{}
	var queue []scopeEntry
	for _, n := range names {
		if hasColumn(ctx, q, n, "profile_id") {
			e := scopeEntry{Table: n, Where: `profile_id = @p`}
			queue = append(queue, e)
			seen[n] = true
		}
	}
	for _, e := range derivedScope {
		if have[e.Table] && !seen[e.Table] {
			seen[e.Table] = true
			out = append(out, e)
		}
	}
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		out = append(out, e)
		// Children that cascade from e.Table.
		for _, c := range names {
			if seen[c] {
				continue
			}
			rows, err := q.QueryContext(ctx, fmt.Sprintf(`SELECT "from", "to" FROM pragma_foreign_key_list('%s') WHERE "table" = ? AND on_delete = 'CASCADE'`, c), e.Table)
			if err != nil {
				return nil, err
			}
			var from, to sql.NullString
			found := false
			for rows.Next() && !found {
				if err := rows.Scan(&from, &to); err != nil {
					rows.Close()
					return nil, err
				}
				found = true
			}
			rows.Close()
			if !found {
				continue
			}
			parentCol := to.String
			if parentCol == "" {
				pk, err := queryStrings(ctx, q, fmt.Sprintf(`SELECT name FROM pragma_table_info('%s') WHERE pk = 1`, e.Table))
				if err != nil || len(pk) == 0 {
					return nil, fmt.Errorf("no primary key on %s to follow %s's foreign key", e.Table, c)
				}
				parentCol = pk[0]
			}
			seen[c] = true
			queue = append(queue, scopeEntry{
				Table: c,
				Where: fmt.Sprintf(`"%s" IN (SELECT "%s" FROM "%s" WHERE %s)`, from.String, parentCol, e.Table, e.Where),
				depth: e.depth + 1,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].depth > out[j].depth })
	return out, nil
}

func hasColumn(ctx context.Context, q scopeQuerier, table, col string) bool {
	var n int
	err := q.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = ?`, table), col).Scan(&n)
	return err == nil && n > 0
}

func queryStrings(ctx context.Context, q scopeQuerier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// countScope counts the profile's rows in each scoped table; tables where it
// has none are left out. Table and condition text comes from sqlite_master
// and derivedScope, never from the user.
func countScope(ctx context.Context, q scopeQuerier, scope []scopeEntry, id string) ([]profileTable, error) {
	var out []profileTable
	for _, e := range scope {
		var c int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+e.Table+`" WHERE `+e.Where, sql.Named("p", id)).Scan(&c); err != nil {
			return nil, fmt.Errorf("count %s: %w", e.Table, err)
		}
		if c > 0 {
			out = append(out, profileTable{Table: e.Table, Rows: c})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Table < out[j].Table })
	return out, nil
}
