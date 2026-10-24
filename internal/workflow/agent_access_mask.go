package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/secrets"
)

// Masking under the owner's agent access (`secret agent-access on`). A run
// of a workflow the user never approved may read vault secrets then, and a
// step could copy a value into its output under any field name (a rename, a
// Set node, a "value" field) that the key-name redaction does not know. What
// the run stores of its steps (inputs, outputs, errors) is therefore
// scrubbed of every secret value the run itself read, wherever it occurs.
// The values are the ones actually resolved in this run (secrets.MaskResolved),
// so nothing else is touched; one shorter than secrets.MinTrackedLen is not.

// maskingStore stores a run's step records with the run's resolved secret
// values masked. Every other call passes through.
type maskingStore struct {
	WorkflowStore
	run context.Context // the run's context, which knows the values read
}

// MaskAgentNodeOutputs applies the same value masking to standalone agent
// steps that a workflow's execution store applies to persisted outputs.
func MaskAgentNodeOutputs(ctx context.Context, outputs []NodeOutput) []NodeOutput {
	if len(secrets.ResolvedValues(ctx)) == 0 {
		return outputs
	}
	out := append([]NodeOutput(nil), outputs...)
	for i := range out {
		out[i].Items = maskItems(ctx, out[i].Items)
	}
	return out
}

// MaskAgentNodeError masks resolved values while retaining the error's type.
func MaskAgentNodeError(ctx context.Context, err error) error {
	return maskRunError(ctx, err)
}

func (m *maskingStore) CreateExecutionNode(ctx context.Context, en *WorkflowExecutionNode) error {
	c := maskedNode(m.run, en)
	err := m.WorkflowStore.CreateExecutionNode(ctx, c)
	if c != en && en != nil {
		en.ID = c.ID // the store names the row; the run goes on with that id
	}
	return err
}

func (m *maskingStore) UpdateExecutionNode(ctx context.Context, en *WorkflowExecutionNode) error {
	return m.WorkflowStore.UpdateExecutionNode(ctx, maskedNode(m.run, en))
}

func (m *maskingStore) SetExecutionNodeFinished(ctx context.Context, id, status string, out []Item, errMsg string) error {
	return m.WorkflowStore.SetExecutionNodeFinished(ctx, id, status, maskItems(m.run, out), secrets.MaskResolved(m.run, errMsg))
}

func (m *maskingStore) SetExecutionResumeAfter(ctx context.Context, id string, until *time.Time) error {
	if s, ok := m.WorkflowStore.(resumeAfterSetter); ok {
		return s.SetExecutionResumeAfter(ctx, id, until)
	}
	return nil
}

// maskedNode is a copy of en (the caller's keeps the real values, which the
// run goes on with) with its items and error masked.
func maskedNode(run context.Context, en *WorkflowExecutionNode) *WorkflowExecutionNode {
	if en == nil || len(secrets.ResolvedValues(run)) == 0 {
		return en
	}
	c := *en
	c.InputItems = maskItems(run, en.InputItems)
	c.OutputItems = maskItems(run, en.OutputItems)
	c.ErrorMessage = secrets.MaskResolved(run, en.ErrorMessage)
	return &c
}

// maskItems returns items with every secret value read in run replaced by
// "***" (in keys and values at any depth); items without one come back as
// they are.
func maskItems(run context.Context, items []Item) []Item {
	values := secrets.ResolvedValues(run)
	if len(values) == 0 || len(items) == 0 {
		return items
	}
	out := make([]Item, len(items))
	for i, it := range items {
		out[i] = Item{JSON: maskJSON(values, it.JSON), Binary: it.Binary}
	}
	return out
}

func maskJSON(values []string, m map[string]interface{}) map[string]interface{} {
	raw, err := json.Marshal(m)
	if err != nil {
		return map[string]interface{}{"masked": true, "note": "output could not be checked for secrets and was withheld"}
	}
	hit := false
	for _, v := range values {
		// A value as JSON escapes it: look for that form too.
		enc, _ := json.Marshal(v)
		if bytes.Contains(raw, []byte(v)) || bytes.Contains(raw, enc[1:len(enc)-1]) {
			hit = true
			break
		}
	}
	if !hit {
		return m
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic interface{}
	if err := dec.Decode(&generic); err != nil {
		return map[string]interface{}{"masked": true, "note": "output could not be checked for secrets and was withheld"}
	}
	out, _ := maskValue(values, generic).(map[string]interface{})
	return out
}

func maskValue(values []string, v interface{}) interface{} {
	switch t := v.(type) {
	case string:
		return maskString(values, t)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, x := range t {
			out[maskString(values, k)] = maskValue(values, x)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, x := range t {
			out[i] = maskValue(values, x)
		}
		return out
	}
	return v
}

func maskString(values []string, s string) string {
	for _, v := range values {
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

// maskedError is err with its text masked; it still unwraps to err.
type maskedError struct {
	err error
	msg string
}

func (e *maskedError) Error() string { return e.msg }
func (e *maskedError) Unwrap() error { return e.err }

// maskRunError masks a secret value read by the run out of the error it
// returns to its caller.
func maskRunError(run context.Context, err error) error {
	if err == nil || !secrets.UnderAgentAccess(run) {
		return err
	}
	if msg := secrets.MaskResolved(run, err.Error()); msg != err.Error() {
		return &maskedError{err: err, msg: msg}
	}
	return err
}
