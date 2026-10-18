package vault

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/storage"
	vaultstore "github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// newSecretsTestCtx mirrors internal/secrets's own newSecretsTestDB helper
// (keyring.MockInit + a real migrated DB), additionally wrapping ctx with
// the vault.DB/ProfileID values the engine normally injects before Execute
// runs (see internal/workflow/engine.go's runExecution).
func newSecretsTestCtx(t *testing.T) context.Context {
	t.Helper()
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "secrets-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })

	ctx := vaultstore.ContextWithDB(context.Background(), db.DB)
	ctx = vaultstore.ContextWithProfileID(ctx, "default")
	return ctx
}

func TestSecretSaveGet_RoundTrip(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	input := workflow.NodeInput{
		WorkflowID:  "wf1",
		ExecutionID: "ex1",
		Items:       []workflow.Item{workflow.NewItem(map[string]interface{}{"api_key": "sk-live-abc123"})},
	}

	save := &SecretSaveNode{}
	outputs, err := save.Execute(ctx, input, map[string]interface{}{
		"kind":       "secret",
		"name":       "test-credential",
		"field_keys": []interface{}{"api_key"},
	})
	if err != nil {
		t.Fatalf("SecretSaveNode.Execute: %v", err)
	}
	if len(outputs) != 1 || len(outputs[0].Items) != 1 {
		t.Fatalf("unexpected outputs: %+v", outputs)
	}
	vaultID, _ := outputs[0].Items[0].JSON["vault_id"].(string)
	if vaultID == "" {
		t.Fatalf("expected a vault_id in output, got %+v", outputs[0].Items[0].JSON)
	}

	get := &SecretGetNode{}
	outputs, err = get.Execute(ctx, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{})}},
		map[string]interface{}{"name": "test-credential"})
	if err != nil {
		t.Fatalf("SecretGetNode.Execute: %v", err)
	}
	cred, ok := outputs[0].Items[0].JSON["credential"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected a credential object in output, got %+v", outputs[0].Items[0].JSON)
	}
	if cred["api_key"] != "sk-live-abc123" {
		t.Fatalf("credential[api_key] = %v, want sk-live-abc123", cred["api_key"])
	}
}

// TestSecretSave_AcceptsBothFieldKeysRepresentations is a regression test
// covering two related historical bugs:
//
//  1. The real workflow engine auto-parses any config string starting with
//     "[" or "{" into a native Go value before Execute ever sees it
//     (ExpressionEngine.resolveValue) — so field_keys arrives as
//     []interface{} there, never the raw JSON string a user typed into the
//     field_keys textarea. An earlier version of this node read
//     config["field_keys"] as a string and re-unmarshaled it, which failed
//     every real workflow run while passing every hand-written unit test
//     that (wrongly) passed a literal string.
//  2. `monoagentcli node run` invokes a node directly with the raw --config
//     JSON, bypassing that engine-level auto-parse entirely — so the exact
//     same stored-shape string (`field_keys: "[\"token\"]"`) arrives as a
//     literal string there instead. A version of this node that only
//     accepted []interface{} (the fix for bug 1) then failed under
//     `node run` specifically, which is a documented, legitimate way to
//     invoke any node type directly.
//
// parseFieldKeys must accept both shapes so the node behaves the same
// regardless of which caller invokes it.
func TestSecretSave_AcceptsBothFieldKeysRepresentations(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	input := workflow.NodeInput{
		Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"token": "t1"})},
	}
	save := &SecretSaveNode{}

	// The shape the real engine delivers.
	if _, err := save.Execute(ctx, input, map[string]interface{}{
		"name":       "regression-parsed-array",
		"field_keys": []interface{}{"token"},
	}); err != nil {
		t.Fatalf("Execute with []interface{} field_keys: %v", err)
	}

	// The shape `node run` delivers (and what a saved workflow's own JSON
	// literally contains, before the engine ever touches it).
	if _, err := save.Execute(ctx, input, map[string]interface{}{
		"name":       "regression-raw-string",
		"field_keys": `["token"]`,
	}); err != nil {
		t.Fatalf("Execute with a raw JSON string field_keys: %v", err)
	}
}

func TestSecretSave_RejectsNonStringFieldKeyElement(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	save := &SecretSaveNode{}
	_, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"api_key": "v"})}},
		map[string]interface{}{"name": "n", "field_keys": []interface{}{"api_key", 42.0}})
	if err == nil {
		t.Fatalf("expected an error when field_keys contains a non-string element")
	}
}

func TestSecretSave_RejectsMalformedFieldKeysString(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	save := &SecretSaveNode{}
	_, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"token": "t1"})}},
		map[string]interface{}{"name": "n", "field_keys": `[not valid json`})
	if err == nil {
		t.Fatalf("expected an error for a malformed field_keys JSON string")
	}
}

func TestSecretSave_RejectsMissingItemField(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	save := &SecretSaveNode{}
	_, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{})}},
		map[string]interface{}{"name": "n", "field_keys": []interface{}{"missing_field"}})
	if err == nil {
		t.Fatalf("expected an error when the item has no field_keys field")
	}
}

func TestSecretSave_RejectsInvalidKind(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	save := &SecretSaveNode{}
	_, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"x": "y"})}},
		map[string]interface{}{"kind": "connection", "name": "n", "field_keys": []interface{}{"x"}})
	if err == nil {
		t.Fatalf("expected an error for kind=connection (not user-creatable)")
	}
}

func TestSecretGet_UnknownNameFails(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	get := &SecretGetNode{}
	_, err := get.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{})}},
		map[string]interface{}{"name": "does-not-exist"})
	if err == nil {
		t.Fatalf("expected an error for an unknown secret name")
	}
}

// TestSecretSave_RejectsNullFieldValue covers a real silent-corruption risk:
// a present-but-null item field (e.g. {"api_key": null}) used to pass the
// `ok` check in a plain map lookup, fail the string type-assertion, and
// fall through to fmt.Sprint(nil) — silently storing the literal string
// "<nil>" as the encrypted value instead of failing loudly.
func TestSecretSave_RejectsNullFieldValue(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	save := &SecretSaveNode{}
	_, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"api_key": nil})}},
		map[string]interface{}{"name": "null-field-test", "field_keys": []interface{}{"api_key"}})
	if err == nil {
		t.Fatalf("expected an error for a null field value, not silent storage of \"<nil>\"")
	}
}

// TestSecretSave_EncodesStructuredFieldValueAsJSON covers re-saving a
// structured value (e.g. the "credential" object vault.secret_get itself
// produces) — fmt.Sprint on a map produces Go's %v syntax (map[k:v]), not
// valid JSON, silently corrupting the value on any round trip through
// vault.secret_get -> vault.secret_save -> vault.secret_get.
func TestSecretSave_EncodesStructuredFieldValueAsJSON(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	save := &SecretSaveNode{}
	structured := map[string]interface{}{"inner": "value", "n": 3.0}
	outputs, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"blob": structured})}},
		map[string]interface{}{"name": "structured-field-test", "field_keys": []interface{}{"blob"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	vaultID, _ := outputs[0].Items[0].JSON["vault_id"].(string)

	get := &SecretGetNode{}
	outputs, err = get.Execute(ctx, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{})}},
		map[string]interface{}{"name": "structured-field-test"})
	if err != nil {
		t.Fatalf("SecretGetNode.Execute: %v", err)
	}
	cred := outputs[0].Items[0].JSON["credential"].(map[string]interface{})
	var roundTripped map[string]interface{}
	if err := json.Unmarshal([]byte(cred["blob"].(string)), &roundTripped); err != nil {
		t.Fatalf("stored value is not valid JSON: %v (value: %q)", err, cred["blob"])
	}
	if roundTripped["inner"] != "value" || roundTripped["n"] != 3.0 {
		t.Fatalf("round-tripped value = %+v, want %+v", roundTripped, structured)
	}
	_ = vaultID
}

// TestSecretSave_BatchErrorNamesTheFailingItem covers a static "name" fed
// more than one item: secrets.Add's unique(profile_id, name) constraint
// means only the first item can ever succeed, so the error for item 2+
// should be traceable to which item failed, not just a bare secrets.Add
// error indistinguishable from any other failure.
func TestSecretSave_BatchErrorNamesTheFailingItem(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	save := &SecretSaveNode{}
	_, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{
			workflow.NewItem(map[string]interface{}{"api_key": "v1"}),
			workflow.NewItem(map[string]interface{}{"api_key": "v2"}),
		}},
		map[string]interface{}{"name": "batch-collision-test", "field_keys": []interface{}{"api_key"}})
	if err == nil {
		t.Fatalf("expected an error: a static name cannot be reused for a second item")
	}
	if !strings.Contains(err.Error(), "item 1") {
		t.Fatalf("error should identify the failing item (index 1), got: %v", err)
	}
}

// TestSecretGet_AppliesSameCredentialToEveryBatchItem covers multi-item
// execution for the get side — SecretGetNode intentionally decrypts once
// (name is a fixed config value, not per-item data) and reuses the result
// across all items; this locks in that every item in the batch actually
// receives it, not just the first.
func TestSecretGet_AppliesSameCredentialToEveryBatchItem(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	save := &SecretSaveNode{}
	if _, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"api_key": "shared-value"})}},
		map[string]interface{}{"name": "batch-get-test", "field_keys": []interface{}{"api_key"}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	get := &SecretGetNode{}
	outputs, err := get.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{
			workflow.NewItem(map[string]interface{}{"n": 1.0}),
			workflow.NewItem(map[string]interface{}{"n": 2.0}),
		}},
		map[string]interface{}{"name": "batch-get-test"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(outputs[0].Items) != 2 {
		t.Fatalf("expected 2 output items, got %d", len(outputs[0].Items))
	}
	for i, item := range outputs[0].Items {
		cred, ok := item.JSON["credential"].(map[string]interface{})
		if !ok || cred["api_key"] != "shared-value" {
			t.Fatalf("item %d: credential = %+v, want api_key=shared-value", i, item.JSON["credential"])
		}
	}
}

// Sanity check that the two node types are wired into a registry the same
// way every other node package is.
func TestRegisterAll(t *testing.T) {
	r := workflow.NewNodeTypeRegistry()
	RegisterAll(r)
	for _, typ := range []string{"vault.secret_save", "vault.secret_get"} {
		if _, ok := r.Get(typ); !ok {
			t.Errorf("registry missing %q", typ)
		}
	}
}

// A workflow the user hasn't approved (the chat's, an agent's, one another
// workflow changed) reads no vault entry: its next step could send the value
// anywhere, and its output would show it to whoever started it. Once
// approved, it reads as before.
func TestSecretGetRefusedInAnUnapprovedRun(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	input := workflow.NodeInput{WorkflowID: "wf1", ExecutionID: "ex1", Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"api_key": "sk-live-abc123"})}}
	if _, err := (&SecretSaveNode{}).Execute(ctx, input, map[string]interface{}{"kind": "secret", "name": "kept", "field_keys": []interface{}{"api_key"}}); err != nil {
		t.Fatal(err)
	}
	get := &SecretGetNode{}
	out, err := get.Execute(workflow.WithAIAuthored(ctx), input, map[string]interface{}{"name": "kept"})
	if err == nil || !strings.Contains(err.Error(), "which this run may not") || out != nil {
		t.Fatalf("an unapproved run's vault.secret_get: %v %v", out, err)
	}
	if _, err := get.Execute(ctx, input, map[string]interface{}{"name": "kept"}); err != nil {
		t.Fatalf("an approved run's vault.secret_get: %v", err)
	}
}
