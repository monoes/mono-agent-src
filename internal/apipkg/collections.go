package apipkg

import (
	"fmt"
	"slices"
	"strings"
)

// checkCollections refuses a key that a collection param of op does not
// declare: an option, or a subfield of an option op.Subfields lists. A
// request merges a collection whole ("$merge": "{{ p.additionalFields }}"),
// so a raw key in one (an "article" in a Zammad ticket's updateFields, a
// "bcc_emails" in Freshservice's additionalFields) would reach the API past
// what the op declares, and past the "if" of its sends. Raw JSON stays
// possible through the params an op declares for it (jsonParameters,
// additionalFieldsJson).
func checkCollections(pkg *Package, op *Operation, params map[string]any) error {
	for _, f := range op.Fields {
		if (f.Type != "collection" && f.Type != "fixedCollection") || len(f.Options) == 0 {
			continue
		}
		m, ok := params[f.Key].(map[string]any)
		if !ok {
			continue
		}
		for k, v := range m {
			if !slices.Contains(f.Options, k) {
				return fmt.Errorf("apipkg %s %s: %s has no option %q (its options: %s)", pkg.ID, op.Key(), f.Key, k, strings.Join(f.Options, ", "))
			}
			sub, ok := op.Subfields[f.Key+"."+k]
			if !ok {
				continue
			}
			for _, obj := range objectsOf(v) {
				for sk := range obj {
					if !slices.Contains(sub, sk) {
						return fmt.Errorf("apipkg %s %s: %s.%s has no field %q (its fields: %s)", pkg.ID, op.Key(), f.Key, k, sk, strings.Join(sub, ", "))
					}
				}
			}
		}
	}
	return nil
}

// objectsOf is v's objects: v itself, or the objects in a list.
func objectsOf(v any) []map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return []map[string]any{x}
	case []any:
		var out []map[string]any
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// CheckConfig checks config as checkCollections does a run's params, ahead
// of any run (workflow validate, activation, n8n import): for an app node,
// or an op a package adds to a native node. A collection or option given as
// an expression is the run's to check; a config whose op the expressions
// choose is not checked.
func CheckConfig(nodeType string, config map[string]any) error {
	if pkg, ok := ForNodeType(nodeType); ok {
		op, err := pkg.resolveOp(config)
		if err != nil {
			return nil
		}
		return checkCollections(pkg, op, config)
	}
	if pkg := extensionFor(nodeType); pkg != nil {
		if op, ok := pkg.Op(strings.TrimSpace(str(config["resource"])), strings.TrimSpace(str(config["operation"]))); ok {
			return checkCollections(pkg, op, config)
		}
	}
	return nil
}
