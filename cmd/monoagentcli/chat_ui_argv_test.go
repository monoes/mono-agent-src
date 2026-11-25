package main

import (
	"strings"
	"testing"
)

// The desktop app and the browser extension run the `chat ui` commands with these argument lists (wails-app's
// jsonCLI puts --profile and --json first; the extension's chatBase does the same). Each must parse with the real
// root command: a `chat ui action` flag that shadowed the global --json once made every button press fail, which
// tests against a fake CLI cannot see.
func TestChatUICallersArgvParsesWithTheRealCommandTree(t *testing.T) {
	for name, line := range map[string]string{
		"desktop action":   "--profile default --json chat ui action --action - -- c1",
		"desktop sync":     "--profile default --json chat ui sync-hil -- c1",
		"extension action": "--profile=p-work --json chat ui action --action - -- c1",
		"extension sync":   "--profile=p-work --json chat ui sync-hil -- c1",
		"flag-like id":     "--profile default --json chat ui action --action - -- -flag",
		"show":             "--profile default --json chat ui show -- c1",
		"by hand":          `chat ui action c1 --profile work --action {}`,
	} {
		root := newRootCmd()
		cmd, rest, err := root.Find(strings.Fields(line))
		if err != nil || cmd.Parent() == nil || cmd.Parent().Name() != "ui" {
			t.Fatalf("%s: found %v (%v)", name, cmd.CommandPath(), err)
		}
		if err := cmd.ParseFlags(rest); err != nil {
			t.Fatalf("%s: flags: %v", name, err)
		}
		if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
			t.Errorf("%s: args %q: %v", name, cmd.Flags().Args(), err)
		}
		if err := cmd.ValidateRequiredFlags(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
