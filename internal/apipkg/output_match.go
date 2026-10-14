package apipkg

import "strings"

// applyMatch keeps the list elements that satisfy output.match. Rows is a
// template giving a list of condition objects, or one object for a single
// condition ({{ p | pick:'lookupColumn,lookupValue' }}); no rows keeps
// everything.
func (r *opRun) applyMatch(list []any) ([]any, error) {
	m := r.op.Output.Match
	if m == nil {
		return list, nil
	}
	rowsV, err := renderString(m.Rows, r.scope)
	if err != nil {
		return nil, r.errf("output.match.rows: %w", err)
	}
	conds, _ := rowsV.([]any)
	if obj, ok := rowsV.(map[string]any); ok && len(obj) > 0 {
		conds = []any{obj}
	}
	if len(conds) == 0 {
		return list, nil
	}
	combine, err := renderText(m.Combine, r.scope)
	if err != nil {
		return nil, r.errf("output.match.combine: %w", err)
	}
	first, err := renderString(m.First, r.scope)
	if err != nil {
		return nil, r.errf("output.match.first: %w", err)
	}
	or := strings.EqualFold(combine, "OR")
	kept := make([]any, 0, len(list))
	for _, el := range list {
		if matchRow(el, conds, m.Column, m.Value, or) {
			kept = append(kept, el)
			if b, _ := toBool(first); b == true {
				break
			}
		}
	}
	return kept, nil
}

// matchRow reports whether el satisfies the condition rows (each names a
// field via colKey and a wanted value via valKey), all (AND) or any (OR).
func matchRow(el any, conds []any, colKey, valKey string, or bool) bool {
	obj, _ := el.(map[string]any)
	for _, c := range conds {
		cm, _ := c.(map[string]any)
		ok := stringify(obj[stringify(cm[colKey])]) == stringify(cm[valKey])
		if or && ok {
			return true
		}
		if !or && !ok {
			return false
		}
	}
	return !or
}

// applyWhere keeps the elements for which output.where (el = the
// element) renders true.
func (r *opRun) applyWhere(list []any) ([]any, error) {
	out := list[:0:0]
	for i, el := range list {
		sc := r.scope
		sc.El = el
		v, err := renderString(r.op.Output.Where, sc)
		if err != nil {
			return nil, r.errf("output.where element %d: %w", i, err)
		}
		if truthy(v) {
			out = append(out, el)
		}
	}
	return out, nil
}
