package main

import (
	"strings"

	"github.com/spf13/cobra"

	refcli "github.com/monoes/mono-agent/internal/cli/ref"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `ref` lives in internal/cli/ref; the catalogues it renders stay here: the
// node catalogue (nodeDocs, ref_nodes_*.go) and the commands catalogue
// (cliDocs, ref_commands.go and the init() of other files), and so does the
// node registry it documents.

// ── node catalogue ────────────────────────────────────────────────────────────

// nodeDoc is one hand-written entry of the node catalogue.
type nodeDoc = refcli.NodeDoc

// nodeDocs is the hand-written prose for node types. Each category lives in
// its own ref_nodes_<group>.go file; ref_nodes_more.go appends the rest at
// init. `ref node` layers this prose over the type's schema, so a type with
// no entry here still gets a generated page.
var nodeDocs = joinNodeDocs(
	nodeDocsPublication, nodeDocsTrigger, nodeDocsSystem, nodeDocsCore,
	nodeDocsHttp, nodeDocsData, nodeDocsImage, nodeDocsVault, nodeDocsDb,
	nodeDocsComm, nodeDocsService, nodeDocsAi, nodeDocsGemini,
	nodeDocsInstagram, nodeDocsLinkedin, nodeDocsX, nodeDocsTiktok,
	nodeDocsApplications, nodeDocsPeople,
)

func joinNodeDocs(parts ...[]nodeDoc) []nodeDoc {
	var all []nodeDoc
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

// findNodeDoc returns nil when not found.
func findNodeDoc(typ string) *nodeDoc {
	for i := range nodeDocs {
		if strings.EqualFold(nodeDocs[i].Type, typ) {
			return &nodeDocs[i]
		}
	}
	return appNodeDoc(typ) // API package nodes (app.*) are documented from their package
}

// refDeps hands internal/cli/ref the catalogues and the node registry. They are
// read when a command runs, so the init() appends are in.
func refDeps() refcli.Deps {
	return refcli.Deps{
		Registry:    refNodeRegistry,
		NodeDocs:    func() []nodeDoc { return nodeDocs },
		FindNodeDoc: findNodeDoc,
		CommandDocs: func() []cmdDoc { return cliDocs },
	}
}

func newRefCmd() *cobra.Command { return refcli.NewCommand(refDeps()) }

// refNodeRegistry is the registry `ref` documents. No database: the manual
// is offline, and the DB-backed packages register their types without one.
func refNodeRegistry() *workflow.NodeTypeRegistry {
	return buildNodeRegistry(false, nil)
}

// refAPICmd is `ref api`.
func refAPICmd() *cobra.Command { return refSubcommand("api") }

// refAccountCmd is `ref account`.
func refAccountCmd() *cobra.Command { return refSubcommand("account") }

// refSubcommand is one `ref` topic as a command of its own (no parent).
func refSubcommand(name string) *cobra.Command {
	ref := newRefCmd()
	c, _, _ := ref.Find([]string{name})
	ref.RemoveCommand(c)
	return c
}

// refTasksText is the `ref tasks` topic.
const refTasksText = refcli.TasksText
