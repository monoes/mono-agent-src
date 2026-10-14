package apipkg

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// uniqueList is the unique filter (and the $unique builder key): a list
// without repeated elements, first occurrence kept, order unchanged.
// Elements compare by their JSON; with a path argument objects compare by
// their value at that path (unique:'url'), elements without it are kept.
func uniqueList(v, arg any) (any, error) {
	list, ok := v.([]any)
	if !ok {
		return v, nil
	}
	path := splitPath(stringify(arg))
	seen := map[string]bool{}
	out := make([]any, 0, len(list))
	for _, el := range list {
		key := el
		if path != nil {
			if key = lookupPath(el, path); key == nil {
				out = append(out, el)
				continue
			}
		}
		b, err := json.Marshal(key)
		if err != nil {
			return nil, fmt.Errorf("unique: %w", err)
		}
		if seen[string(b)] {
			continue
		}
		seen[string(b)] = true
		out = append(out, el)
	}
	return out, nil
}

// setKey is the set filter: the object (nil counts as {}) with key set to
// value, a copy; an empty key leaves it unchanged. Excel upsert appends
// {columnToMatchOn: valueToMatchOn, …fields}:
// {{ p.fieldsUi.values | kv:'column,fieldValue' | set:p.columnToMatchOn,p.valueToMatchOn }}.
func setKey(v, arg any) (any, error) {
	args, _ := arg.([]any)
	if len(args) != 2 {
		return nil, fmt.Errorf("set: needs a key and a value")
	}
	out := map[string]any{}
	switch t := v.(type) {
	case nil:
	case map[string]any:
		for k, x := range t {
			out[k] = x
		}
	default:
		return nil, fmt.Errorf("set: value is %T, not an object", v)
	}
	if k := stringify(args[0]); k != "" {
		out[k] = args[1]
	}
	return out, nil
}

// base64Chunks is the base64_chunks filter: base64 data → the list of its
// n-byte pieces, each base64 again (chunked media uploads: X/Twitter
// APPEND sends each 5 MiB segment). Empty data → nil. A list (bin.files)
// gives one flat list of segments across its elements, each
// {"item": element index, "segment": index within it, "data": piece,
// "file": the element}; an element is base64 text or an object with a
// "base64" key.
func base64Chunks(v, arg any) (any, error) {
	n, err := toInt(arg)
	if err != nil || n == nil || n.(int64) <= 0 {
		return nil, fmt.Errorf("base64_chunks: the argument must be a positive byte count")
	}
	size := int(n.(int64))
	list, ok := v.([]any)
	if !ok {
		chunks, err := splitBase64(v, size)
		if err != nil || len(chunks) == 0 {
			return nil, err
		}
		return chunks, nil
	}
	var out []any
	for i, el := range list {
		data := el
		if m, ok := el.(map[string]any); ok {
			data = m["base64"]
		}
		chunks, err := splitBase64(data, size)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		for s, c := range chunks {
			out = append(out, map[string]any{"item": i, "segment": s, "data": c, "file": el})
		}
	}
	return out, nil
}

// splitBase64 decodes base64 text and re-encodes it in size-byte pieces.
func splitBase64(v any, size int) ([]any, error) {
	data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(stringify(v)), ""))
	if err != nil {
		return nil, fmt.Errorf("base64_chunks: value is not base64: %w", err)
	}
	var out []any
	for start := 0; start < len(data); start += size {
		end := min(start+size, len(data))
		out = append(out, base64.StdEncoding.EncodeToString(data[start:end]))
	}
	return out, nil
}

// placeholderRE matches a whole "$N" placeholder.
var placeholderRE = regexp.MustCompile(`^\$(\d+)$`)

// bindParams is the bind filter: n8n's query parameters for JSON queries
// (Elasticsearch, Firestore, MongoDB). Every string value or object key
// that is exactly "$N" is replaced by the N-th element of the parameter
// list (a list or its JSON text), so a parameter never adds structure.
// A key placeholder must yield a plain non-empty name not starting with
// "$" that no other key of that object has; a missing or unused
// parameter is an error. An empty list leaves the value unchanged.
func bindParams(v, arg any) (any, error) {
	params := arg
	if s, ok := arg.(string); ok {
		if strings.TrimSpace(s) == "" {
			return v, nil
		}
		if err := json.Unmarshal([]byte(s), &params); err != nil {
			return nil, fmt.Errorf("bind: the parameters must be a JSON array: %w", err)
		}
	}
	if params == nil {
		return v, nil
	}
	list, ok := params.([]any)
	if !ok {
		return nil, fmt.Errorf("bind: the parameters must be a list, not %T", params)
	}
	if len(list) == 0 {
		return v, nil
	}
	for i, p := range list {
		if !bindScalar(p) {
			inner, isList := p.([]any)
			ok := isList
			for _, x := range inner {
				ok = ok && bindScalar(x)
			}
			if !ok {
				return nil, fmt.Errorf("bind: parameter %d must be a scalar or a list of scalars", i+1)
			}
		}
	}
	used := make([]bool, len(list))
	take := func(ph string) (any, error) {
		n, _ := strconv.Atoi(placeholderRE.FindStringSubmatch(ph)[1])
		if n < 1 || n > len(list) {
			return nil, fmt.Errorf("bind: placeholder %s has no value", ph)
		}
		used[n-1] = true
		return list[n-1], nil
	}
	var walk func(x any) (any, error)
	walk = func(x any) (any, error) {
		switch t := x.(type) {
		case string:
			if placeholderRE.MatchString(t) {
				return take(t)
			}
			return t, nil
		case []any:
			out := make([]any, len(t))
			for i, el := range t {
				r, err := walk(el)
				if err != nil {
					return nil, err
				}
				out[i] = r
			}
			return out, nil
		case map[string]any:
			out := make(map[string]any, len(t))
			for _, k := range sortedKeys(t) {
				key := k
				if placeholderRE.MatchString(k) {
					val, err := take(k)
					if err != nil {
						return nil, err
					}
					s, isStr := val.(string)
					if !isStr || s == "" || strings.HasPrefix(s, "$") || s == "__proto__" || s == "constructor" || s == "prototype" {
						return nil, fmt.Errorf("bind: placeholder %s is not a valid field name", k)
					}
					key = s
				}
				if _, dup := out[key]; dup {
					return nil, fmt.Errorf("bind: field name %q is used more than once", key)
				}
				r, err := walk(t[k])
				if err != nil {
					return nil, err
				}
				out[key] = r
			}
			return out, nil
		}
		return x, nil
	}
	out, err := walk(v)
	if err != nil {
		return nil, err
	}
	for i, u := range used {
		if !u {
			return nil, fmt.Errorf("bind: parameter %d is not used ($%d is not in the query)", i+1, i+1)
		}
	}
	return out, nil
}

func bindScalar(x any) bool {
	switch x.(type) {
	case nil, string, float64, int, int64, bool, json.Number:
		return true
	}
	return false
}
