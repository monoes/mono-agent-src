// Package accountgate is the CLI gate: layer 1 of the monoes.me account gate, which keeps every
// command that is not open refused until there is a valid login. It classifies the command tree
// and judges an invocation before cobra runs anything of it. cmd/monoagentcli installs it; what it
// needs from the CLI (the command tree, the CLI's login_required error) comes in through Gate.
package accountgate

import (
	"strings"

	"github.com/spf13/cobra"
)

// The CLI gate (spec §6.1, D6, D20): layer 1 of the monoes.me account gate.
// Every command carries a class in Annotations[accountAnnotation]:
//
//	open   runs whatever the account state;
//	gated  with no valid session is refused before cobra runs a single hook
//	       (no first-run check, no database open; from the enforcement date the
//	       guard writes only the clock-guard record, A25): exit 4;
//	serve  a long-running command (spec §6.4): it starts even when locked and
//	       refuses the work itself (layers 2 and 3), because a daemon that
//	       exited on the lock would be respawned in a loop by launchd, and a
//	       locked MCP server or bridge could not tell its client what to do.
//
// One table classifies the whole command tree, so the commands need no edit
// and a command added later is gated until someone opens it here.
const (
	Annotation = "monoagent.account"
	ClassOpen  = "open"
	ClassGated = "gated"
	ClassServe = "serve"
)

// accountClasses is that table, keyed by command path without the root's
// name. A command takes the class of its nearest listed ancestor: `account`
// covers all the account commands. A command
// that nothing lists is gated, and so is one stamped with any other word.
//
// `org serve` is not a serving command here: it is a launcher that starts the
// external monomind process and returns, so a refusal causes no respawn loop and
// a locked machine does not start org services (D8).
//
// Open is the way in and nothing else: signing in (account, and the library
// aliases of it), help, the version, and update, so that a broken install can
// be fixed, and `doomsday stop`, so that an open-ended run can always be stopped. Everything else, ref, doctor, setup, release and the completion
// scripts included, waits for a login.
//
// help, completion and __complete are cobra's own: it adds them while a
// command executes, after the gate has looked, so they reach the gate as the
// root with leftover arguments (see InvocationClass). help is listed here;
// completion and __complete (and its alias __completeNoDesc) are not listed,
// so they are gated like every unlisted command.
var accountClasses = map[string]string{
	"version":        ClassOpen,
	"update":         ClassOpen,
	"account":        ClassOpen,
	"library login":  ClassOpen,
	"library logout": ClassOpen,
	"library status": ClassOpen,
	"help":           ClassOpen,

	// The one exemption outside the way in (the user's decision): Stop of the open-ended mode works
	// signed out, because a run must always be stoppable, whatever the account, the network or the
	// key store. Exactly this verb: not the doomsday group, no sibling (start, finish, rubric, status,
	// render and the rest wait for a login). It is still operator-only (requireOperator and
	// opgate.Refusal), so signed out is not "anyone". TestOnlyStopOfDoomsdayIsOpen holds it.
	"doomsday stop": ClassOpen,

	"daemon":           ClassServe,
	"daemon install":   ClassGated,
	"daemon restart":   ClassGated,
	"daemon uninstall": ClassGated,
	"httpapi":          ClassServe,
	"mcp":              ClassServe,
	"extension serve":  ClassServe,
}

// Listed is a copy of accountClasses, for the tests that hold it to the real command tree.
func Listed() map[string]string {
	out := make(map[string]string, len(accountClasses))
	for k, v := range accountClasses {
		out[k] = v
	}
	return out
}

// CommandKey is c's path below the root: "doctor fix", "library login".
func CommandKey(c *cobra.Command) string {
	var names []string
	for ; c != nil && c.HasParent(); c = c.Parent() {
		names = append([]string{c.Name()}, names...)
	}
	return strings.Join(names, " ")
}

// Apply stamps every command below root with its class from
// accountClasses. It can run again on a tree that gained commands.
func Apply(root *cobra.Command) {
	var walk func(parent *cobra.Command, inherited string)
	walk = func(parent *cobra.Command, inherited string) {
		for _, c := range parent.Commands() {
			class := inherited
			if listed, ok := accountClasses[CommandKey(c)]; ok {
				class = listed
			}
			if class == "" {
				class = ClassGated
			}
			if c.Annotations == nil {
				c.Annotations = map[string]string{}
			}
			c.Annotations[Annotation] = class
			walk(c, class)
		}
	}
	walk(root, "")
}

// CommandClass is c's own stamp, else its nearest stamped ancestor's, else
// gated; a stamp that is not open or serve counts as gated. The root is never
// consulted: InvocationClass decides what the root itself may do.
func CommandClass(c *cobra.Command) string {
	for ; c != nil && c.HasParent(); c = c.Parent() {
		if class, ok := c.Annotations[Annotation]; ok {
			if class == ClassOpen || class == ClassServe {
				return class
			}
			return ClassGated
		}
	}
	return ClassGated
}
