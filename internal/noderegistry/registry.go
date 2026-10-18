// Package noderegistry builds the canonical workflow node-type registry
// shared by the CLI and the desktop app, so the set of available nodes
// has a single source of truth.
package noderegistry

import (
	"database/sql"
	"sync"

	"github.com/rs/zerolog"

	ainodes "github.com/monoes/mono-agent/internal/ai/nodes"
	"github.com/monoes/mono-agent/internal/apipkg"
	cfgpkg "github.com/monoes/mono-agent/internal/config"
	"github.com/monoes/mono-agent/internal/hilsend"
	_ "github.com/monoes/mono-agent/internal/n8nimport/n8napps" // n8n import of API package nodes
	"github.com/monoes/mono-agent/internal/nodes"
	agentnodes "github.com/monoes/mono-agent/internal/nodes/agent"
	choosenodes "github.com/monoes/mono-agent/internal/nodes/ai/choose"
	crawlnodes "github.com/monoes/mono-agent/internal/nodes/ai/crawl"
	aisubnodes "github.com/monoes/mono-agent/internal/nodes/aisub"
	applicationsnodes "github.com/monoes/mono-agent/internal/nodes/applications"
	applynodes "github.com/monoes/mono-agent/internal/nodes/apply"
	"github.com/monoes/mono-agent/internal/nodes/browserjev"
	"github.com/monoes/mono-agent/internal/nodes/comm"
	"github.com/monoes/mono-agent/internal/nodes/control"
	"github.com/monoes/mono-agent/internal/nodes/data"
	dbnodes "github.com/monoes/mono-agent/internal/nodes/db"
	discoverynodes "github.com/monoes/mono-agent/internal/nodes/discovery"
	documentsnodes "github.com/monoes/mono-agent/internal/nodes/documents"
	evalnode "github.com/monoes/mono-agent/internal/nodes/evalnode"
	guardrailsnodes "github.com/monoes/mono-agent/internal/nodes/guardrails"
	httpnodes "github.com/monoes/mono-agent/internal/nodes/http"
	imagenodes "github.com/monoes/mono-agent/internal/nodes/image"
	matchingnodes "github.com/monoes/mono-agent/internal/nodes/matching"
	mcpnodes "github.com/monoes/mono-agent/internal/nodes/mcp"
	orgnodes "github.com/monoes/mono-agent/internal/nodes/org"
	peoplenodes "github.com/monoes/mono-agent/internal/nodes/people"
	publicationnodes "github.com/monoes/mono-agent/internal/nodes/publication"
	"github.com/monoes/mono-agent/internal/nodes/service"
	"github.com/monoes/mono-agent/internal/nodes/system"
	vaultnodes "github.com/monoes/mono-agent/internal/nodes/vault"
	vectornodes "github.com/monoes/mono-agent/internal/nodes/vector"
	"github.com/monoes/mono-agent/internal/workflow"
)

// buildMu serializes Build: several node packages keep their DB connection
// in a package-level variable that RegisterAll sets, so concurrent Builds
// (parallel tests, the GUI and a CLI call in one process) would race on it.
var buildMu sync.Mutex

// Build creates a registry with all built-in node types registered.
// If db is non-nil, DB-backed node packages get the connection.
func Build(db *sql.DB) *workflow.NodeTypeRegistry {
	buildMu.Lock()
	defer buildMu.Unlock()
	// Outbound sends started by an agent wait for approval (internal/hilsend),
	// the API packages' annotated sends included.
	workflow.SetExecGuard(hilsend.Guard)
	hilsend.SetClassifier(apipkgSends{})
	// A reply a waiting node opens in front of a send is the user's
	// (workflow.RedactReplies); hilsend tells what sends.
	workflow.SetSendClassifier(hilsend.MaySend)
	registry := workflow.NewNodeTypeRegistry()
	control.RegisterAll(registry)
	data.RegisterAll(registry)
	httpnodes.RegisterAll(registry)
	system.RegisterAll(registry)
	dbnodes.RegisterAll(registry)
	comm.RegisterAll(registry)
	service.RegisterAll(registry)
	nodes.RegisterBrowserNodes(registry)
	peoplenodes.RegisterAll(registry, db)
	applicationsnodes.RegisterAll(registry, db)
	discoverynodes.RegisterAll(registry, db)
	documentsnodes.RegisterAll(registry, db)
	matchingnodes.RegisterAll(registry, db)
	applynodes.RegisterAll(registry, db)

	// Local AI agent nodes (monomind delegation) — no store needed.
	agentnodes.RegisterAll(registry)
	aisubnodes.RegisterAll(registry) // ai.model / ai.memory / ai.output_parser: sub-nodes of agent.ask
	mcpnodes.RegisterAll(registry)   // mcp.call: MCP client (stdio / Streamable HTTP)

	// Goal-driven browser agent (TypeSafe Jev) in the user's own browser.
	browserjev.RegisterAll(registry)
	choosenodes.RegisterAll(registry)     // ai.choose: route items by a TypeSafe Jev choice
	guardrailsnodes.RegisterAll(registry) // ai.guardrails: check or sanitize text (n8n Guardrails)
	evalnode.RegisterAll(registry)        // core.evaluation: n8n Evaluation (workflow evaluate)

	// Org runtime node (org.run) — kicks off/manages agent orgs from workflows.
	orgnodes.RegisterAll(registry)

	// Historical ai.* provider nodes are deprecated: fail-fast stubs keep
	// saved workflows actionable instead of "unknown node type".
	ainodes.RegisterDeprecated(registry)

	// Image processing nodes (Tier 1)
	imagenodes.RegisterAll(registry)

	// Credential vault nodes (vault.secret_save / vault.secret_get)
	vaultnodes.RegisterAll(registry)
	vectornodes.RegisterAll(registry) // RAG: vector.embed / split_text / load_document / store
	publicationnodes.RegisterAll(registry)
	apipkg.Register(registry) // declarative API packages: app.* node types

	// AI crawl nodes (natural extraction runs on a local agent)
	crawlnodes.RegisterAll(registry, cfgpkg.NewAgentGenerator(zerolog.Nop()))

	// Register legacy (unprefixed) aliases so old workflows still resolve.
	for legacy, canonical := range map[string]string{
		"google_sheets": "service.google_sheets", "gmail": "service.gmail", "google_drive": "service.google_drive",
		"github": "service.github", "notion": "service.notion", "airtable": "service.airtable",
		"jira": "service.jira", "linear": "service.linear", "asana": "service.asana",
		"stripe": "service.stripe", "shopify": "service.shopify", "salesforce": "service.salesforce",
		"hubspot": "service.hubspot",
		"slack":   "comm.slack", "discord": "service.discord", "telegram": "comm.telegram",
		"twilio": "comm.twilio", "whatsapp": "comm.whatsapp",
		"email_send": "comm.email_send", "email_read": "comm.email_read",
		"mysql": "db.mysql", "postgres": "db.postgres", "mongodb": "db.mongodb", "redis": "db.redis",
		"datetime": "data.datetime", "crypto": "data.crypto", "html": "data.html",
		"xml": "data.xml", "markdown": "data.markdown", "spreadsheet": "data.spreadsheet",
		"compression": "data.compression", "write_binary_file": "data.write_binary_file",
		"if": "core.if", "switch": "core.switch", "merge": "core.merge", "set": "core.set",
		"code": "core.code", "filter": "core.filter", "sort": "core.sort", "limit": "core.limit",
		"aggregate": "core.aggregate", "wait": "core.wait",
		"http_request": "http.request", "http_response": "http.response",
		"execute_command": "system.execute_command", "rss_read": "system.rss_read",
		"read_write_file": "system.read_write_file",
	} {
		registry.Alias(legacy, canonical)
	}

	return registry
}

// apipkgSends is the API packages' annotated sends (apipkg.SendOf) as
// hilsend classifies them.
type apipkgSends struct{}

func (apipkgSends) Classify(nodeType string, config map[string]interface{}) (hilsend.Classified, bool) {
	s, ok := apipkg.SendOf(nodeType, config)
	return hilsend.Classified{Action: s.Action, Channel: s.Channel, To: s.To, Text: s.Text, Subject: s.Subject,
		ToLabel: s.ToLabel, ToUnset: s.ToUnset, Op: s.Op, Show: s.Show, Exprs: s.Exprs, PerItem: s.PerItem}, ok
}

func (apipkgSends) SendsSome(nodeType string) bool { return apipkg.SendsSome(nodeType) }
