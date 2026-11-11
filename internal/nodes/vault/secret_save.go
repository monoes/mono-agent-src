package vault

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/secrets"
	vaultstore "github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// copyMap shallow-copies an item's JSON map before mutating it, matching the
// convention every other node package uses (see internal/nodes/image/image.go).
func copyMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// parseFieldKeys accepts field_keys in either shape it can legitimately
// arrive in:
//
//   - []interface{} — what the real workflow engine delivers: it
//     auto-parses any config string starting with "[" or "{" into a native
//     Go value before Execute ever runs (ExpressionEngine.resolveValue in
//     internal/workflow/expression.go), so a workflow-stored JSON-array
//     string like `["api_key"]` arrives already parsed.
//   - string — what `monoagentcli node run` delivers: it invokes a node
//     directly with the raw --config JSON, bypassing the engine's
//     expression-resolution pass entirely, so the exact same
//     `field_keys: "[\"api_key\"]"` value a saved workflow would contain
//     arrives as a literal string instead.
//
// Accepting both means this node behaves the same regardless of which
// caller invokes it, rather than silently depending on one specific
// caller's preprocessing.
func parseFieldKeys(raw interface{}) ([]string, error) {
	var arr []interface{}
	switch v := raw.(type) {
	case []interface{}:
		arr = v
	case string:
		if err := json.Unmarshal([]byte(v), &arr); err != nil {
			return nil, fmt.Errorf("field_keys must be a JSON array of item field names: %w", err)
		}
	default:
		return nil, fmt.Errorf("field_keys must be a non-empty JSON array of item field names")
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("field_keys must be a non-empty JSON array of item field names")
	}
	keys := make([]string, len(arr))
	for i, k := range arr {
		s, ok := k.(string)
		if !ok {
			return nil, fmt.Errorf("field_keys[%d] must be a string", i)
		}
		keys[i] = s
	}
	return keys, nil
}

// SecretSaveNode writes a credential into the profile's encrypted vault
// (~/.monoagent — see internal/secrets), pulling the field values to encrypt
// out of the current item's own JSON data (e.g. a token a prior node in the
// same workflow just obtained). It never receives or logs plaintext itself
// beyond what secrets.Add already handles.
type SecretSaveNode struct{}

func (n *SecretSaveNode) Type() string { return "vault.secret_save" }

// PublishesToThirdParty: vault.secret_save sends nothing off this machine (issue #1418).
func (SecretSaveNode) PublishesToThirdParty() bool { return false }

func (n *SecretSaveNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	kind, _ := config["kind"].(string)
	if kind == "" {
		kind = "secret"
	}
	if kind != "secret" && kind != "login" {
		return nil, fmt.Errorf("vault.secret_save: kind must be \"secret\" or \"login\", got %q", kind)
	}
	name, _ := config["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("vault.secret_save: name is required")
	}
	fieldKeys, err := parseFieldKeys(config["field_keys"])
	if err != nil {
		return nil, fmt.Errorf("vault.secret_save: %w", err)
	}
	username, _ := config["username"].(string)
	url, _ := config["url"].(string)
	notes, _ := config["notes"].(string)
	outputField, _ := config["output_field"].(string)
	if outputField == "" {
		outputField = "vault_id"
	}

	db := vaultstore.DBFromContext(ctx)
	if db == nil {
		return nil, fmt.Errorf("vault.secret_save: no database in context")
	}
	profileID, perr := vaultstore.ProfileIDFromContext(ctx)
	if perr != nil {
		return nil, fmt.Errorf("vault.secret_save: ran without a profile: %w", perr)
	}

	outItems := make([]workflow.Item, 0, len(input.Items))
	for itemIdx, item := range input.Items {
		newJSON := copyMap(item.JSON)

		fields := make(map[string]string, len(fieldKeys))
		for _, key := range fieldKeys {
			v, ok := item.JSON[key]
			// A present-but-null field (ok=true, v=nil) is just as unusable
			// as a missing one: without this check, v.(string) below fails
			// silently and fmt.Sprint(nil) would have stored the literal
			// string "<nil>" as the secret's field value.
			if !ok || v == nil {
				return nil, fmt.Errorf("vault.secret_save: item %d has no field %q", itemIdx, key)
			}
			if s, ok := v.(string); ok {
				fields[key] = s
				continue
			}
			// Numbers/bools round-trip fine as their JSON text (e.g. "42",
			// "true"). Anything structured (map/slice — e.g. re-saving a
			// vault.secret_get "credential" object under one field) must go
			// through json.Marshal too: fmt.Sprint would produce Go's
			// %v-style map[k:v] syntax, not valid JSON, silently corrupting
			// the value on any round trip.
			encoded, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("vault.secret_save: item %d field %q: %w", itemIdx, key, err)
			}
			fields[key] = string(encoded)
		}

		id, err := secrets.Add(ctx, db, profileID, kind, name, fields, username, url, notes)
		if err != nil {
			return nil, fmt.Errorf("vault.secret_save: item %d: %w", itemIdx, err)
		}
		newJSON[outputField] = id
		outItems = append(outItems, workflow.Item{JSON: newJSON})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}
