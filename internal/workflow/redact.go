package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
)

// redactKeyPattern matches map keys whose VALUES are credentials. Matching is
// key-name-only (case-insensitive, exact after trimming nothing) — the
// redactor never inspects or pattern-matches value CONTENT, so a secret that
// lands under an innocuous key is left alone and a public value under a
// sensitive key is still masked (fail-closed on the key name).
var redactKeyPattern = regexp.MustCompile(
	`(?i)^(password|passwd|secret|secrets|token|access_token|refresh_token|id_token|api_key|apikey|authorization|auth|cookie|cookies|client_secret|private_key|bearer)$`)

// RedactedValue replaces every value stored under a sensitive key.
const RedactedValue = "***"

// redactBroadKeyPattern matches, as a case-insensitive SUBSTRING, key names that
// carry credentials in HTTP and error output (Set-Cookie, X-Api-Key,
// Proxy-Authorization, access_token, db_password...). It masks only STRING
// values: a count under max_tokens or a flag under use_secret_store stays, and
// a container under such a key is walked, so its own credential keys are
// masked and its ordinary fields are not.
var redactBroadKeyPattern = regexp.MustCompile(`(?i)(cookie|authorization|api[-_]?key|token|secret|password|passwd|pwd)`)

// AgentTextScrubber, when set, is applied to every string a ReplyRedactor
// shows: a marked process (an agent, the chat's runs, a workflow's process)
// gets credential-shaped text in run output and errors removed, which no key
// name can catch. internal/aitask sets it (its value scanner), as workflow
// cannot import it.
var AgentTextScrubber func(string) string

// RedactAny is v with credential-named string values masked, as RedactItems
// does for items; for stored values that are not item arrays.
func RedactAny(v any) any { return redactValueDepth(v, 0, nil) }

// CycleMarker replaces any value that is part of a reference cycle or
// deeper than MaxRedactDepth during redaction. goja's Export() preserves
// reference cycles from core.code results, so without this guard a cyclic
// item recurses forever and kills the process with a stack overflow.
const CycleMarker = "***cycle***"

// MaxRedactDepth bounds how deep redactValueDepth recurses into nested maps
// and arrays before substituting CycleMarker.
const MaxRedactDepth = 32

// errCyclicItem is returned by marshalItem for values that contain a
// reference cycle or exceed maxMarshalDepth.
var errCyclicItem = errors.New("value contains a cycle or exceeded max nesting depth")

// maxMarshalDepth mirrors encoding/json's own maximum nesting depth; the
// pre-walk in marshalItem never recurses deeper than this.
const maxMarshalDepth = 10000

// MaxOutputItemBytes caps a single output item included in execution
// summaries (`workflow run --json`, MCP workflow_run/workflow_status);
// larger items are replaced by a truncation note so one huge scrape
// doesn't make the summary unusable. Item counts are always preserved
// (the array shape never changes).
const MaxOutputItemBytes = 4 * 1024

// RedactItems returns a copy of items with every value stored under a
// sensitive key (see redactKeyPattern) replaced by RedactedValue. The walk
// is recursive through nested maps and arrays; non-matching keys, and
// values not reachable from a matching key, are copied through unchanged.
// The input is never mutated. A nil slice yields nil.
func RedactItems(items []map[string]any) []map[string]any {
	if items == nil {
		return nil
	}
	out := make([]map[string]any, len(items))
	for i, it := range items {
		out[i] = redactMap(it)
	}
	return out
}

// redactMap returns a redacted copy of m. Cyclic or too-deep values
// reachable from m are replaced by CycleMarker instead of recursing
// forever.
func redactMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out, _ := redactValueDepth(m, 0, nil).(map[string]any)
	return out
}

// redactValueDepth recursively redacts maps and arrays; scalars pass
// through. Guarded against cycles (path-based visited set of container
// pointers) and excessive depth; violations become CycleMarker.
func redactValueDepth(v any, depth int, visited map[uintptr]bool) any {
	if depth > MaxRedactDepth {
		return CycleMarker
	}
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			return t
		}
		ptr := reflect.ValueOf(t).Pointer()
		if visited[ptr] {
			return CycleMarker
		}
		out := make(map[string]any, len(t))
		vNext := withVisited(visited, ptr)
		for k, val := range t {
			if redactKeyPattern.MatchString(k) {
				out[k] = RedactedValue
				continue
			}
			if str, isStr := val.(string); isStr && str != "" && redactBroadKeyPattern.MatchString(k) {
				out[k] = RedactedValue
				continue
			}
			out[k] = redactValueDepth(val, depth+1, vNext)
		}
		return out
	case []any:
		if len(t) == 0 {
			return t
		}
		ptr := reflect.ValueOf(t).Pointer()
		if visited[ptr] {
			return CycleMarker
		}
		out := make([]any, len(t))
		vNext := withVisited(visited, ptr)
		for i, e := range t {
			out[i] = redactValueDepth(e, depth+1, vNext)
		}
		return out
	default:
		return v
	}
}

// withVisited returns a copy of visited with ptr added. Copying keeps the
// set path-scoped, so a subtree that merely appears twice (a shared
// reference, not a cycle) is still redacted normally on both visits.
func withVisited(visited map[uintptr]bool, ptr uintptr) map[uintptr]bool {
	next := make(map[uintptr]bool, len(visited)+1)
	for k := range visited {
		next[k] = true
	}
	next[ptr] = true
	return next
}

// RedactItemJSON returns a copy of item with its JSON map redacted (Binary
// passes through untouched), for callers that deal in Item values.
func RedactItemJSON(item Item) Item {
	return Item{JSON: redactMap(item.JSON), Binary: item.Binary}
}

// TruncateItems returns items with any single item whose JSON encoding
// exceeds MaxOutputItemBytes (or that fails to encode) replaced by a stub
// noting the original size. The input is never mutated. Cyclic values are
// detected by marshalItem's pre-walk, so a cyclic item becomes a stub
// instead of relying solely on encoding/json internals.
func TruncateItems(items []Item) []Item {
	out := make([]Item, len(items))
	for i, it := range items {
		b, err := marshalItem(it)
		if err == nil && len(b) <= MaxOutputItemBytes {
			out[i] = it
			continue
		}
		note := "output item exceeded 4KB and was truncated"
		if err != nil {
			note = fmt.Sprintf("output item could not be encoded (%v) and was truncated", err)
			if errors.Is(err, errCyclicItem) {
				note = fmt.Sprintf("output item contained a cycle (%s) and was truncated", CycleMarker)
			}
		}
		out[i] = Item{JSON: map[string]interface{}{
			"truncated":      true,
			"original_bytes": len(b),
			"note":           note,
		}}
	}
	return out
}

// marshalItem JSON-encodes an Item after a cycle/depth pre-walk over its
// JSON value. The pre-walk mirrors redactValueDepth's guards so a cyclic
// item is reported as an error deterministically, never a crash.
func marshalItem(it Item) ([]byte, error) {
	if err := checkEncodable(it.JSON, 0, nil); err != nil {
		return nil, err
	}
	return json.Marshal(it)
}

// checkEncodable walks v and returns errCyclicItem when a reference cycle
// or nesting beyond maxMarshalDepth is found.
func checkEncodable(v any, depth int, visited map[uintptr]bool) error {
	if depth > maxMarshalDepth {
		return errCyclicItem
	}
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			return nil
		}
		ptr := reflect.ValueOf(t).Pointer()
		if visited[ptr] {
			return errCyclicItem
		}
		vNext := withVisited(visited, ptr)
		for _, val := range t {
			if err := checkEncodable(val, depth+1, vNext); err != nil {
				return err
			}
		}
		return nil
	case []any:
		if len(t) == 0 {
			return nil
		}
		ptr := reflect.ValueOf(t).Pointer()
		if visited[ptr] {
			return errCyclicItem
		}
		vNext := withVisited(visited, ptr)
		for _, e := range t {
			if err := checkEncodable(e, depth+1, vNext); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

// RedactAndTruncateItems applies redaction FIRST, then the size truncation,
// so a masked item smaller than the limit keeps its shape and an oversized
// item is still replaced by the truncation stub. This is the exact pipeline
// used for both `workflow run --json` and the MCP workflow_run /
// workflow_status tools.
func RedactAndTruncateItems(items []Item) []Item {
	if items == nil {
		return nil
	}
	maps := make([]map[string]any, len(items))
	for i, it := range items {
		maps[i] = it.JSON
	}
	redacted := RedactItems(maps)
	out := make([]Item, len(items))
	for i, it := range items {
		out[i] = Item{JSON: redacted[i], Binary: it.Binary}
	}
	return TruncateItems(out)
}
