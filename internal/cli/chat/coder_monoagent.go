package chat

import (
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/agent"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

const defaultDBPath = "~/.monoagent/monoagent.db"

// A coder turn gets none of mono-agent's own tools (AGENTS.md, "Coder mode"):
// its agent has a shell, and synced messages must never reach a turn that has
// one. What it gets instead is the mono-agent section of its system prompt,
// which points it at the part of the CLI that builds automation, so it builds
// on the nodes, templates and workflows mono-agent already has instead of
// writing standalone code.

// monoagentInvocation is the command prefix that reaches this installation
// from a coder agent's shell: bin by absolute path, since a turn the GUI
// started may not have it on PATH; the turn's resolved profile, since the CLI
// takes the active profile from settings when a call names none (it ignores
// MONOAGENT_PROFILE); and the database when it isn't the default, made
// absolute because the agent works in another folder than this process. An
// empty bin (os.Executable failed) falls back to the bare name, which the
// agent can only run if the CLI is on its PATH.
func monoagentInvocation(bin, profileID, dbPath string) string {
	if bin == "" {
		bin = "monoagentcli"
	}
	parts := []string{agent.ShellQuote(bin)}
	if profileID != "" {
		parts = append(parts, "--profile", agent.ShellQuote(profileID))
	}
	if dbPath != "" && dbPath != defaultDBPath {
		if abs, err := filepath.Abs(clicore.ExpandPath(dbPath)); err == nil {
			parts = append(parts, "--db-path", agent.ShellQuote(abs))
		}
	}
	return strings.Join(parts, " ")
}

// monoagentPrompt is the system prompt section that makes a coder agent check
// what mono-agent already does before it writes code. cli is the
// monoagentInvocation the agent runs; the commands below it are written
// `<cli> …` so each one is a backtick span a test can check against the real
// command tree. It sends the agent to the workflow, node and ref commands
// only, and it never shows a real run.
func monoagentPrompt(cli string) string {
	return "mono-agent is installed on this computer: a local workflow-automation engine with a large library of nodes " +
		"(schedules and webhooks, HTTP, databases, email and chat, AI and more) and ready-made templates. " +
		"In the commands below `<cli>` stands for `" + cli + "`; keep its flags.\n\n" +
		"For automation, integration, scheduling, messaging, scraping or data-moving tasks, find out what it already does " +
		"before writing standalone code, then build on it:\n" +
		"1. Discover: `<cli> --json workflow search \"<query>\"` lists the templates and saved workflows that already run; " +
		"`<cli> ref nodes` lists the node types and `<cli> node schema <type>` shows one's config. " +
		"`<cli> ref` is the offline manual; prefer it to guessing from --help.\n" +
		"2. Build: when a template fits, `<cli> workflow templates use <id>` (it starts inactive). Otherwise write the workflow JSON " +
		"(`<cli> ref workflow` has the format) with `\"is_active\": false`, since an active workflow's triggers fire on their own " +
		"once the daemon runs; check it with `<cli> workflow validate --file <wf.json>`, " +
		"store it with `<cli> workflow import --file <wf.json>` (it prints the id), and test it with `<cli> workflow run <id> --dry-run`.\n" +
		"3. Gaps: where no node fits, write the smallest code that does it in this folder and call it from a " +
		"core.code or system.execute_command node, so the result is still a mono-agent workflow.\n" +
		"Say what you reused. A task with no automation angle, like fixing a bug or editing code, needs none of this.\n\n" +
		"Before a real workflow run, activating a workflow, or anything that acts on a real account (posting, sending, " +
		"following), say what it will do and wait for a yes in chat; a --dry-run needs no ask. `node run` really executes a " +
		"node, so the same rule covers any node that sends, posts or changes something; a plain lookup or local computation " +
		"needs no ask. Never read, send or change the user's messages or people records, and never print or export a " +
		"secret, directly or through a workflow's output; reference credentials by id (`<cli> ref connections`)."
}

// MonoagentPrompt is the coder prompt's mono-agent section for the command
// prefix cli; the command-tree tests in package main check what it teaches
// against the real commands.
func MonoagentPrompt(cli string) string { return monoagentPrompt(cli) }
