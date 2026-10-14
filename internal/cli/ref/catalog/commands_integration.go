package catalog

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/apipkg"
)

// The `integration` entries of `ref commands`: declarative API packages
// (internal/apipkg) that run as app.<id> nodes.
var integrationCommandDocs = []cmdDoc{
	{
		Name:  "integration list",
		Short: "List the API packages: the built-in catalog and ~/.monoagent/apipkgs/<id>/package.json",
		Usage: "monoagentcli integration list",
		Examples: []string{
			"monoagentcli integration list",
			"monoagentcli --json integration list",
			"monoagentcli node schema app.todoist",
		},
	},
	{
		Name:  "integration show",
		Short: "Show one API package: node type, connection, n8n types and every operation with its fields (* = required)",
		Usage: "monoagentcli integration show <id>",
		Examples: []string{
			"monoagentcli integration show todoist",
			"monoagentcli --json integration show todoist",
		},
	},
	{
		Name:  "integration import-openapi",
		Short: "Convert an OpenAPI 3.x / Swagger 2.0 document (file or URL, JSON or YAML) into an API package; installs it unless --out",
		Usage: "monoagentcli integration import-openapi <file|url> [--id <id>] [--ops <tag,…>] [--out <dir>]",
		Flags: `  --id string   Package id ([a-z0-9_]+; default: from info.title)
  --ops string  Only operations with one of these tags (comma-separated)
  --out string  Write <dir>/<id>.json instead of installing the package`,
		Examples: []string{
			"monoagentcli integration import-openapi ./petstore.yaml --id petstore",
			"monoagentcli integration import-openapi https://example.com/openapi.json --ops pets --out ./pkgs",
		},
	},
	{
		Name:  "integration validate",
		Short: "Validate an API package file (apipkg.v1 schema and semantic checks); exit 3 with every problem listed",
		Usage: "monoagentcli integration validate <file>",
		Examples: []string{
			"monoagentcli integration validate internal/apipkg/catalog/todoist.json",
		},
	},
}

// appNodeDoc documents an API package node type (app.*) from its package:
// the operations with their fields, an example config and the connection.
func appNodeDoc(typ string) *nodeDoc {
	p, ok := apipkg.ForNodeType(typ)
	if !ok {
		return nil
	}
	var ops strings.Builder
	for _, o := range p.Operations {
		var fields []string
		for _, f := range o.Fields {
			k := f.Key
			if f.Required {
				k += "*"
			}
			fields = append(fields, k)
		}
		key := o.Operation
		if o.Resource != "" {
			key = "resource=" + o.Resource + " operation=" + o.Operation
		}
		fmt.Fprintf(&ops, "  %-48s %s %s\n", key, o.Method, o.Path)
		if len(fields) > 0 {
			fmt.Fprintf(&ops, "  %-48s fields: %s\n", "", strings.Join(fields, ", "))
		}
	}
	first := p.Operations[0]
	example := map[string]any{"credential_id": p.PrimaryPlatform()}
	if first.Resource != "" {
		example["resource"] = first.Resource
	}
	example["operation"] = first.Operation
	for _, f := range first.Fields {
		if f.Required {
			example[f.Key] = "<" + f.Label + ">"
		}
	}
	cfg, _ := json.MarshalIndent(example, "", "  ")
	notes := "API package " + p.ID + " (" + p.Source + "); `monoagentcli integration show " + p.ID + "` lists it, `node schema " + typ + "` gives the form."
	if pl := p.PrimaryPlatform(); pl != "" {
		notes += " Connect with `monoagentcli connect " + pl + "`."
	}
	if len(p.N8nTypes) > 0 {
		notes += " Imports n8n node " + strings.Join(p.N8nTypes, ", ") + " with the same parameter names."
	}
	return &nodeDoc{
		Type: typ, Category: "app", Short: p.Title + " — " + p.Description,
		Description: "Runs one " + p.Title + " API operation per input item (* = required field):\n" + ops.String(),
		Config:      string(cfg),
		Inputs:      "items; fields may use {{ $json.x }} and are resolved per item",
		Outputs:     "the API response: list results become one item per element; files land in the item's binary data",
		Notes:       notes,
	}
}
