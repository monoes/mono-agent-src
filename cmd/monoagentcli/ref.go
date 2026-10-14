package main

import (
	"github.com/spf13/cobra"

	refcli "github.com/monoes/mono-agent/internal/cli/ref"
	"github.com/monoes/mono-agent/internal/cli/ref/catalog"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `ref` lives in internal/cli/ref and the catalogues it renders in
// internal/cli/ref/catalog; the node registry it documents stays here.

// nodeDoc is one entry of the node catalogue; cmdDoc one of `ref commands`.
type (
	nodeDoc = refcli.NodeDoc
	cmdDoc  = refcli.CmdDoc
)

// cliDocs is the `ref commands` catalogue.
var cliDocs = catalog.CommandDocs

// findNodeDoc returns nil when not found.
func findNodeDoc(typ string) *nodeDoc { return catalog.FindNodeDoc(typ) }

// refDeps hands internal/cli/ref the catalogues and the node registry.
func refDeps() refcli.Deps {
	return refcli.Deps{
		Registry:    refNodeRegistry,
		NodeDocs:    func() []nodeDoc { return catalog.NodeDocs },
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
