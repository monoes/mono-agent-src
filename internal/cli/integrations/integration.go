package integrations

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/n8nimport"
)

// maxSpecBytes caps an OpenAPI document fetched from a URL.
const maxSpecBytes = 32 << 20

// NewIntegrationCommand manages declarative API packages (internal/apipkg):
// the built-in catalog and the ones installed under ~/.monoagent/apipkgs.
func (d Deps) NewIntegrationCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "integration",
		Short: "List, inspect, validate and import declarative API packages (app.* nodes)",
		Long: "API packages describe a REST API as JSON (apipkg.v1); one engine runs them as app.<id> nodes.\n" +
			"Built-in packages ship in the binary; installed ones live in ~/.monoagent/apipkgs/<id>/package.json.\n" +
			"The author guide is internal/apipkg/FORMAT.md in the source tree.",
	}
	cmd.AddCommand(d.newIntegrationListCmd(), d.newIntegrationShowCmd(),
		d.newIntegrationImportOpenAPICmd(), d.newIntegrationValidateCmd())
	return cmd
}

// integrationSummary is one row of `integration list --json`.
type integrationSummary struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	NodeType   string   `json:"node_type,omitempty"`
	Extends    string   `json:"extends,omitempty"`
	Operations int      `json:"operations"`
	Platform   string   `json:"credential_platform,omitempty"`
	N8nTypes   []string `json:"n8n_types,omitempty"`
	Source     string   `json:"source"`
}

func (d Deps) newIntegrationListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the API packages (built-in and installed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pkgs, loadErr := apipkg.Load()
			rows := make([]integrationSummary, 0, len(pkgs))
			for _, p := range pkgs {
				rows = append(rows, integrationSummary{
					ID: p.ID, Title: p.Title, NodeType: p.NodeTypeOf(), Extends: p.Extends,
					Operations: len(p.Operations), Platform: p.PrimaryPlatform(),
					N8nTypes: append(append([]string{}, p.N8nTypes...), p.N8nTriggerTypes...), Source: p.Source,
				})
			}
			out := cmd.OutOrStdout()
			if loadErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: some packages were left out:\n%v\n", loadErr)
			}
			if d.JSONOutput() {
				return json.NewEncoder(out).Encode(rows)
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNODE TYPE\tOPS\tCONNECTION\tN8N\tSOURCE")
			for _, r := range rows {
				nt := r.NodeType
				if r.Extends != "" {
					nt = "extends " + r.Extends
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n", r.ID, nt, r.Operations, dash(r.Platform), dash(strings.Join(r.N8nTypes, ",")), r.Source)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(out, "\n%d packages\n", len(rows))
			return nil
		},
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (d Deps) newIntegrationShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one API package: its operations, or the whole file with --json",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			apipkg.Load() // errors concern other packages; a missing one is reported below
			p, ok := apipkg.ByID(args[0])
			if !ok {
				return clicore.NotFound("no API package %q — run `monoagentcli integration list`", args[0])
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(p)
			}
			fmt.Fprintf(out, "%s (%s) — %s\n", p.Title, p.ID, p.Description)
			if p.Extends != "" {
				fmt.Fprintf(out, "extends:    %s\n", p.Extends)
			} else {
				fmt.Fprintf(out, "node type:  %s\n", p.NodeTypeOf())
			}
			fmt.Fprintf(out, "base url:   %s\nsource:     %s\n", p.BaseURL, p.Source)
			for _, c := range p.Credentials {
				fmt.Fprintf(out, "connection: %s (%s) — monoagentcli connect %s\n", c.EffectivePlatform(), strings.Join(c.Methods, ", "), c.EffectivePlatform())
			}
			if len(p.N8nTypes) > 0 {
				fmt.Fprintf(out, "n8n:        %s\n", strings.Join(p.N8nTypes, ", "))
			}
			for _, t := range p.N8nTriggerTypes {
				fmt.Fprintf(out, "n8n trigger: %s → %s\n", t, triggerImport(t))
			}
			fmt.Fprintln(out)
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "RESOURCE\tOPERATION\tREQUEST\tFIELDS")
			for _, o := range p.Operations {
				var fields []string
				for _, f := range o.Fields {
					k := f.Key
					if f.Required {
						k += "*"
					}
					fields = append(fields, k)
				}
				fmt.Fprintf(w, "%s\t%s\t%s %s\t%s\n", dash(o.Resource), o.Operation, o.Method, o.Path, strings.Join(fields, ", "))
			}
			return w.Flush()
		},
	}
}

// triggerImport says what an n8n trigger of the package becomes when an
// n8n workflow is imported.
func triggerImport(n8nType string) string {
	switch n8nimport.TriggerKind(n8nType) {
	case "webhook":
		return "trigger.webhook (register its URL with the app)"
	case "poll":
		return "trigger.schedule polling the app's list operation, new records only"
	default:
		return "not imported yet (no trigger mapping)"
	}
}

func (d Deps) newIntegrationValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <file>",
		Short: "Validate an API package file against apipkg.v1 (schema and semantics)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(args[0])
			if err != nil {
				return clicore.NotFound("%v", err)
			}
			p, err := apipkg.Parse(b, args[0])
			if err != nil {
				var ve *apipkg.ValidationError
				if errors.As(err, &ve) {
					if d.JSONOutput() {
						_ = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"valid": false, "file": args[0], "problems": ve.Problems})
					}
					return clicore.InvalidInput("%v", err)
				}
				return err
			}
			if d.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"valid": true, "file": args[0], "id": p.ID, "operations": len(p.Operations)})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: valid (%s, %d operations)\n", args[0], p.ID, len(p.Operations))
			return nil
		},
	}
}

func (d Deps) newIntegrationImportOpenAPICmd() *cobra.Command {
	var id, ops, outDir string
	cmd := &cobra.Command{
		Use:   "import-openapi <file|url>",
		Short: "Convert an OpenAPI 3.x / Swagger 2.0 document (JSON or YAML) into an API package",
		Long: "Tags become resources, operationIds operations, parameters and request bodies fields,\n" +
			"security schemes connections. Without --out the package is installed to\n" +
			"~/.monoagent/apipkgs/<id>/package.json and its app.<id> node is available at once;\n" +
			"with --out it is written to <dir>/<id>.json (e.g. internal/apipkg/catalog). Review the\n" +
			"result: pagination and output paths cannot be inferred and are left out.",
		Example: `  monoagentcli integration import-openapi ./petstore.yaml --id petstore
  monoagentcli integration import-openapi https://example.com/openapi.json --ops pets,store --out ./pkgs`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readSpec(cmd, args[0])
			if err != nil {
				return err
			}
			var tags []string
			for _, t := range strings.Split(ops, ",") {
				if t = strings.TrimSpace(t); t != "" {
					tags = append(tags, t)
				}
			}
			errOut := cmd.ErrOrStderr()
			p, err := apipkg.OpenAPIToPackage(data, apipkg.OpenAPIOptions{ID: id, Tags: tags,
				Warn: func(s string) { fmt.Fprintln(errOut, "warning:", s) }})
			if err != nil {
				return clicore.InvalidInput("%v", err)
			}
			b, err := json.MarshalIndent(p, "", "  ")
			if err != nil {
				return err
			}
			path := filepath.Join(apipkg.InstallDir(), p.ID, "package.json")
			if outDir != "" {
				path = filepath.Join(outDir, p.ID+".json")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
				return err
			}
			apipkg.Reload()
			if d.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"id": p.ID, "node_type": p.NodeTypeOf(), "path": path, "operations": len(p.Operations)})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (%s, %d operations, node type %s)\n", path, p.ID, len(p.Operations), p.NodeTypeOf())
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Package id ([a-z0-9_]+; default: from info.title)")
	cmd.Flags().StringVar(&ops, "ops", "", "Only operations with one of these tags (comma-separated)")
	cmd.Flags().StringVar(&outDir, "out", "", "Write <dir>/<id>.json instead of installing the package")
	return cmd
}

// readSpec reads a local file or fetches an http(s) URL (size-capped).
func readSpec(cmd *cobra.Command, src string) ([]byte, error) {
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		b, err := os.ReadFile(src)
		if err != nil {
			return nil, clicore.NotFound("%v", err)
		}
		return b, nil
	}
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, src, nil)
	if err != nil {
		return nil, clicore.InvalidInput("%v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, clicore.AuthConnection("fetch %s: %v", src, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, clicore.AuthConnection("fetch %s: HTTP %d", src, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSpecBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSpecBytes {
		return nil, clicore.InvalidInput("%s is larger than %d MiB", src, maxSpecBytes>>20)
	}
	return b, nil
}
