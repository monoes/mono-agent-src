// Package nodejs is `monoagentcli nodejs`: the private Node.js runtime monoagent can install
// for monomind (internal/nodemgr), and FinishStreamed, the end of a command whose progress was
// streamed as `doctor fix` streams it. Everything the commands need from the CLI around them
// comes in through Deps, so they can be tested in isolation.
package nodejs

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// StdinIsTerminal says whether `nodejs remove` may ask instead of requiring --yes.
	StdinIsTerminal func() bool
}
