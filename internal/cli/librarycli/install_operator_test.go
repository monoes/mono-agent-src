package librarycli

import (
	"bytes"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// An agent may not install an automation (or an API package) from the library; a
// dry run, which changes nothing, stays open.
func TestInstallOfCodeIsOperatorOnly(t *testing.T) {
	f := newFixture(t)
	f.fake.Add("monoes", "automation", "hackernews", "Hacker News", "official", "1.1.0", pack(t, "hackernews"),
		map[string]any{"automation_id": "hackernews"})
	f.must(nil, "login")
	run := func(args ...string) error {
		d := f.deps(true)
		d.Refusal = func() string { return "an agent is running this" }
		cmd := NewCommand(d)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		return cmd.ExecuteContext(t.Context())
	}
	if err := run("install", "automation", "hackernews", "--yes"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("install by an agent: %v", err)
	}
	if err := run("install", "automation", "hackernews", "--dry-run"); err != nil {
		t.Fatalf("dry run by an agent: %v", err)
	}
}
