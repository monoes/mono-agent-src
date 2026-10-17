package apipkg

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// bulkOn reports whether op's bulk variant applies to this run: its when
// template, rendered for the first item, is true.
func (r *opRun) bulkOn() (bool, error) {
	b := r.op.Bulk
	if b == nil {
		return false, nil
	}
	w, err := renderString(b.When, r.scope)
	if err != nil {
		return false, r.errf("bulk.when: %w", err)
	}
	return truthy(w), nil
}

// runBulk sends op's bulk request once per chunk of bulk.size items. Every
// item's params are resolved and checked as for a per-item run; the batch
// root holds {p, item, index} per item of the chunk.
func (r *opRun) runBulk(ctx context.Context, engine *workflow.ExpressionEngine, config map[string]any, held []string,
	items []workflow.Item, input workflow.NodeInput) ([]workflow.Item, error) {
	b := r.op.Bulk
	rows := make([]any, len(items))
	jsons := make([]any, len(items))
	params := make([]map[string]any, len(items))
	for i, it := range items {
		if it.JSON == nil {
			it.JSON = map[string]any{}
			items[i] = it
		}
		ps, err := itemParams(engine, config, held, it, input)
		if err != nil {
			return nil, r.errf("item %d: %w", i, err)
		}
		applyDefaults(r.op, ps)
		if err := checkRequired(r.pkg, r.op, ps); err != nil {
			return nil, err
		}
		if err := checkCollections(r.pkg, r.op, ps); err != nil {
			return nil, err
		}
		params[i], jsons[i] = ps, it.JSON
		rows[i] = map[string]any{"p": ps, "item": it.JSON, "index": i}
	}
	op := *r.op
	op.Method, op.Path, op.BaseURL = b.Method, b.Path, b.BaseURL
	op.Query, op.Body, op.BodyType, op.Headers = b.Query, b.Body, b.BodyType, b.Headers
	op.Output, op.ErrorWhen, op.ErrorMessage = b.Output, b.ErrorWhen, b.ErrorMessage
	op.Pagination, op.Also, op.Before, op.Poll, op.Binary, op.Bulk, op.Batch = nil, nil, nil, nil, nil, nil, false
	size := b.Size
	if size <= 0 {
		size = len(items)
	}
	var out []workflow.Item
	for start := 0; start < len(items); start += size {
		end := min(start+size, len(items))
		run := &opRun{pkg: r.pkg, op: &op, params: params[start], item: items[start], credDef: r.credDef,
			scope: Scope{P: params[start], Cred: r.scope.Cred, Item: jsons[start].(map[string]any),
				Items: jsons[start:end], Batch: rows[start:end]}}
		res, err := run.execute(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, res...)
	}
	return out, nil
}

// check validates the bulk variant.
func (b *Bulk) check(where string, fields, credKeys map[string]bool) []string {
	var probs []string
	if b.When == "" {
		probs = append(probs, where+": needs a when template")
	}
	if b.Size < 0 {
		probs = append(probs, where+": size must not be negative")
	}
	probs = append(probs, checkStep(where, Step{When: b.When, Method: b.Method, Path: b.Path, BaseURL: b.BaseURL, Query: b.Query,
		Body: b.Body, BodyType: b.BodyType, Headers: b.Headers, ErrorWhen: b.ErrorWhen, ErrorMessage: b.ErrorMessage}, fields, credKeys)...)
	if out := b.Output; out != nil {
		probs = append(probs, checkTemplates(where+" output", []any{out.Path, out.Each, out.From, out.To, out.ToWhen, out.Where}, fields, credKeys)...)
	}
	return probs
}

// checkStep checks a request step (pagination.next, bulk): a method, a
// path, a known body type and its templates.
func checkStep(where string, st Step, fields, credKeys map[string]bool) []string {
	var probs []string
	if (!validMethods[strings.ToUpper(st.Method)] && !hasTemplate(st.Method)) || st.Path == "" {
		probs = append(probs, where+": needs a method (GET, POST, …) and a path")
	}
	if st.BodyType != "" && !validBodyTypes[st.BodyType] {
		probs = append(probs, fmt.Sprintf("%s: unknown body_type %q", where, st.BodyType))
	}
	probs = append(probs, checkTemplates(where, []any{st.When, st.BaseURL, st.Path, st.Query, st.Body, st.ErrorWhen, st.ErrorMessage}, fields, credKeys)...)
	return append(probs, checkHeaderTemplates(where, st.Headers, fields, credKeys)...)
}

var validBodyTypes = map[string]bool{BodyJSON: true, BodyForm: true, BodyMultipart: true, BodyRaw: true, BodyNone: true, BodyNDJSON: true}
