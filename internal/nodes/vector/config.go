// Package vector holds the retrieval-augmented-generation (RAG) node types:
// vector.embed (text → embedding vectors), vector.split_text (chunking),
// vector.load_document (items or files → documents) and vector.store
// (insert / search / update / delete against a vector database).
//
// n8n wires the equivalent LangChain nodes together as sub-nodes (an
// embedding node hangs off a vector store node through an ai_embedding
// connection). Mono has no sub-node concept, so every node here is flat:
// vector.store carries its embedding settings inline under "embedding",
// or reads vectors that an upstream vector.embed already computed.
package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// httpClient is shared by every REST provider. Tests swap base URLs, not
// the client.
var httpClient = &http.Client{Timeout: 120 * time.Second}

// Response-size caps, matching internal/nodes/service/helpers.go: no
// provider response is read unbounded, and an error body echoed into a
// message is capped tighter.
const (
	maxResponseBody  = 64 << 20
	maxErrorBodyEcho = 64 << 10
)

func errorBodyEcho(b []byte) string {
	if len(b) > maxErrorBodyEcho {
		return string(b[:maxErrorBodyEcho]) + fmt.Sprintf("…(%d bytes truncated)", len(b)-maxErrorBodyEcho)
	}
	return string(b)
}

// doJSON sends body (JSON-encoded unless nil) and decodes a 2xx JSON
// response into out (skipped when out is nil or the body is empty).
// Header values are set verbatim. A 404 is reported as *httpStatusError so
// callers can branch on "missing collection".
func doJSON(ctx context.Context, method, url string, headers map[string]string, body, out interface{}) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{Status: resp.StatusCode, Body: errorBodyEcho(data)}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w (body: %s)", err, errorBodyEcho(data))
	}
	return nil
}

// httpStatusError is a non-2xx response.
type httpStatusError struct {
	Status int
	Body   string
}

func (e *httpStatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

func isNotFound(err error) bool {
	se, ok := err.(*httpStatusError)
	return ok && se.Status == http.StatusNotFound
}

// ── typed config readers ────────────────────────────────────────────────────

func str(cfg map[string]interface{}, key string) string {
	switch v := cfg[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	}
	return ""
}

// strOr returns the first non-empty value among keys, else def.
func strOr(cfg map[string]interface{}, def string, keys ...string) string {
	for _, k := range keys {
		if s := str(cfg, k); s != "" {
			return s
		}
	}
	return def
}

// intField reads a non-negative integer. A string holding a number is
// accepted (expression results arrive as strings). Absent → def.
func intField(cfg map[string]interface{}, key string, def int) (int, error) {
	v, ok := cfg[key]
	if !ok || v == nil || v == "" {
		return def, nil
	}
	var n int
	switch t := v.(type) {
	case float64:
		n = int(t)
	case int:
		n = t
	case int64:
		n = int(t)
	case string:
		p, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, fmt.Errorf("%s must be a whole number, got %q", key, t)
		}
		n = p
	default:
		return 0, fmt.Errorf("%s must be a number, got %T", key, v)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must not be negative, got %d", key, n)
	}
	return n, nil
}

func boolField(cfg map[string]interface{}, key string, def bool) bool {
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return def
}

// stringList reads a list given either as a JSON array or as a
// comma-separated string.
func stringList(cfg map[string]interface{}, key string) []string {
	var out []string
	switch v := cfg[key].(type) {
	case []interface{}:
		for _, e := range v {
			if s := strings.TrimSpace(fmt.Sprint(e)); s != "" && e != nil {
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// jsonValue reads a field that may hold a JSON object/array/string either
// natively or as JSON text. A plain string that does not parse as JSON is
// returned as-is (some providers take filter expressions as strings).
func jsonValue(cfg map[string]interface{}, key string) interface{} {
	v, ok := cfg[key]
	if !ok || v == nil {
		return nil
	}
	s, isStr := v.(string)
	if !isStr {
		return v
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		var parsed interface{}
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			return parsed
		}
	}
	return s
}

// lookupPath reads a dotted path ("a.b.c") from an item's JSON.
func lookupPath(m map[string]interface{}, path string) (interface{}, bool) {
	cur := interface{}(m)
	for _, part := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur, ok = mm[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// toFloats converts a JSON array of numbers into []float64.
func toFloats(v interface{}) ([]float64, bool) {
	switch t := v.(type) {
	case []float64:
		return t, len(t) > 0
	case []float32:
		out := make([]float64, len(t))
		for i, f := range t {
			out[i] = float64(f)
		}
		return out, len(out) > 0
	case []interface{}:
		out := make([]float64, 0, len(t))
		for _, e := range t {
			switch n := e.(type) {
			case float64:
				out = append(out, n)
			case int:
				out = append(out, float64(n))
			default:
				return nil, false
			}
		}
		return out, len(out) > 0
	}
	return nil, false
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

func copyMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m)+4)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func trimSlash(s string) string { return strings.TrimRight(s, "/") }

// perItemString evaluates a held-back (PerItemConfigFields) template
// against one item. Plain strings pass through unchanged.
func perItemString(tmpl string, item workflow.Item, in workflow.NodeInput) (string, error) {
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}
	return workflow.NewExpressionEngine().EvaluateString(tmpl, workflow.ExpressionContext{
		JSON: item.JSON, Node: in.NodeOutputs, WorkflowID: in.WorkflowID, ExecutionID: in.ExecutionID,
	})
}
