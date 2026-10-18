package chat

import (
	"regexp"
	"strings"
	"testing"
)

// promptCommand matches a command the Bash prompt shows: a binary name,
// its command group and the subcommand (a scope mention such as
// "monoagentcli org ..." or a quoted "monomind" does not match). Since
// #288 the org allowlist is per subcommand, so the subcommand must be
// part of the match.
var promptCommand = regexp.MustCompile(`\b(?:monomind|monoagentcli) (?:org|workflow) [a-z][a-z-]*`)

// allowedBashCommand reports whether cmd starts with one of
// chatBashPrefixes. It is this test's copy of the real check, which
// monomind makes: the prefixes reach it as --allow-bash-prefix
// (internal/monomind/exec.go), and agent-exec's canUseTool admits a Bash
// command only when it starts with one of its allowBashPrefixes. Keep it a
// literal prefix match like monomind's; a looser check here would pass
// prompt examples that monomind denies.
func allowedBashCommand(cmd string) bool {
	for _, p := range chatBashPrefixes {
		if cmd == p || strings.HasPrefix(cmd, p+" ") {
			return true
		}
	}
	return false
}

// #247: the prompt's workflow example put --profile before the subcommand,
// which the literal prefix allowlist always denied. Every command the
// prompt shows must be one the allowlist runs.
func TestChatBashPromptCommandsAreAllowed(t *testing.T) {
	prompt := chatBashPrompt("/home/u/project", "prof-123")
	cmds := promptCommand.FindAllString(prompt, -1)
	// The prompt's concrete commands: org create-json and workflow create.
	if len(cmds) < 2 {
		t.Fatalf("found only %d commands in the prompt: %v", len(cmds), cmds)
	}
	for _, c := range cmds {
		if !allowedBashCommand(c) {
			t.Errorf("prompt shows %q, which the Bash allowlist %v denies", c, chatBashPrefixes)
		}
	}
	for _, want := range []string{
		"monoagentcli workflow create <name> --profile prof-123",
		`monoagentcli org create-json <name> --project "/home/u/project"`,
		`with --project "/home/u/project" on every call`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	// The checker itself rejects the form #247 reported.
	if allowedBashCommand("monoagentcli --profile") || !allowedBashCommand("monoagentcli workflow") {
		t.Fatal("allowedBashCommand is wrong")
	}
	if strings.Contains(prompt, "%!") {
		t.Fatalf("prompt has a formatting error: %s", prompt)
	}
}

// #288 (review of #295): the chat assistant can't reach the operator's own
// org commands — signing, full-access grants, quarantine approvals, and
// monomind's create (it signs what it writes) — through either binary,
// nor monomind's own approve, gate-approve and answer, which skip the check
// `monoagentcli org` makes, while the org work it does is still allowed.
func TestChatBashCantSignOrGrant(t *testing.T) {
	for _, cmd := range []string{
		"monomind org sign growth --yes", "monomind org sign --all --yes", "monomind org sign growth",
		"monomind org role set-access growth lead full --yes-i-understand",
		"monomind org approve-paths .mcp.json", "monomind org create growth",
		"monoagentcli org sign growth --yes --expect-hash abc", "monoagentcli org sign growth",
		"monoagentcli org role set-access growth lead full", "monoagentcli org --project /p sign growth --yes",
		"monomind org", "monoagentcli org",
		// Settling an org's item with no check of whether a message to a
		// person goes out (#831 follow-up N1c): the chat uses monoagentcli.
		"monomind org approve growth lead act", "monomind org gate-approve growth gate-1",
		"monomind org answer growth q-1 yes",
		// Revoking a profile's keys, grants and endpoints is the user's own.
		"monoagentcli org teardown-profile", "monoagentcli org teardown-profile --dry-run",
	} {
		if allowedBashCommand(cmd) {
			t.Errorf("chat Bash allows %q", cmd)
		}
	}
	for _, cmd := range []string{
		"monoagentcli org create-json x --project /p --json {}", "monoagentcli org status growth",
		"monoagentcli org automation add growth --workflow w --alias a", "monoagentcli org automation-role add growth",
		"monoagentcli org grant add growth --role r --automation a", "monoagentcli org autonomy set growth --level mid",
		"monomind org deny growth lead act", "monomind org gate-reject growth gate-1", "monomind org approvals growth",
		"monoagentcli org approve growth lead act", "monoagentcli org answer growth q-1 yes",
		"monomind org status", "monomind org run growth",
		"monoagentcli workflow create x --profile p",
	} {
		if !allowedBashCommand(cmd) {
			t.Errorf("chat Bash denies %q", cmd)
		}
	}
}
