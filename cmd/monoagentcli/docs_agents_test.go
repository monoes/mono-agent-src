package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The documents spell two lists that the CLI gate's table decides: the commands
// that never need a monoes.me sign-in (spec D6) and the serving commands, which
// start even when locked and refuse their work instead (spec §6.4).
// TestOpenListMatchesTheGate ties both lists to the table in both directions, so
// opening or closing a command takes a deliberate edit of the table, these lists
// and the documents.
var (
	openCommands    = []string{"version", "help", "update", "account", "library login", "library logout", "library status", "doomsday stop"}
	servingCommands = []string{"daemon", "httpapi", "mcp", "extension serve"}
)

// listed reports whether key is one of paths or below one (a command inherits its parent's class).
func listed(paths []string, key string) bool {
	for _, p := range paths {
		if key == p || strings.HasPrefix(key, p+" ") {
			return true
		}
	}
	return false
}

func TestOpenListMatchesTheGate(t *testing.T) {
	root := newRootCmd()
	applyClassification(root)
	for class, paths := range map[string][]string{classOpen: openCommands, classServe: servingCommands} {
		for _, p := range paths {
			// cobra adds help and completion only while it executes
			if c, _, err := root.Find(strings.Fields(p)); err == nil && c != root && commandClass(c) != class {
				t.Errorf("%q is documented as %s but the gate classifies it %s", p, class, commandClass(c))
			}
		}
	}
	var walk func(*cobra.Command)
	walk = func(parent *cobra.Command) {
		for _, c := range parent.Commands() {
			switch key := commandKey(c); commandClass(c) {
			case classOpen:
				if !listed(openCommands, key) {
					t.Errorf("%q is open in the gate but not in the documented open list: update the documents and openCommands", key)
				}
			case classServe:
				if !listed(servingCommands, key) {
					t.Errorf("%q is a serving command in the gate but not in the documented list: update the documents and servingCommands", key)
				}
			}
			walk(c)
		}
	}
	walk(root)
}

// assertListText checks that the paragraph of text that starts at marker names every command of paths.
func assertListText(t *testing.T, where, text, marker string, paths []string) {
	t.Helper()
	i := strings.Index(text, marker)
	if i < 0 {
		t.Errorf("%s has no paragraph starting %q", where, marker)
		return
	}
	para := text[i:]
	if j := strings.Index(para, "\n\n"); j > 0 {
		para = para[:j]
	}
	for _, p := range paths {
		if !strings.Contains(flat(para), p) {
			t.Errorf("the paragraph of %s that starts %q does not name %q", where, marker, p)
		}
	}
}

func TestAgentsDocCoversTheAccount(t *testing.T) {
	assertNoStaleClaims(t, "AGENTS.md")
	assertSays(t, "AGENTS.md", append(spelled("`"),
		"## monoes.me account", "### Headless and Docker", refusalLine(), "login_required", "account login --email", "account status --json",
		"account logout", "MONOAGENT_ALLOW_FILE_KEYRING", "MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE", "-tags devaccount", "core.monoes_account",
		"`ref account`", "No credential comes from an environment variable", "daemon restart", graceText(),
		"never copy or restore", "signs out the original and every copy", "services.daemon.restart", "MONOAGENT_DEV_ENFORCE_FROM", "monoes..account",
		"The gate is live")...)
	if strings.Contains(flat(docText(t, "AGENTS.md")), "stored per profile in the encrypted vault") {
		t.Error("AGENTS.md still says the library login is stored per profile in the vault: it uses the machine session")
	}
	agents := docText(t, "AGENTS.md")
	assertListText(t, "AGENTS.md", agents, "**What is open**", openCommands)
	assertListText(t, "AGENTS.md", agents, "**Serving commands**", servingCommands)
	if n := strings.Count(agents, "| `ref account` |"); n != 1 {
		t.Errorf("AGENTS.md lists `ref account` %d times in its ref table, want 1", n)
	}
}

func TestContributingExplainsTheDevelopmentBuild(t *testing.T) {
	assertSays(t, "CONTRIBUTING.md", "-tags devaccount", "scripts/check-release-tags.sh", "accounttest.Install", "MONOAGENT_DEV_ENFORCE_FROM", "internal/accountsmoke")
}
