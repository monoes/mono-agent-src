package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/datatable"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// DataTableNode reads and writes the profile's data tables
// (internal/datatable), n8n's Data table node.
//
// Config fields:
//
//	"resource" (string): row (default) or table.
//	"operation" (string): for rows insert (default), get, update, upsert,
//	    deleteRows, rowExists or rowNotExists; for tables create, list,
//	    update (rename), clear or delete.
//	"table" (string): the table's name or id (resolved per item).
//	"columns_mode" (string): values (default: "values" sets the columns) or
//	    auto (the item's JSON, without id/createdAt/updatedAt).
//	"values" (object): column → value (resolved per item). A column the
//	    table lacks is added, typed by its value.
//	"match" (string): any (default) or all of "filters".
//	"filters" ([{column, condition, value}], resolved per item): conditions
//	    eq (default), neq, gt, gte, lt, lte, like, ilike, isEmpty,
//	    isNotEmpty, isTrue, isFalse.
//	get: "return_all" (bool), "limit" (default 50), "order_by" (column) and
//	    "order_direction" (ASC or DESC, default DESC).
//	update/upsert/deleteRows: "dry_run" (bool) returns the rows before and
//	    after without writing.
//	insert: "optimize_bulk" (bool) returns only the count.
//	table create: "table" is the name, "columns" ([{name, type}], type
//	    string, number, boolean or date), "reuse_existing" (default true).
//	table update: "new_name". table list: "filter_name", "sort_field"
//	    (name, createdAt or updatedAt), "sort_direction" (asc or desc),
//	    "return_all" (default true) and "limit".
type DataTableNode struct{}

func (n *DataTableNode) Type() string { return "core.data_table" }

// PublishesToThirdParty: core.data_table sends nothing off this machine (issue #1418).
func (DataTableNode) PublishesToThirdParty() bool { return false }

// WritesLocalStore: core.data_table puts what it is given where another run can read it back (issue #1418).
func (DataTableNode) WritesLocalStore() bool { return true }

// PerItemConfigFields holds the table, values and filters back from the
// engine's one-pass resolution (first item only): each item resolves its
// own, as n8n does.
func (n *DataTableNode) PerItemConfigFields() []string {
	return []string{"table", "values", "filters"}
}

const dataTableDefaultLimit = 50

func (n *DataTableNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	db := vault.DBFromContext(ctx)
	if db == nil {
		return nil, fmt.Errorf("core.data_table: data tables need the profile database")
	}
	profile, perr := vault.ProfileIDFromContext(ctx)
	if perr != nil {
		return nil, fmt.Errorf("core.data_table: ran without a profile: %w", perr)
	}
	store := datatable.New(db, profile)
	resource := configStr(config, "resource", "row")
	op := configStr(config, "operation", "")
	if op == "" {
		op = "insert"
		if resource == "table" {
			op = "list"
		}
	}
	items := input.Items
	if len(items) == 0 {
		items = []workflow.Item{{JSON: map[string]interface{}{}}}
	}
	run := &dataTableRun{store: store, input: input, config: config, tables: map[string]*datatable.Table{}}
	var out []workflow.Item
	var err error
	switch resource {
	case "row":
		out, err = run.rows(ctx, op, items)
	case "table":
		out, err = run.tableOps(ctx, op, items)
	default:
		err = fmt.Errorf("%w: resource %q (use row or table)", workflow.ErrInvalidConfig, resource)
	}
	if err != nil {
		return nil, fmt.Errorf("core.data_table: %w", err)
	}
	if out == nil {
		out = []workflow.Item{}
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

func configStr(config map[string]interface{}, key, def string) string {
	if s, ok := config[key].(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return def
}

func configBool(config map[string]interface{}, key string, def bool) bool {
	switch v := config[key].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return def
}

// dataTableRun is one execution: its config, and the tables it looked up
// (by reference), which carry the columns writes add.
type dataTableRun struct {
	store  *datatable.Store
	input  workflow.NodeInput
	config map[string]interface{}
	tables map[string]*datatable.Table
}

// itemConfig is the per-item part of the config resolved for one item.
type itemConfig struct {
	table   string
	values  map[string]interface{}
	filter  datatable.Filter
	filters int
}

func (r *dataTableRun) resolve(item workflow.Item) (itemConfig, error) {
	raw := map[string]interface{}{}
	for _, k := range []string{"table", "values", "filters"} {
		if v, ok := r.config[k]; ok {
			raw[k] = v
		}
	}
	res, err := workflow.NewExpressionEngine().ResolveConfig(raw, r.input.ItemContext(item.JSON))
	if err != nil {
		return itemConfig{}, err
	}
	ic := itemConfig{table: strings.TrimSpace(fmt.Sprint(valueOr(res["table"], "")))}
	if configStr(r.config, "columns_mode", "values") == "auto" {
		ic.values = map[string]interface{}{}
		for k, v := range item.JSON {
			if _, sys := datatable.SystemColumns[k]; !sys {
				ic.values[k] = v
			}
		}
	} else if m, ok := jsonValue(res["values"]).(map[string]interface{}); ok {
		ic.values = m
	} else if v := res["values"]; v != nil && v != "" {
		return ic, fmt.Errorf("%w: values must be an object of column → value", workflow.ErrInvalidConfig)
	}
	ic.filter.All = configStr(r.config, "match", "any") == "all"
	list, _ := jsonValue(res["filters"]).([]interface{})
	for i, f := range list {
		m, ok := f.(map[string]interface{})
		if !ok {
			return ic, fmt.Errorf("%w: filters[%d] must be {column, condition, value}", workflow.ErrInvalidConfig, i)
		}
		c := datatable.Condition{Column: strings.TrimSpace(fmt.Sprint(valueOr(m["column"], ""))), Value: m["value"]}
		c.Op, _ = m["condition"].(string)
		if c.Column == "" {
			return ic, fmt.Errorf("%w: filters[%d] has no column", workflow.ErrInvalidConfig, i)
		}
		ic.filter.Conditions = append(ic.filter.Conditions, c)
	}
	ic.filters = len(ic.filter.Conditions)
	return ic, nil
}

func (r *dataTableRun) table(ctx context.Context, ref string) (*datatable.Table, error) {
	if t, ok := r.tables[ref]; ok {
		return t, nil
	}
	t, err := r.store.FindTable(ctx, ref)
	if err != nil {
		return nil, err
	}
	r.tables[ref] = &t
	return &t, nil
}

// jsonValue parses a JSON object or list given as text (the editor's
// JSON fields); any other value is returned as it is.
func jsonValue(v interface{}) interface{} {
	s, ok := v.(string)
	if !ok {
		return v
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return v
	}
	return parsed
}

func rowItems(rows []map[string]interface{}) []workflow.Item {
	out := make([]workflow.Item, len(rows))
	for i, row := range rows {
		out[i] = workflow.Item{JSON: row}
	}
	return out
}

func (r *dataTableRun) rows(ctx context.Context, op string, items []workflow.Item) ([]workflow.Item, error) {
	if op == "insert" {
		return r.insert(ctx, items)
	}
	needsFilter := op != "get"
	dryRun := configBool(r.config, "dry_run", false)
	var out []workflow.Item
	for i, item := range items {
		ic, err := r.resolve(item)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		t, err := r.table(ctx, ic.table)
		if err != nil {
			return nil, err
		}
		if needsFilter && ic.filters == 0 {
			return nil, fmt.Errorf("%w: %s needs at least one condition in filters", workflow.ErrInvalidConfig, op)
		}
		var rows []map[string]interface{}
		switch op {
		case "get":
			q := datatable.Query{Filter: ic.filter}
			if !configBool(r.config, "return_all", false) {
				q.Limit = int(configNumber(r.config, "limit"))
				if q.Limit <= 0 {
					q.Limit = dataTableDefaultLimit
				}
			}
			if col := configStr(r.config, "order_by", ""); col != "" {
				q.SortBy, q.Desc = col, !strings.EqualFold(configStr(r.config, "order_direction", "DESC"), "ASC")
			}
			rows, err = r.store.Select(ctx, *t, q)
		case "rowExists", "rowNotExists":
			rows, err = r.store.Select(ctx, *t, datatable.Query{Filter: ic.filter, Limit: 1})
			if err == nil && (len(rows) > 0) == (op == "rowExists") {
				out = append(out, item)
			}
			rows = nil
		case "update":
			rows, err = r.store.Update(ctx, t, ic.filter, ic.values, dryRun)
		case "upsert":
			rows, err = r.store.Upsert(ctx, t, ic.filter, ic.values, dryRun)
		case "deleteRows", "delete":
			rows, err = r.store.Delete(ctx, *t, ic.filter, dryRun)
		default:
			return nil, fmt.Errorf("%w: row operation %q (use insert, get, update, upsert, deleteRows, rowExists or rowNotExists)", workflow.ErrInvalidConfig, op)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, rowItems(rows)...)
	}
	return out, nil
}

// insert adds one row per item, all items of a table in one transaction.
func (r *dataTableRun) insert(ctx context.Context, items []workflow.Item) ([]workflow.Item, error) {
	var order []*datatable.Table
	batches := map[*datatable.Table][]map[string]interface{}{}
	for i, item := range items {
		ic, err := r.resolve(item)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		t, err := r.table(ctx, ic.table)
		if err != nil {
			return nil, err
		}
		if _, ok := batches[t]; !ok {
			order = append(order, t)
		}
		if ic.values == nil {
			ic.values = map[string]interface{}{}
		}
		batches[t] = append(batches[t], ic.values)
	}
	var out []workflow.Item
	count := 0
	for _, t := range order {
		rows, err := r.store.Insert(ctx, t, batches[t])
		if err != nil {
			return nil, err
		}
		count += len(rows)
		out = append(out, rowItems(rows)...)
	}
	if configBool(r.config, "optimize_bulk", false) {
		return []workflow.Item{{JSON: map[string]interface{}{"success": true, "insertedRows": count}}}, nil
	}
	return out, nil
}
