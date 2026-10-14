// Package ref is `monoagentcli ref`: the built-in reference — the topics (templates, workflow,
// connections, expressions, examples, crawling, api, org, publication, tasks, account), `ref
// commands`, and `ref nodes` / `ref node <type>`, which layer the hand-written node catalogue
// over each node type's schema. The catalogues and the node registry stay in the CLI and come
// in through Deps.
package ref

import "github.com/monoes/mono-agent/internal/workflow"

// NodeDoc is one hand-written entry of the node catalogue.
type NodeDoc struct {
	Type        string
	Category    string
	Short       string
	Description string
	Config      string // example config JSON (indented)
	Inputs      string
	Outputs     string
	Notes       string
}

// CmdDoc is one entry of `ref commands`.
type CmdDoc struct {
	Name     string
	Short    string
	Usage    string
	Flags    string
	Examples []string
}

// Deps is what `ref` takes from the CLI around it. Each is read when a command runs.
type Deps struct {
	// Registry is the node registry the manual documents (built without a database).
	Registry func() *workflow.NodeTypeRegistry
	// NodeDocs is the node catalogue, in catalogue order.
	NodeDocs func() []NodeDoc
	// FindNodeDoc returns a type's catalogue entry (matched case-insensitively), else the
	// entry generated for an API package node, else nil.
	FindNodeDoc func(typ string) *NodeDoc
	// CommandDocs is the `ref commands` catalogue, in order.
	CommandDocs func() []CmdDoc
}
