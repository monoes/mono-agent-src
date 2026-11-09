package nodes

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/workflow"
)

// formKeyFeeds is what one node form field sets for the action, as
// BrowserNode.Execute maps config: "targets" becomes selectedListItems,
// "message" the message variables, "keywords" the keyword variables and
// "limit" maxResultsCount. Every other key is passed through as itself.
// The "username" field is the browser session, so it feeds no input.
var formKeyFeeds = map[string][]string{
	"targets":  {"targets", "selectedListItems"},
	"message":  {"message", "messageText", "contentMessage", "commentText", "replyText", "text"},
	"keywords": {"keywords", "keyword"},
	"limit":    {"limit", "maxResultsCount"},
	"username": nil,
}

// requiredInput is one required input of an action: its name, aliases,
// and whether it declares a default (then the user need not supply it).
type requiredInput struct {
	Name       string
	Aliases    []string
	HasDefault bool
}

func requiredInputs(def *action.ActionDef) []requiredInput {
	var out []requiredInput
	if def.Inputs == nil {
		return nil
	}
	for _, raw := range def.Inputs.Required {
		var name string
		if json.Unmarshal(raw, &name) == nil {
			out = append(out, requiredInput{Name: name})
			continue
		}
		var obj struct {
			Name    string          `json:"name"`
			Aliases []string        `json:"aliases"`
			Default json.RawMessage `json:"default"`
		}
		if json.Unmarshal(raw, &obj) == nil && obj.Name != "" {
			out = append(out, requiredInput{Name: obj.Name, Aliases: obj.Aliases,
				HasDefault: len(obj.Default) > 0 && string(obj.Default) != "null"})
		}
	}
	return out
}

// TestBuiltinForms_RequiredInputsAreRequiredFields: every required input of
// every built-in action is set by a required field of the node's resolved
// form (directly, through the node-facing names above, or through one of the
// input's declared aliases). A field's default doesn't count: only the
// editor pre-fills it, and a caller that sends just the required fields
// (CLI, MCP, API) would fail. Such an input belongs among the action's
// optional inputs, with its default.
func TestBuiltinForms_RequiredInputsAreRequiredFields(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	action.SetDefSource(nil)

	for _, nt := range browserNodeTypes() {
		dot := strings.Index(nt, ".")
		def, err := action.GetLoader().Load(nt[:dot], nt[dot+1:])
		if err != nil {
			t.Errorf("%s: %v", nt, err)
			continue
		}
		schema, err := workflow.LoadDefaultSchema(nt)
		if err != nil {
			t.Errorf("%s: %v", nt, err)
			continue
		}
		fed := map[string]bool{} // input name → set by a required field
		for _, f := range schema.Fields {
			// Only a field the user must fill counts: a default is the
			// editor's pre-fill, and CLI/MCP/API callers never send it.
			if !f.Required {
				continue
			}
			feeds, mapped := formKeyFeeds[f.Key]
			if !mapped {
				feeds = []string{f.Key}
			}
			for _, n := range feeds {
				fed[n] = true
			}
		}
		for _, in := range requiredInputs(def) {
			ok := fed[in.Name] // a required input's own default isn't applied at run time
			for _, a := range in.Aliases {
				ok = ok || fed[a]
			}
			if !ok {
				t.Errorf("%s: required input %q has no required form field", nt, in.Name)
			}
		}
	}
}

// TestBrowserForms_HaveSessionPlatform: every browser automation node's
// form names its session platform, so the editor offers the session
// picker (built-ins, and an installed package via the registry).
func TestBrowserForms_HaveSessionPlatform(t *testing.T) {
	check := func(label string, want func(nt string) string) {
		t.Helper()
		for _, nt := range browserNodeTypes() {
			s, err := workflow.LoadDefaultSchema(nt)
			if err != nil {
				t.Errorf("%s: %s: %v", label, nt, err)
				continue
			}
			w := want(nt)
			if s.CredentialPlatform == nil || *s.CredentialPlatform != w {
				got := "<nil>"
				if s.CredentialPlatform != nil {
					got = *s.CredentialPlatform
				}
				t.Errorf("%s: %s credential_platform = %s, want %s", label, nt, got, w)
			}
			if raw, ok := workflow.ReadEmbeddedSchema(nt); ok {
				var m map[string]any
				if json.Unmarshal(raw, &m) != nil || m["credential_platform"] != w {
					t.Errorf("%s: %s node schema JSON credential_platform = %v, want %s", label, nt, m["credential_platform"], w)
				}
			}
		}
	}
	prefix := func(nt string) string { return nt[:strings.Index(nt, ".")] }

	home := t.TempDir()
	testhome.SetHome(t, home)
	action.SetDefSource(nil)
	check("legacy", prefix)

	reg, err := BootAutomations(filepath.Join(home, ".monoagent"))
	if err != nil {
		t.Fatalf("BootAutomations: %v", err)
	}
	t.Cleanup(func() { action.SetDefSource(nil) })
	src := filepath.Join(t.TempDir(), "acme-crm")
	for path, body := range map[string]string{
		"automation.json": `{"schema":"monoagent.automation/v1","id":"acme-crm","name":"Acme CRM","version":"1.0.0",
		  "site":{"startUrl":"https://app.acme-crm.com/","domains":["app.acme-crm.com"]},
		  "permissions":{"steps":["navigate"],"scripts":[],"downloads":false},
		  "actions":["list_deals"],"policy":{"tier":"standard"}}`,
		"actions/list_deals.json": `{"actionType":"list_deals","automation":"acme-crm","sideEffects":"read",
		  "steps":[{"id":"open","type":"navigate","url":"https://app.acme-crm.com/deals"}]}`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := reg.Install(src, automation.InstallOptions{Source: automation.SourceLocal}); err != nil || !res.Installed {
		t.Fatalf("Install: %v %+v", err, res)
	}
	check("registry", prefix)

	// Not a browser node: left alone.
	if s, _ := workflow.LoadDefaultSchema("core.if"); s.CredentialPlatform != nil {
		t.Errorf("core.if got credential_platform %q", *s.CredentialPlatform)
	}
}
