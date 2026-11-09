package workflow

import (
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
)

// pkgSource is a DefSource serving fixed action JSON.
type pkgSource map[string]string

func (s pkgSource) Load(a, t string) ([]byte, error) {
	if raw, ok := s[a+"/"+t]; ok {
		return []byte(raw), nil
	}
	return nil, fmt.Errorf("not found: %s/%s", a, t)
}
func (s pkgSource) List() ([]string, error)                         { return nil, nil }
func (s pkgSource) Package(automation string) action.PackageContext { return nil }

// formPkg is a package context that serves forms/<action>.json.
type formPkg struct {
	action.PackageContext
	forms map[string]string
}

func (p formPkg) Form(name string) ([]byte, error) {
	if f, ok := p.forms[name]; ok {
		return []byte(f), nil
	}
	return nil, fmt.Errorf("no form %s", name)
}

type formSource struct {
	pkgSource
	pkg formPkg
}

func (s formSource) Package(a string) action.PackageContext {
	if a == "acme-crm" {
		return s.pkg
	}
	return nil
}

func TestLoadDefaultSchema_PackageFormOverride(t *testing.T) {
	action.SetDefSource(formSource{
		pkgSource: pkgSource{
			"acme-crm/create_contact": `{"actionType":"create_contact","inputs":{"required":[{"name":"email","type":"string"}]},"steps":[]}`,
			"acme-crm/list_deals":     `{"actionType":"list_deals","inputs":{"optional":[{"name":"stage","type":"string"}]},"steps":[]}`,
		},
		pkg: formPkg{forms: map[string]string{
			"create_contact": `{"credential_platform":null,"fields":[{"key":"email","label":"Work email","type":"text","required":true,"placeholder":"jane@acme.com"}]}`,
		}},
	})
	t.Cleanup(func() { action.SetDefSource(nil) })

	s, err := LoadDefaultSchema("acme-crm.create_contact")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Fields) != 1 || s.Fields[0].Label != "Work email" || s.Fields[0].Placeholder != "jane@acme.com" {
		t.Errorf("forms/ override not used: %+v", s.Fields)
	}
	// No form for this action: generated from inputs.
	s, _ = LoadDefaultSchema("acme-crm.list_deals")
	fieldByKey(t, s, "username")
	fieldByKey(t, s, "stage")
}

func fieldByKey(t *testing.T, s *NodeSchema, key string) NodeSchemaField {
	t.Helper()
	for _, f := range s.Fields {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("field %q missing; have %+v", key, s.Fields)
	return NodeSchemaField{}
}

func TestLoadDefaultSchema_GeneratedFromPackageInputs(t *testing.T) {
	action.SetDefSource(pkgSource{
		"acme-crm/create_contact": `{
		  "actionType": "create_contact",
		  "inputs": {
		    "required": [
		      {"name": "email", "type": "string", "format": "email",
		       "ui": {"label": "Email", "placeholder": "jane@x.com", "help": "Work email"}},
		      {"name": "selectedListItems", "type": "list", "description": "Accounts"}
		    ],
		    "optional": [
		      "note",
		      {"name": "maxRetries", "type": "number", "default": 3, "min": 1, "max": 9, "description": "Retries"},
		      {"name": "stage", "type": "string", "enum": ["lead", "customer"], "default": "lead"},
		      {"name": "notify", "type": "boolean"},
		      {"name": "bio", "type": "string", "ui": {"widget": "textarea", "rows": 4}},
		      {"name": "username", "type": "string"}
		    ]
		  },
		  "steps": []
		}`,
		// Same action name as a built-in shared form: must NOT borrow it.
		"acme-crm/find_by_keyword": `{"actionType":"find_by_keyword","inputs":{"required":[{"name":"query","type":"string"}]},"steps":[]}`,
	})
	t.Cleanup(func() { action.SetDefSource(nil) })

	s, err := LoadDefaultSchema("acme-crm.create_contact")
	if err != nil {
		t.Fatal(err)
	}
	if s.Fields[0].Key != "username" || s.Fields[0].Label != "Session Username" {
		t.Errorf("first field should be the session field, got %+v", s.Fields[0])
	}
	n := 0
	for _, f := range s.Fields {
		if f.Key == "username" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("username appears %d times", n)
	}

	email := fieldByKey(t, s, "email")
	if !email.Required || email.Type != "text" || email.Label != "Email" || email.Placeholder != "jane@x.com" || email.Help != "Work email" {
		t.Errorf("email: %+v", email)
	}
	targets := fieldByKey(t, s, "targets")
	if !targets.Required || targets.Type != "array" || targets.ItemType != "text" || targets.Help != "Accounts" {
		t.Errorf("targets: %+v", targets)
	}
	note := fieldByKey(t, s, "note")
	if note.Required || note.Type != "text" || note.Label != "Note" {
		t.Errorf("note: %+v", note)
	}
	mr := fieldByKey(t, s, "maxRetries")
	if mr.Type != "number" || mr.Label != "Max Retries" || mr.Default != float64(3) || mr.Min == nil || *mr.Min != 1 || mr.Max == nil || *mr.Max != 9 {
		t.Errorf("maxRetries: %+v", mr)
	}
	stage := fieldByKey(t, s, "stage")
	if stage.Type != "select" || len(stage.Options) != 2 || stage.Options[1] != "customer" || stage.Default != "lead" {
		t.Errorf("stage: %+v", stage)
	}
	if f := fieldByKey(t, s, "notify"); f.Type != "boolean" {
		t.Errorf("notify: %+v", f)
	}
	if f := fieldByKey(t, s, "bio"); f.Type != "textarea" || f.Rows != 4 {
		t.Errorf("bio: %+v", f)
	}

	fk, _ := LoadDefaultSchema("acme-crm.find_by_keyword")
	fieldByKey(t, fk, "query")
	for _, f := range fk.Fields {
		if f.Key == "keywords" {
			t.Error("imported package borrowed the built-in action.find_by_keyword form")
		}
	}

	raw, ok := ReadEmbeddedSchema("acme-crm.create_contact")
	if !ok || len(raw) == 0 {
		t.Error("ReadEmbeddedSchema: no generated schema")
	}
	if _, ok := ReadEmbeddedSchema("acme-crm.nope"); ok {
		t.Error("ReadEmbeddedSchema: unknown action should report missing")
	}
}

// Built-in nodes resolve: schema file → shared action.<name>.json →
// generated from the action's inputs (the full golden is
// TestBuiltinForms_Golden in internal/nodes).
func TestLoadDefaultSchema_BuiltinResolution(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	action.SetDefSource(nil)

	// No schema file: generated from the action's inputs.
	s, _ := LoadDefaultSchema("hackernews.submit_post")
	if f := fieldByKey(t, s, "title"); !f.Required {
		t.Errorf("hackernews.submit_post title: %+v", f)
	}
	if _, ok := ReadEmbeddedSchema("hackernews.submit_post"); !ok {
		t.Error("ReadEmbeddedSchema: no generated schema for hackernews.submit_post")
	}
	s, _ = LoadDefaultSchema("tiktok.like_video")
	if f := fieldByKey(t, s, "targets"); !f.Required {
		t.Errorf("tiktok.like_video targets should be required: %+v", f)
	}
	for _, f := range s.Fields {
		if f.Key == "keywords" || f.Key == "limit" {
			t.Errorf("tiktok.like_video offers %q, which the action ignores", f.Key)
		}
	}
	// Shared form.
	s, _ = LoadDefaultSchema("linkedin.find_by_keyword")
	fieldByKey(t, s, "keywords")
	// A legacy local-<platform> action keeps the generic form.
	s, _ = LoadDefaultSchema("local-instagram.my_custom_action")
	fieldByKey(t, s, "targets")
}

func TestHumanizeName(t *testing.T) {
	for in, want := range map[string]string{
		"maxComments": "Max Comments", "max_comments": "Max Comments",
		"postURL": "Post URL", "email": "Email", "url": "URL", "postId": "Post ID",
	} {
		if got := humanizeName(in); got != want {
			t.Errorf("humanizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Inputs the browser node fills from its own config names are asked for
// under those names, typed as the node reads them.
func TestInputField_NodeFacingKeys(t *testing.T) {
	for _, c := range []struct {
		in       actionInput
		key, typ string
	}{
		{actionInput{Name: "commentText", Type: "string"}, "message", "textarea"},
		{actionInput{Name: "replyText", Type: "string"}, "message", "textarea"},
		{actionInput{Name: "selectedListItems", Type: "list"}, "targets", "array"},
		{actionInput{Name: "maxResultsCount", Type: "number"}, "limit", "number"},
		{actionInput{Name: "maxComments", Type: "number", Aliases: []string{"limit"}}, "limit", "number"},
		{actionInput{Name: "searches", Type: "array", Aliases: []string{"keywords", "keyword"}}, "keywords", "text"},
		// A scalar aliased to targets keeps its name: the node reads targets as a list.
		{actionInput{Name: "target_url", Type: "string", Aliases: []string{"targets", "username"}}, "target_url", "text"},
		{actionInput{Name: "itemID", Type: "string"}, "itemID", "text"},
	} {
		f := inputField(c.in, true)
		if f.Key != c.key || f.Type != c.typ {
			t.Errorf("%s: key %q type %q, want %q %q", c.in.Name, f.Key, f.Type, c.key, c.typ)
		}
	}
	// The label still says what the action wants.
	if f := inputField(actionInput{Name: "commentText", Type: "string"}, true); f.Label != "Comment Text" {
		t.Errorf("label %q", f.Label)
	}
}
