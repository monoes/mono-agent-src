package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/monoes/mono-agent/internal/action"
)

// Forms for browser automation nodes ("<automation>.<action>") that have no
// schema file are generated from the action's declared inputs (spec §4.3).

// actionInput is one entry of an action's inputs.required/optional. An entry
// may also be a bare string, which is just the name.
type actionInput struct {
	Name        string        `json:"name"`
	Type        string        `json:"type"`
	Description string        `json:"description"`
	Default     interface{}   `json:"default"`
	Min         *float64      `json:"min"`
	Max         *float64      `json:"max"`
	Enum        []interface{} `json:"enum"`
	Options     []interface{} `json:"options"`
	Format      string        `json:"format"`
	Aliases     []string      `json:"aliases"`
	UI          *inputUI      `json:"ui"`
}

// inputUI is the optional "ui" hint block on an input.
type inputUI struct {
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
	Help        string `json:"help"`
	Widget      string `json:"widget"` // any NodeSchemaField type, e.g. textarea, password, code
	Rows        int    `json:"rows"`
}

// sessionField is the browser-session selector every generated browser form
// starts with (the same field browser.generic.json offers).
var sessionField = NodeSchemaField{
	Key:      "username",
	Label:    "Session Username",
	Type:     "text",
	Required: false,
	Help:     "Username of the browser session to use. Leave blank to use the default session.",
}

// officialAutomations are the packages that used to ship built into the
// app and are now published as official items on monoes.me (source:
// automations/ in the repo). Their nodes keep their pre-package forms
// whichever way they were installed.
var officialAutomations = map[string]bool{
	"gemini": true, "hackernews": true, "instagram": true, "linkedin": true,
	"producthunt": true, "tiktok": true, "x": true,
}

// loadActionDef finds the action behind a browser node type, through the
// installed definition source when there is one, else the legacy loader.
func loadActionDef(automation, actionType string) (*action.ActionDef, error) {
	return loadActionDefFrom(automation, actionType)
}

func loadActionDefFrom(automation, actionType string) (*action.ActionDef, error) {
	src := action.CurrentDefSource()
	if src == nil {
		return action.GetLoader().Load(automation, actionType)
	}
	raw, err := src.Load(automation, actionType)
	if err != nil {
		return nil, err
	}
	var def action.ActionDef
	if err := json.Unmarshal(raw, &def); err != nil {
		return nil, fmt.Errorf("parse %s/%s: %w", automation, actionType, err)
	}
	return &def, nil
}

// formProvider is implemented by package contexts that can serve a package's
// forms/<action>.json override.
type formProvider interface {
	Form(actionName string) ([]byte, error)
}

// packageForm returns the package's forms/<action>.json, which replaces the
// generated form for that action.
func packageForm(automation, actionType string) (*NodeSchema, bool) {
	src := action.CurrentDefSource()
	if src == nil {
		return nil, false
	}
	fp, ok := src.Package(automation).(formProvider)
	if !ok {
		return nil, false
	}
	raw, err := fp.Form(actionType)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	var schema NodeSchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, false
	}
	if schema.Fields == nil {
		schema.Fields = []NodeSchemaField{}
	}
	return &schema, true
}

// sessionPlatform is the login/session platform of a browser automation
// node — what the editor's session picker lists sessions for — or "" when
// nodeType isn't an automation action. It is the automation id; a legacy
// local-<p> package runs on <p>'s login, so its original platform name
// (the registry's LegacyPlatform, else the id without "local-").
func sessionPlatform(nodeType string) string {
	dot := strings.Index(nodeType, ".")
	if dot <= 0 || dot == len(nodeType)-1 {
		return ""
	}
	id := nodeType[:dot]
	if _, err := loadActionDef(id, nodeType[dot+1:]); err != nil {
		return ""
	}
	if !strings.HasPrefix(id, "local-") {
		return id
	}
	if src := action.CurrentDefSource(); src != nil {
		if lp, ok := src.Package(id).(interface{ LegacyPlatform() string }); ok {
			if name := lp.LegacyPlatform(); name != "" {
				return name
			}
		}
	}
	return strings.TrimPrefix(id, "local-")
}

// generateActionSchema builds the form for a browser node type: the
// package's forms/ override when there is one, else one built from its
// action's inputs. ok is false when nodeType is not a known action.
func generateActionSchema(nodeType string) (*NodeSchema, bool) {
	dot := strings.Index(nodeType, ".")
	if dot <= 0 || dot == len(nodeType)-1 {
		return nil, false
	}
	// Legacy local-* nodes keep their pre-package forms.
	if strings.HasPrefix(nodeType, "local-") {
		return nil, false
	}
	if form, ok := packageForm(nodeType[:dot], nodeType[dot+1:]); ok {
		return form, true
	}
	def, err := loadActionDef(nodeType[:dot], nodeType[dot+1:])
	if err != nil || def == nil {
		return nil, false
	}
	return schemaFromInputs(def.Inputs), true
}

// schemaFromInputs turns declared inputs into form fields, required first,
// after the session field.
func schemaFromInputs(in *action.InputDef) *NodeSchema {
	schema := &NodeSchema{Fields: []NodeSchemaField{sessionField}}
	if in == nil {
		return schema
	}
	seen := map[string]bool{sessionField.Key: true}
	add := func(raws []json.RawMessage, required bool) {
		for _, raw := range raws {
			ai, ok := parseActionInput(raw)
			if !ok {
				continue
			}
			f := inputField(ai, required)
			if seen[f.Key] {
				continue
			}
			seen[f.Key] = true
			schema.Fields = append(schema.Fields, f)
		}
	}
	add(in.Required, true)
	add(in.Optional, false)
	return schema
}

func parseActionInput(raw json.RawMessage) (actionInput, bool) {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return actionInput{Name: name}, name != ""
	}
	var ai actionInput
	if err := json.Unmarshal(raw, &ai); err != nil || ai.Name == "" {
		return actionInput{}, false
	}
	return ai, true
}

func inputField(ai actionInput, required bool) NodeSchemaField {
	f := NodeSchemaField{
		Key:      ai.Name,
		Label:    humanizeName(ai.Name),
		Required: required,
		Default:  ai.Default,
		Help:     ai.Description,
		Min:      ai.Min,
		Max:      ai.Max,
	}
	// Inputs the browser node fills from its own config names are asked for
	// under that name, so the form writes the config the node expects and
	// workflows saved with the old generic form still fill it in.
	switch key := nodeFacingKey(ai); key {
	case ai.Name:
	case "targets":
		f.Key, f.Label = key, "Targets"
	default:
		f.Key = key
	}

	opts := ai.Options
	if len(opts) == 0 {
		opts = ai.Enum
	}
	switch {
	case len(opts) > 0:
		f.Type = "select"
		for _, o := range opts {
			f.Options = append(f.Options, fmt.Sprint(o))
		}
	case ai.Type == "number" || ai.Type == "integer":
		f.Type = "number"
	case ai.Type == "boolean":
		f.Type = "boolean"
	case ai.Type == "list" || ai.Type == "array":
		f.Type, f.ItemType = "array", "text"
	case ai.Format == "password":
		f.Type = "password"
	default:
		f.Type = "text"
	}

	// The node reads these names as scalars whatever the input's own type.
	switch f.Key {
	case "message":
		if f.Type == "text" || f.Type == "array" {
			f.Type, f.ItemType, f.Rows = "textarea", "", 3
		}
	case "keywords":
		f.Type, f.ItemType = "text", ""
	case "limit":
		f.Type, f.ItemType = "number", ""
	}

	if ui := ai.UI; ui != nil {
		if ui.Label != "" {
			f.Label = ui.Label
		}
		if ui.Placeholder != "" {
			f.Placeholder = ui.Placeholder
		}
		if ui.Help != "" {
			f.Help = ui.Help
		}
		if ui.Widget != "" {
			f.Type = ui.Widget
		}
		if ui.Rows > 0 {
			f.Rows = ui.Rows
		}
	}
	return f
}

// acronyms are written in capitals in generated labels ("postUrl" → "Post URL").
var acronyms = map[string]bool{"url": true, "id": true, "api": true, "dm": true, "html": true, "json": true}

// nodeFacingKeys are the browser node's config names and the action inputs
// it fills from them (see BrowserNode.Execute and the action's variable
// seeding): targets → selectedListItems; message → the message/comment/
// reply text variables; keywords → keyword(s); limit → maxResultsCount.
var nodeFacingKeys = map[string]string{
	"selectedListItems": "targets",
	"targets":           "targets",
	"message":           "message",
	"messageText":       "message",
	"contentMessage":    "message",
	"commentText":       "message",
	"replyText":         "message",
	"text":              "message",
	"keyword":           "keywords",
	"keywords":          "keywords",
	"maxResultsCount":   "limit",
	"limit":             "limit",
}

// nodeFacingKey is the config name the form uses for an input: its node
// mapping, else a scalar node name among its aliases (e.g. maxComments
// aliased to limit), else the input's own name. A scalar input aliased to
// "targets" keeps its name: the node only reads targets as a list.
func nodeFacingKey(ai actionInput) string {
	if k, ok := nodeFacingKeys[ai.Name]; ok {
		return k
	}
	for _, a := range ai.Aliases {
		if k, ok := nodeFacingKeys[a]; ok && k != "targets" {
			return k
		}
	}
	return ai.Name
}

// humanizeName turns "maxComments" or "max_comments" into "Max Comments".
func humanizeName(name string) string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}
	prevLower := false
	for _, r := range name {
		startsWord := unicode.IsUpper(r) && prevLower
		prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
		switch {
		case r == '_' || r == '-' || r == ' ':
			flush()
		case startsWord:
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	for i, w := range words {
		if acronyms[strings.ToLower(w)] {
			words[i] = strings.ToUpper(w)
			continue
		}
		rs := []rune(w)
		rs[0] = unicode.ToUpper(rs[0])
		words[i] = string(rs)
	}
	return strings.Join(words, " ")
}
