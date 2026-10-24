package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactItemsMasksSensitiveKeys(t *testing.T) {
	items := []map[string]any{{
		"token":         "sk-super-secret",
		"PASSWORD":      "hunter2",
		"Api_Key":       "key123",
		"client_secret": "cs-1",
		"bearer":        "b",
	}}
	got := RedactItems(items)
	for _, key := range []string{"token", "PASSWORD", "Api_Key", "client_secret", "bearer"} {
		if v, _ := got[0][key].(string); v != "***" {
			t.Errorf("key %q = %v, want ***", key, got[0][key])
		}
	}
}

func TestRedactItemsNestedAndArrays(t *testing.T) {
	items := []map[string]any{{
		"user": map[string]any{
			"name":     "alice",
			"password": "hunter2",
			"contacts": []any{
				map[string]any{"type": "email", "address": "a@b.c"},
				map[string]any{"type": "api", "access_token": "tok-1"},
			},
		},
		"tags": []any{"x", "y"},
	}}
	got := RedactItems(items)[0]
	user := got["user"].(map[string]any)
	if user["name"] != "alice" {
		t.Errorf("name = %v, want alice (non-matching key must pass through)", user["name"])
	}
	if user["password"] != "***" {
		t.Errorf("nested password = %v, want ***", user["password"])
	}
	contacts := user["contacts"].([]any)
	if contacts[0].(map[string]any)["address"] != "a@b.c" {
		t.Errorf("non-matching nested key altered: %v", contacts[0])
	}
	if contacts[1].(map[string]any)["access_token"] != "***" {
		t.Errorf("nested access_token = %v, want ***", contacts[1])
	}
	if got["tags"].([]any)[0] != "x" {
		t.Errorf("scalar array altered: %v", got["tags"])
	}
}

func TestRedactItemsNonMatchingKeysUntouched(t *testing.T) {
	// Values that LOOK like secrets under innocuous keys are NOT masked:
	// the redactor is key-match-only and never scans content.
	items := []map[string]any{{
		"body":          "password=hunter2 token=abc",
		"tokens_count":  3,
		"authorized":    true,
		"secret_note":   "keep",
		"Authorization": "Bearer xyz",
	}}
	got := RedactItems(items)[0]
	if got["body"] != "password=hunter2 token=abc" {
		t.Errorf("body was content-scanned: %v", got["body"])
	}
	// A count and a flag under a credential-like key are not credentials; a
	// STRING under a key that merely contains one is masked (fail closed).
	if got["tokens_count"] != 3 || got["authorized"] != true {
		t.Errorf("near-miss keys were masked: %+v", got)
	}
	if got["secret_note"] != "***" {
		t.Errorf("a string under a key containing secret = %v, want ***", got["secret_note"])
	}
	if got["Authorization"] != "***" {
		t.Errorf("exact Authorization = %v, want ***", got["Authorization"])
	}
}

func TestRedactItemsDoesNotMutateInput(t *testing.T) {
	items := []map[string]any{{"token": "sk-1", "nested": map[string]any{"api_key": "k"}}}
	_ = RedactItems(items)
	if items[0]["token"] != "sk-1" || items[0]["nested"].(map[string]any)["api_key"] != "k" {
		t.Fatalf("RedactItems mutated its input: %+v", items)
	}
}

func TestRedactItemsNil(t *testing.T) {
	if got := RedactItems(nil); got != nil {
		t.Errorf("RedactItems(nil) = %v, want nil", got)
	}
}

func TestRedactAndTruncateItemsOrderAndShape(t *testing.T) {
	small := Item{JSON: map[string]any{"token": "sk-1", "note": "hi"}}
	big := Item{JSON: map[string]any{"token": "sk-2", "blob": strings.Repeat("x", MaxOutputItemBytes)}}

	got := RedactAndTruncateItems([]Item{small, big})
	if len(got) != 2 {
		t.Fatalf("item count must be preserved, got %d", len(got))
	}
	if got[0].JSON["token"] != "***" || got[0].JSON["note"] != "hi" {
		t.Errorf("small redacted item = %+v", got[0].JSON)
	}
	if truncated, _ := got[1].JSON["truncated"].(bool); !truncated {
		t.Errorf("oversized item must still be truncated after redaction: %+v", got[1].JSON)
	}
	if got[1].JSON["token"] != nil {
		t.Errorf("truncated stub should not carry the original token: %+v", got[1].JSON)
	}
}

func TestTruncateItemsMarshalEquivalence(t *testing.T) {
	small := Item{JSON: map[string]any{"k": "v"}}
	big := Item{JSON: map[string]any{"k": strings.Repeat("x", MaxOutputItemBytes+1)}}
	got := TruncateItems([]Item{small, big})
	b, err := json.Marshal(got[0])
	if err != nil || len(b) > MaxOutputItemBytes {
		t.Errorf("small item must pass through: %v %d", err, len(b))
	}
	if got[1].JSON["truncated"] != true {
		t.Errorf("big item must be replaced by stub: %+v", got[1].JSON)
	}
	if ob, _ := got[1].JSON["original_bytes"].(int); ob <= MaxOutputItemBytes {
		t.Errorf("original_bytes = %v, want > %d", got[1].JSON["original_bytes"], MaxOutputItemBytes)
	}
}

// cyclicItems builds one item whose map contains a direct self-reference
// and an array that contains itself — the exact shape goja's Export()
// produces for a core.code result like `const o = {}; o.self = o; [o]`.
func cyclicItems() []Item {
	m := map[string]any{"name": "root", "password": "hunter2"}
	m["self"] = m
	arr := []any{nil}
	arr[0] = arr
	m["loop"] = arr
	return []Item{{JSON: m}}
}

func TestRedactItemsCyclicNoCrash(t *testing.T) {
	got := RedactItems([]map[string]any{cyclicItems()[0].JSON})
	if len(got) != 1 {
		t.Fatalf("item count changed: %d", len(got))
	}
	// The whole cyclic item must marshal now — the cycle was cut.
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("redacted cyclic item still unmarshalable: %v", err)
	}
	b, _ := json.Marshal(got[0])
	if !strings.Contains(string(b), CycleMarker) {
		t.Errorf("cycle marker missing from redacted output: %s", b)
	}
	if !strings.Contains(string(b), "***") || strings.Contains(string(b), "hunter2") {
		t.Errorf("sensitive value leaked or redaction missing: %s", b)
	}
}

func TestRedactItemsDepthGuard(t *testing.T) {
	// A deeply (but not cyclically) nested acyclic chain longer than
	// MaxRedactDepth must be cut by the depth guard, not recurse forever.
	leaf := map[string]any{"k": 1}
	cur := leaf
	for i := 0; i < MaxRedactDepth+10; i++ {
		cur = map[string]any{"child": cur}
	}
	got := RedactItems([]map[string]any{cur})
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("depth-capped item must marshal: %v", err)
	}
	if !strings.Contains(string(b), CycleMarker) {
		t.Errorf("depth guard did not fire: %s", b)
	}
}

func TestTruncateItemsCyclicNoCrash(t *testing.T) {
	got := TruncateItems(cyclicItems())
	if len(got) != 1 {
		t.Fatalf("item count changed: %d", len(got))
	}
	if truncated, _ := got[0].JSON["truncated"].(bool); !truncated {
		t.Fatalf("cyclic item must become a truncation stub: %+v", got[0].JSON)
	}
	note, _ := got[0].JSON["note"].(string)
	if !strings.Contains(note, CycleMarker) {
		t.Errorf("cycle marker missing from stub note: %q", note)
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("stub must be marshalable: %v", err)
	}
}

func TestRedactAndTruncateItemsCyclicNoCrash(t *testing.T) {
	got := RedactAndTruncateItems(cyclicItems())
	if len(got) != 1 {
		t.Fatalf("item count changed: %d", len(got))
	}
	// Redaction runs first and cuts the cycle, so the pipeline output is
	// fully marshalable and carries the marker.
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("pipeline output must marshal: %v", err)
	}
	if !strings.Contains(string(b), CycleMarker) {
		t.Errorf("cycle marker missing from pipeline output: %s", b)
	}
}

func TestRedactItemsSharedSubtreeNotACycle(t *testing.T) {
	// A DAG (same subtree referenced twice) is NOT a cycle: both
	// occurrences must be redacted normally, no marker.
	shared := map[string]any{"note": "x", "token": "sk-1"}
	items := []map[string]any{{
		"a":    shared,
		"b":    shared,
		"list": []any{shared, shared},
	}}
	got := RedactItems(items)[0]
	for _, loc := range []string{"a", "b"} {
		m := got[loc].(map[string]any)
		if m["token"] != "***" || m["note"] != "x" {
			t.Errorf("%s = %v, want redacted copy", loc, m)
		}
	}
	for _, e := range got["list"].([]any) {
		m := e.(map[string]any)
		if m["token"] != "***" {
			t.Errorf("list element = %v, want redacted copy", m)
		}
	}
	if a := got["a"].(map[string]any); a["note"] != "x" {
		t.Errorf("shared subtree content altered: %v", a)
	}
}

func TestRedactItemsMasksCredentialHeadersBySubstring(t *testing.T) {
	items := []map[string]any{{"headers": map[string]any{
		"Set-Cookie": "sid=1", "X-Api-Key": "k", "Proxy-Authorization": "Basic x", "x-auth-token": "t", "db_password": "p", "Content-Type": "text/html"},
		"usage": map[string]any{"total_tokens": 7}, "max_tokens": 5}}
	got := RedactItems(items)[0]
	h := got["headers"].(map[string]any)
	for _, k := range []string{"Set-Cookie", "X-Api-Key", "Proxy-Authorization", "x-auth-token", "db_password"} {
		if h[k] != "***" {
			t.Errorf("%s = %v, want ***", k, h[k])
		}
	}
	if h["Content-Type"] != "text/html" || got["usage"].(map[string]any)["total_tokens"] != 7 || got["max_tokens"] != 5 {
		t.Errorf("ordinary output changed: %+v", got)
	}
}
