package node

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	_ "github.com/monoes/mono-agent/internal/bot/gemini"
	_ "github.com/monoes/mono-agent/internal/bot/instagram"
	_ "github.com/monoes/mono-agent/internal/bot/linkedin"
	_ "github.com/monoes/mono-agent/internal/bot/tiktok"
	_ "github.com/monoes/mono-agent/internal/bot/x"
	"github.com/monoes/mono-agent/internal/i18n"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// reLinkedInActivity matches the numeric activity ID in LinkedIn post URLs.
// e.g. "activity-7123456789" or "activity:7123456789"
var reLinkedInActivity = regexp.MustCompile(`activity[-:](\d+)`)

// needsBrowserSession reports whether a node type drives the browser and so
// needs the extension session provider: every automation action (built-in
// or installed package, all BrowserNode) and the browser.* nodes.
func needsBrowserSession(nodeType string, factory workflow.NodeFactory) bool {
	if strings.HasPrefix(nodeType, "browser.") {
		return true
	}
	_, ok := factory().(*nodes.BrowserNode)
	return ok
}

// nodeTypeToPlatform maps a node type to its connections-registry platform ID.
// The mapping covers node-type prefixes that don't match their platform ID directly.
var nodeTypeToPlatformOverrides = map[string]string{
	"db.postgres": "postgresql",
	"db.mysql":    "mysql",
	"db.mongodb":  "mongodb",
	"db.redis":    "redis",
}

// nodeTypeToPlatform derives the connections-registry platform ID from a node type.
// Examples:
//
//	"service.google_sheets" → "google_sheets"
//	"service.github"        → "github"
//	"comm.slack"            → "slack"
//	"db.postgres"           → "postgresql"
//	"google_sheets"         → "google_sheets"  (legacy unprefixed)
func nodeTypeToPlatform(nodeType string) string {
	if p, ok := nodeTypeToPlatformOverrides[nodeType]; ok {
		return p
	}
	// Strip known category prefixes.
	for _, prefix := range []string{"service.", "comm.", "db."} {
		if strings.HasPrefix(nodeType, prefix) {
			return strings.TrimPrefix(nodeType, prefix)
		}
	}
	// Already a bare platform name (legacy alias).
	return nodeType
}

// NewCommand returns the `node` command with subcommands; the CLI adds `node palette`.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: i18n.T("node.short"),
	}
	cmd.AddCommand(
		d.newNodeListCmd(),
		d.newNodeRunCmd(),
		d.newNodeSchemaCmd(),
		d.newNodeCurlCmd(),
	)
	return cmd
}

// newNodeSchemaCmd prints a node type's embedded JSON schema (pretty-printed).
func (d Deps) newNodeSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "schema <type>",
		Short:   i18n.T("node.schema.short"),
		Example: i18n.T("node.schema.example"),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, ok := workflow.ReadEmbeddedSchema(args[0])
			if !ok {
				return d.NotFound("no schema for node type %q — run `monoagentcli node list` to see available types", args[0])
			}
			var buf bytes.Buffer
			if err := json.Indent(&buf, data, "", "  "); err != nil {
				return fmt.Errorf("format schema for %s: %w", args[0], err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), buf.String())
			return nil
		},
	}
}

// nodeListEntry is one element of `node list --json`.
type nodeListEntry struct {
	Type     string `json:"type"`
	Category string `json:"category"`
	Title    string `json:"title"`
}

// nodeTypeCategory returns the category portion of a node type — the prefix
// before the first dot (core, comm, db, http, …). Legacy unprefixed types
// fall back to the heuristic nodeCategory mapping.
func nodeTypeCategory(t string) string {
	if i := strings.Index(t, "."); i > 0 {
		return t[:i]
	}
	return nodeCategory(t)
}

// triggerNodeTypes are the trigger types the engine handles itself: they are
// never in the node registry (execution.go passes them through), so `node
// list` adds them to the registered types.
var triggerNodeTypes = []string{"trigger.manual", "trigger.org", "trigger.schedule", "trigger.sse", "trigger.webhook"}

// newNodeListCmd lists all registered node types and the engine's triggers.
func (d Deps) newNodeListCmd() *cobra.Command {
	var filter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all available node types",
		Example: `  monoagentcli node list
  monoagentcli node list --filter comm
  monoagentcli node list --filter http`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Open DB on best-effort basis so AI nodes appear in the list.
			var rawDB *sql.DB
			if db, err := d.OpenDB(); err == nil {
				rawDB = db.DB
				defer db.Close()
			}
			registry := d.BuildRegistry(rawDB)
			types := registry.Types()
			for _, t := range triggerNodeTypes {
				if !registry.Has(t) {
					types = append(types, t)
				}
			}
			sort.Strings(types)

			if filter != "" {
				var filtered []string
				for _, t := range types {
					if strings.Contains(t, filter) {
						filtered = append(filtered, t)
					}
				}
				types = filtered
			}

			if d.JSONOutput() {
				entries := make([]nodeListEntry, 0, len(types))
				for _, t := range types {
					entries = append(entries, nodeListEntry{
						Type:     t,
						Category: nodeTypeCategory(t),
						Title:    workflow.SchemaTitle(t),
					})
				}
				return json.NewEncoder(os.Stdout).Encode(entries)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "TYPE\tCATEGORY")
			fmt.Fprintln(w, "────\t────────")
			for _, t := range types {
				fmt.Fprintf(w, "%s\t%s\n", t, nodeTypeCategory(t))
			}
			w.Flush()
			fmt.Printf("\n%d node types\n", len(types))
			return nil
		},
	}

	cmd.Flags().StringVar(&filter, "filter", "", "Filter node types by substring")
	return cmd
}

// nodeCategory infers a display category from a node type string.
func nodeCategory(t string) string {
	switch {
	case strings.HasPrefix(t, "trigger."):
		return "trigger"
	case strings.HasPrefix(t, "control."), t == "if" || t == "switch" || t == "merge" || t == "set" || t == "code" || t == "filter" || t == "sort" || t == "limit" || t == "aggregate" || t == "wait":
		return "control"
	case strings.HasPrefix(t, "http."):
		return "http"
	case strings.HasPrefix(t, "system."):
		return "system"
	case strings.HasPrefix(t, "comm."):
		return "communication"
	case strings.HasPrefix(t, "ai."):
		return "ai"
	case strings.HasPrefix(t, "instagram."), strings.HasPrefix(t, "linkedin."), strings.HasPrefix(t, "x."), strings.HasPrefix(t, "tiktok."), strings.HasPrefix(t, "hackernews."), strings.HasPrefix(t, "producthunt."):
		return "browser/social"
	case strings.HasPrefix(t, "people."):
		return "people"
	case strings.HasPrefix(t, "app."):
		return "app"
	case t == "mysql" || t == "postgres" || t == "mongodb" || t == "redis":
		return "database"
	case t == "github" || t == "notion" || t == "airtable" || t == "jira" || t == "linear" || t == "asana" || t == "stripe" || t == "shopify" || t == "salesforce" || t == "hubspot" || t == "google_sheets" || t == "gmail" || t == "google_drive":
		return "service"
	case t == "datetime" || t == "crypto" || t == "html" || t == "xml" || t == "markdown" || t == "spreadsheet" || t == "compression" || t == "write_binary_file":
		return "data"
	default:
		return "other"
	}
}
