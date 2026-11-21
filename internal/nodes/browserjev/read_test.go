package browserjev

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

const readSite = "https://example.com/"

// examplePage answers a11y_tree as the extension does for example.com.
func examplePage(f *browserctltest.Fake) {
	f.On(extension.CmdA11yTree, func(*extension.Command) (any, error) {
		return extension.A11yTreeResult{Nodes: []extension.A11yNode{{
			Role: "RootWebArea", Name: "Example Domain",
			Children: []extension.A11yNode{
				{Role: "heading", Name: "Example Domain"},
				{Role: "paragraph", Children: []extension.A11yNode{{Role: "StaticText", Name: "This domain is for use in documentation examples."}}},
				{Role: "paragraph", Children: []extension.A11yNode{{Role: "link", Name: "Learn more"}}},
			},
		}}}, nil
	})
}

func readRun(ctx context.Context, f *browserctltest.Fake) (map[string]interface{}, error) {
	n := &ReadNode{sender: f}
	out, err := n.Execute(ctx, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"keep": 1})}},
		map[string]interface{}{"url": readSite})
	if err != nil {
		return nil, err
	}
	return out[0].Items[0].JSON, nil
}

// readCommands are all a read may send: tab bookkeeping, the console and
// network marks OpenTab takes for an agent's tab, and the accessibility
// tree. No click, input, eval or CDP.
var readCommands = map[string]bool{
	extension.CmdListTabs: true, extension.CmdPageInfo: true, extension.CmdCreateTab: true, extension.CmdTabGroup: true,
	extension.CmdWatchTab: true, extension.CmdReload: true, extension.CmdWaitLoad: true, extension.CmdA11yTree: true,
	extension.CmdReadConsole: true, extension.CmdReadNetwork: true, extension.CmdCloseTab: true,
}

// opened says whether any tab was created.
func opened(f *browserctltest.Fake) bool { return slices.Contains(f.Types(), extension.CmdCreateTab) }

// A run the user started is held as chat (full by default): the page is read
// through the accessibility tree, the tab is closed, and nothing in the page
// is clicked, typed or evaluated.
func TestBrowserReadReadsAndClosesTheTab(t *testing.T) {
	f := browserctltest.New()
	examplePage(f)
	res, err := readRun(permissionsDB(t, browserctl.KindChat, browserctl.LevelFull), f)
	if err != nil {
		t.Fatal(err)
	}
	if res["keep"] != 1 || res["url"] != readSite || res["title"] != readSite {
		t.Errorf("item: %v", res)
	}
	if res["first_paragraph"] != "This domain is for use in documentation examples." {
		t.Errorf("first_paragraph = %q", res["first_paragraph"])
	}
	if want := "Example Domain\nThis domain is for use in documentation examples.\nLearn more"; res["text"] != want {
		t.Errorf("text = %q, want %q", res["text"], want)
	}
	var tab int
	for _, c := range f.Calls() {
		if c.Type == extension.CmdA11yTree {
			tab = c.TabID
		}
		if !readCommands[c.Type] {
			t.Errorf("a read sent %s", c.Type)
		}
	}
	if _, open := f.Tab(tab); tab == 0 || open {
		t.Errorf("tab %d still open (calls %v)", tab, f.Types())
	}
}

// The levels of the kind the run is held as decide, with the existing
// refusals, before any tab opens.
func TestBrowserReadFollowsTheLevel(t *testing.T) {
	f := browserctltest.New()
	_, err := readRun(permissionsDB(t, browserctl.KindChat, browserctl.LevelOff), f)
	var off *browserctl.BrowserOffError
	if !errors.As(err, &off) || opened(f) {
		t.Fatalf("chat off: %v (opened %v)", err, opened(f))
	}
	f = browserctltest.New()
	_, err = readRun(permissionsDB(t, browserctl.KindChat, browserctl.LevelDev), f)
	if err == nil || !strings.Contains(err.Error(), "https://example.com is not allowed") || opened(f) {
		t.Fatalf("chat dev: %v (opened %v)", err, opened(f))
	}
	f = browserctltest.New()
	examplePage(f)
	if _, err := readRun(permissionsDB(t, browserctl.KindChat, browserctl.LevelDev, "https://example.com=allow"), f); err != nil {
		t.Fatalf("chat dev with an allow rule: %v", err)
	}
}

// A run an org started keeps the org's level (dev by default), never the
// chat's looser one; so does a run from an agent shell (other, dev). That
// browserctl.Open never loosens the caller it is given is pinned by
// TestOpenCallerNeverLoosens and its siblings (internal/browserctl,
// caller_option_test.go).
func TestBrowserReadKeepsTheStricterKind(t *testing.T) {
	const refused = "https://example.com is not allowed"
	f := browserctltest.New()
	ctx := withOrgTrigger(permissionsDB(t, browserctl.KindChat, browserctl.LevelFull))
	if _, err := readRun(ctx, f); err == nil || !strings.Contains(err.Error(), refused) || opened(f) {
		t.Fatalf("org run at org's dev: %v (opened %v)", err, opened(f))
	}
	t.Setenv("CLAUDECODE", "1") // an agent shell: other, dev by default
	f = browserctltest.New()
	if _, err := readRun(permissionsDB(t, browserctl.KindChat, browserctl.LevelFull), f); err == nil || !strings.Contains(err.Error(), refused) || opened(f) {
		t.Fatalf("agent shell, chat full: %v (opened %v)", err, opened(f))
	}
}

// noBrowser is a bridge with no browser attached for the profile.
type noBrowser struct{ *browserctltest.Fake }

func (noBrowser) ResolveTarget(extension.Target) (extension.ConnInfo, error) {
	return extension.ConnInfo{}, extension.ErrNoExtension
}

// No browser bound: the bridge's own error comes back and no tab is asked for.
func TestBrowserReadWithoutABrowser(t *testing.T) {
	f := browserctltest.New()
	n := &ReadNode{sender: noBrowser{f}}
	_, err := n.Execute(permissionsDB(t, browserctl.KindChat, browserctl.LevelFull), workflow.NodeInput{},
		map[string]interface{}{"url": readSite})
	if !errors.Is(err, extension.ErrNoExtension) || opened(f) {
		t.Fatalf("no browser: %v (opened %v)", err, opened(f))
	}
}

// Without a profile the run is refused before the browser is touched; a
// missing url is a config error.
func TestBrowserReadNeedsAProfileAndAURL(t *testing.T) {
	f := browserctltest.New()
	if _, err := readRun(vault.ContextWithDB(context.Background(), nil), f); !errors.Is(err, vault.ErrNoProfile) || len(f.Calls()) > 0 {
		t.Fatalf("no profile: %v (calls %v)", err, f.Types())
	}
	_, err := (&ReadNode{sender: f}).Execute(testCtx(), workflow.NodeInput{}, map[string]interface{}{})
	if !errors.Is(err, workflow.ErrInvalidConfig) {
		t.Fatalf("no url: %v", err)
	}
}

func TestPageTextCutsAtMaxChars(t *testing.T) {
	f := browserctltest.New()
	examplePage(f)
	n := &ReadNode{sender: f}
	out, err := n.Execute(permissionsDB(t, browserctl.KindChat, browserctl.LevelFull), workflow.NodeInput{},
		map[string]interface{}{"url": readSite, "max_chars": float64(7)})
	if err != nil {
		t.Fatal(err)
	}
	if got := out[0].Items[0].JSON; got["text"] != "Example" || got["truncated"] != true {
		t.Errorf("cut: %v", got)
	}
}
