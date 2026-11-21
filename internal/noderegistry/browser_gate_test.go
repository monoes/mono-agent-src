package noderegistry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// browserDrivenElsewhere are the registered node types that drive the
// user's browser without being a package's BrowserNode, each with the gate
// that keeps an agent's runs of it in check, reviewed.
var browserDrivenElsewhere = map[string]string{
	"browser.jev":  "the agent browser permission levels (internal/browserctl, nodes/browserjev/gate.go)",
	"browser.read": "browserctl.Open held as the run's AI caller's kind, else chat (nodes/browserjev/read.go)",
}

type gateTabs struct{ calls int }

func (p *gateTabs) GetPage(context.Context, string, string) (browser.PageInterface, error) {
	p.calls++
	return nil, fmt.Errorf("no browser in tests")
}

// Every registered node type that drives the user's browser is held for an
// agent: each package BrowserNode of a package that isn't built-in refuses
// an agent's run before a tab opens, and any other browser-driving type is
// one reviewed in browserDrivenElsewhere, so a new one fails here until its
// gate is named. The registry here has a package an agent could write by
// hand (a folder under ~/.monoagent/actions).
func TestEveryBrowserDrivingNodeIsHeldForAnAgent(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "actions", "handmade")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"press.json": `{"actionType":"press","steps":[{"id":"go","type":"click","selector":"#go"}]}`,
		"peek.json":  `{"actionType":"peek","sideEffects":"read","steps":[{"id":"t","type":"extract_text","selector":"h1"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := nodes.BootAutomations(home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { action.SetDefSource(nil); nodes.SetGlobalSessionProvider(nil) })
	r := Build(nil)
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDECODE", "1")

	names := r.Types()
	for alias := range r.Aliases() {
		names = append(names, alias)
	}
	gated := 0
	for _, typ := range names {
		f, ok := r.Get(typ)
		if !ok {
			continue // an alias of a type that isn't registered
		}
		bn, isBrowserNode := f().(*nodes.BrowserNode)
		switch {
		case isBrowserNode:
			tabs := &gateTabs{}
			nodes.SetGlobalSessionProvider(tabs)
			_, err := bn.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{})
			if !errors.Is(err, nodes.ErrUserOnlyBrowserRun) || tabs.calls != 0 {
				t.Errorf("%s: an agent's run: %v (tabs %d), want ErrUserOnlyBrowserRun before a tab", typ, err, tabs.calls)
			}
			gated++
		case strings.HasPrefix(typ, "browser."):
			if browserDrivenElsewhere[typ] == "" {
				t.Errorf("%s drives the browser and no gate for an agent's runs of it is reviewed: hold it, or name its gate in browserDrivenElsewhere", typ)
			}
		}
	}
	if gated < 4 {
		t.Fatalf("only %d browser actions checked: the hand-written package was not registered under both names?", gated)
	}
}
