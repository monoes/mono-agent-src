package a2ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var actionName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,63}$`)

// validateComponent checks one component against the catalog. Provenance
// matters only for action names.
func validateComponent(c map[string]any, prov Provenance, lim Limits) error {
	id, _ := c["id"].(string)
	if id == "" || len(id) > 80 {
		return errors.New("component id must be a non-empty string of at most 80 bytes")
	}
	typ, _ := c["component"].(string)
	spec, ok := catalog[typ]
	if !ok {
		return fmt.Errorf("component %q: %q is not in the catalog", id, typ)
	}
	for _, r := range spec.required {
		if _, has := c[r]; !has {
			return fmt.Errorf("component %q (%s): missing %q", id, typ, r)
		}
	}
	for k, v := range c {
		if k == "id" || k == "component" {
			continue
		}
		ps, known := spec.props[k]
		if !known {
			return fmt.Errorf("component %q (%s): unknown property %q", id, typ, k)
		}
		if err := checkProp(v, ps, prov, lim); err != nil {
			return fmt.Errorf("component %q (%s).%s: %w", id, typ, k, err)
		}
	}
	return nil
}

func checkProp(v any, ps propSpec, prov Provenance, lim Limits) error {
	switch ps.kind {
	case pDynString:
		if s, ok := v.(string); ok {
			return checkLiteral(s, lim)
		}
		return checkBinding(v)
	case pDynNumber:
		if _, ok := v.(json.Number); ok {
			return nil
		}
		return checkBinding(v)
	case pDynBool:
		if _, ok := v.(bool); ok {
			return nil
		}
		return checkBinding(v)
	case pDynStringList:
		arr, ok := v.([]any)
		if !ok {
			return checkBinding(v)
		}
		if len(arr) > lim.MaxOptions {
			return errors.New("too many entries")
		}
		for _, e := range arr {
			if s, ok := e.(string); !ok || checkLiteral(s, lim) != nil {
				return errors.New("entries must be short strings")
			}
		}
		return nil
	case pBool:
		if _, ok := v.(bool); !ok {
			return errors.New("must be a boolean")
		}
		return nil
	case pChild:
		if s, ok := v.(string); !ok || s == "" || len(s) > 80 {
			return errors.New("must be a component id")
		}
		return nil
	case pChildren:
		return checkChildren(v, lim)
	case pEnum:
		if s, ok := v.(string); !ok || !slices.Contains(ps.enum, s) {
			return fmt.Errorf("must be one of %s", strings.Join(ps.enum, ", "))
		}
		return nil
	case pOptions:
		return checkOptions(v, lim)
	case pAction:
		return checkAction(v, prov, lim)
	}
	return errors.New("unhandled property kind")
}

func checkChildren(v any, lim Limits) error {
	arr, ok := v.([]any)
	if !ok {
		return errors.New("must be an array of component ids (templates are not supported)")
	}
	if len(arr) > lim.MaxChildren {
		return fmt.Errorf("more than %d children", lim.MaxChildren)
	}
	for _, e := range arr {
		if s, ok := e.(string); !ok || s == "" || len(s) > 80 {
			return errors.New("children must be component ids")
		}
	}
	return nil
}

func checkOptions(v any, lim Limits) error {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 || len(arr) > lim.MaxOptions {
		return fmt.Errorf("must be 1 to %d options", lim.MaxOptions)
	}
	for _, e := range arr {
		o, ok := e.(map[string]any)
		if !ok || len(o) != 2 {
			return errors.New("each option is {label, value}")
		}
		val, vok := o["value"].(string)
		if !vok || checkLiteral(val, lim) != nil {
			return errors.New("option value must be a short string")
		}
		if err := checkProp(o["label"], propSpec{kind: pDynString}, FromAgent, lim); err != nil {
			return fmt.Errorf("option label: %w", err)
		}
	}
	return nil
}

func checkAction(v any, prov Provenance, lim Limits) error {
	a, ok := v.(map[string]any)
	if !ok || len(a) != 1 {
		return errors.New("only {event: {...}} actions are supported")
	}
	ev, ok := a["event"].(map[string]any)
	if !ok {
		return errors.New("only {event: {...}} actions are supported")
	}
	name, _ := ev["name"].(string)
	if !actionName.MatchString(name) {
		return errors.New("event name must be a token such as submit_form")
	}
	if prov != FromSystem {
		for _, p := range ReservedActionPrefixes {
			if strings.HasPrefix(name, p) {
				return fmt.Errorf("event name %q is reserved for system surfaces", name)
			}
		}
	}
	for k, val := range ev {
		switch k {
		case "name":
		case "userMessage":
			if s, ok := val.(string); !ok || checkLiteral(s, lim) != nil {
				return errors.New("userMessage must be a short string")
			}
		case "context":
			ctx, ok := val.(map[string]any)
			if !ok || len(ctx) > lim.MaxOptions {
				return errors.New("context must be a small object")
			}
			for ck, cv := range ctx {
				if err := checkContextValue(cv, lim); err != nil {
					return fmt.Errorf("context.%s: %w", ck, err)
				}
			}
		default:
			return fmt.Errorf("unknown event field %q", k)
		}
	}
	return nil
}

func checkContextValue(v any, lim Limits) error {
	switch t := v.(type) {
	case string:
		return checkLiteral(t, lim)
	case bool, json.Number, nil:
		return nil
	}
	return checkBinding(v)
}

func checkLiteral(s string, lim Limits) error {
	if len(s) > lim.MaxStringBytes {
		return fmt.Errorf("string is %d bytes, over the %d byte cap", len(s), lim.MaxStringBytes)
	}
	return nil
}

// checkBinding accepts exactly {"@path": "<pointer>"}. Function calls
// ("@call") are refused: our catalog has none.
func checkBinding(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return errors.New("must be a literal or a {\"@path\": ...} binding")
	}
	if _, call := m["@call"]; call {
		return errors.New("function calls are not supported")
	}
	p, ok := m["@path"].(string)
	if len(m) != 1 || !ok || !validPointer(p) {
		return errors.New("a binding is exactly {\"@path\": \"/json/pointer\"}")
	}
	return nil
}

// checkReturnedValue checks one value of a returned action's context. It is
// looser than checkContextValue, which bounds what an author may write into a
// component: the user may have typed long text (an edited message), and a
// ChoicePicker resolves to a list of option values.
func checkReturnedValue(v any, lim Limits) error {
	switch t := v.(type) {
	case string:
		if len(t) > lim.MaxReturnedString {
			return fmt.Errorf("string is %d bytes, over the %d byte cap", len(t), lim.MaxReturnedString)
		}
		return nil
	case bool, json.Number, nil:
		return nil
	case []any:
		if len(t) > lim.MaxOptions {
			return errors.New("too many entries")
		}
		for _, e := range t {
			if s, ok := e.(string); !ok || checkLiteral(s, lim) != nil {
				return errors.New("list entries must be short strings")
			}
		}
		return nil
	}
	return errors.New("must be a string, number, boolean or a list of short strings")
}
