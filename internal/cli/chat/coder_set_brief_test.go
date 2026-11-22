package chat

import (
	"bytes"
	"strings"
	"testing"
)

// With a Jev key set, Jev gets a worker's brief under every router: auto and
// jev route with it, and the staffer asks Jev for the model and effort the
// lead leaves open, with --org-router model too (internal/dynorg staff.go).
// coder set's help must not say a router keeps briefs local (security
// review of #871); only removing the Jev key does.
func TestCoderSetHelpSaysWhereBriefsGo(t *testing.T) {
	cmd := newCoderSetCmd(testDeps(&testConfig{DBPath: t.TempDir() + "/x.db", ProfileID: "default"}))
	var help bytes.Buffer
	cmd.SetOut(&help)
	cmd.SetArgs([]string{"--help"})
	_ = cmd.Execute()
	h := strings.Join(strings.Fields(help.String()), " ")
	for _, wrong := range []string{"keeps briefs local", "keep briefs local", "Only removing the Jev key keeps briefs on this machine"} {
		if strings.Contains(h, wrong) {
			t.Errorf("coder set --help still says %q:\n%s", wrong, h)
		}
	}
	for _, want := range []string{
		"Whenever a decision provider resolves (the dynorg surface's, else the machine default: TypeSafe with a Jev key set, or a provider you added), a worker's brief goes to it whatever app access and the router say",
		"Briefs stay on this machine only when no Jev key or provider resolves, or when that provider's host is this computer (and even then the local server may forward them).",
		"whenever a decision provider resolves (a Jev key set, or the dynorg surface's provider) a worker's brief still goes to it, whatever the router",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("coder set --help misses %q:\n%s", want, h)
		}
	}
}
