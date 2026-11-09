package nodes

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/workflow"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestInstalledPackage_FormsAndNodes is the end-to-end path for an imported
// package: install it into a real registry, then its actions become nodes,
// a forms/<action>.json replaces the generated form, and an action without
// one gets a form generated from its inputs.
func TestInstalledPackage_FormsAndNodes(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	reg, err := BootAutomations(filepath.Join(home, ".monoagent"))
	t.Cleanup(func() { action.SetDefSource(nil) })
	if err != nil {
		t.Fatalf("BootAutomations: %v", err)
	}

	src := filepath.Join(t.TempDir(), "acme-crm")
	writeFile(t, filepath.Join(src, "automation.json"), `{
	  "schema": "monoagent.automation/v1",
	  "id": "acme-crm", "name": "Acme CRM", "version": "1.0.0",
	  "site": {"startUrl": "https://app.acme-crm.com/", "domains": ["app.acme-crm.com"]},
	  "permissions": {"steps": ["navigate", "type"], "scripts": [], "downloads": false},
	  "actions": ["create_contact", "list_deals"],
	  "policy": {"tier": "standard"}
	}`)
	writeFile(t, filepath.Join(src, "actions", "create_contact.json"), `{
	  "actionType": "create_contact", "automation": "acme-crm", "sideEffects": "write",
	  "inputs": {"required": [{"name": "email", "type": "string"}]},
	  "steps": [{"id": "open", "type": "navigate", "url": "https://app.acme-crm.com/contacts/new"},
	            {"id": "email", "type": "type", "selector": "#email", "value": "{{email}}", "sideEffect": true}]
	}`)
	writeFile(t, filepath.Join(src, "actions", "list_deals.json"), `{
	  "actionType": "list_deals", "automation": "acme-crm", "sideEffects": "read",
	  "inputs": {"optional": [{"name": "stage", "type": "string", "enum": ["open", "won"]}]},
	  "steps": [{"id": "open", "type": "navigate", "url": "https://app.acme-crm.com/deals"}]
	}`)
	writeFile(t, filepath.Join(src, "forms", "create_contact.json"),
		`{"credential_platform":null,"fields":[{"key":"email","label":"Work email","type":"text","required":true}]}`)

	res, err := reg.Install(src, automation.InstallOptions{Source: automation.SourceLocal})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !res.Installed {
		t.Fatalf("not installed: %+v", res)
	}

	r := workflow.NewNodeTypeRegistry()
	RegisterBrowserNodes(r)
	for _, nt := range []string{"acme-crm.create_contact", "acme-crm.list_deals"} {
		if !r.Has(nt) {
			t.Errorf("node %s not registered", nt)
		}
	}

	s, err := workflow.LoadDefaultSchema("acme-crm.create_contact")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Fields) != 1 || s.Fields[0].Label != "Work email" {
		t.Errorf("forms/create_contact.json not used: %+v", s.Fields)
	}
	s, _ = workflow.LoadDefaultSchema("acme-crm.list_deals")
	var stage *workflow.NodeSchemaField
	for i := range s.Fields {
		if s.Fields[i].Key == "stage" {
			stage = &s.Fields[i]
		}
	}
	if stage == nil || stage.Type != "select" || len(stage.Options) != 2 {
		t.Errorf("generated list_deals form: %+v", s.Fields)
	}
	if s.Fields[0].Key != "username" {
		t.Errorf("generated form should start with the session field: %+v", s.Fields)
	}
}
