package apipkg

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

// mergeHeaderKey in a headers map spreads an object of headers (Azure
// x-ms-meta-* from a key/value collection):
// "$merge": "{{ p.options.metadata | kv:'fieldName,fieldValue,x-ms-meta-' }}".
const mergeHeaderKey = "$merge"

// applyHeaders renders a headers map (package, op or step) onto h. The
// "$merge" entry is applied first, so explicit headers override it. Header
// names may be templates; a header whose name or value renders empty is not
// sent.
func applyHeaders(h http.Header, hs map[string]string, sc Scope) error {
	if tv, ok := hs[mergeHeaderKey]; ok {
		v, err := renderString(tv, sc)
		if err != nil {
			return fmt.Errorf("headers.%s: %w", mergeHeaderKey, err)
		}
		switch m := v.(type) {
		case map[string]any:
			for k, x := range m {
				name, val := strings.TrimSpace(k), stringify(x)
				if name != "" && val != "" {
					h.Set(name, val)
				}
			}
		default:
			if !isEmpty(v) {
				return fmt.Errorf("headers.%s: want an object of headers, got %T", mergeHeaderKey, v)
			}
		}
	}
	for k, tv := range hs {
		if k == mergeHeaderKey {
			continue
		}
		name := k
		if hasTemplate(k) {
			n, err := renderText(k, sc)
			if err != nil {
				return fmt.Errorf("header name %s: %w", k, err)
			}
			name = strings.TrimSpace(n)
		}
		if name == "" {
			continue
		}
		v, err := renderText(tv, sc)
		if err != nil {
			return fmt.Errorf("header %s: %w", name, err)
		}
		if v != "" {
			h.Set(name, v)
		}
	}
	return nil
}

// checkHeaderTemplates validates a headers map: its values, its "$merge"
// entry and any templated header names.
func checkHeaderTemplates(where string, hs map[string]string, fields, credKeys map[string]bool) []string {
	var probs []string
	for k, v := range hs {
		probs = append(probs, checkTemplates(where+" headers."+k, v, fields, credKeys)...)
		if k != mergeHeaderKey && hasTemplate(k) {
			probs = append(probs, checkTemplates(where+" header name "+k, k, fields, credKeys)...)
		}
	}
	return probs
}

// lengthOf is the len filter: the number of elements of a list, keys of an
// object, or characters of a text (other scalars: of their text form).
func lengthOf(v any) any {
	switch t := v.(type) {
	case []any:
		return len(t)
	case []string:
		return len(t)
	case map[string]any:
		return len(t)
	case string:
		return utf8.RuneCountInString(t)
	}
	return utf8.RuneCountInString(stringify(v))
}

// urlEncode is the urlencode filter: an object → "k=v&k2=v2" with keys sorted
// and each part percent-encoded like JavaScript's encodeURIComponent (Azure
// x-ms-tags); any other value → its text percent-encoded.
func urlEncode(v any) any {
	esc := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	m, ok := v.(map[string]any)
	if !ok {
		return esc(stringify(v))
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, esc(k)+"="+esc(stringify(m[k])))
	}
	return strings.Join(parts, "&")
}
