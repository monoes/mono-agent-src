package browserjev

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/workflow"
)

// originGuard is the fake bridge with the extension's origin check: a
// command stamped with expectOrigin is refused (origin_changed) when its tab
// is on another origin, close_tab included (agent_commands.js checkOrigin).
type originGuard struct{ *browserctltest.Fake }

func (g originGuard) SendCommandTo(t extension.Target, cmd *extension.Command, d time.Duration) (*extension.Response, error) {
	if want, _ := cmd.Params[browserctl.ExpectOriginParam].(string); want != "" {
		if tab, ok := g.Tab(cmd.TabID); ok && browserctl.BrowserOrigin(tab.URL) != want {
			return &extension.Response{ID: cmd.ID, Error: browserctl.OriginChangedCode}, fmt.Errorf("extension error: %s", browserctl.OriginChangedCode)
		}
	}
	return g.Fake.SendCommandTo(t, cmd, d)
}

// openTabs are the tabs the fake still has open, of those created.
func openTabs(f *browserctltest.Fake) []int {
	var open []int
	for _, c := range f.Calls() {
		if c.Type != extension.CmdA11yTree {
			continue
		}
		if _, ok := f.Tab(c.TabID); ok {
			open = append(open, c.TabID)
		}
	}
	return open
}

// The page moves to another origin while it is read: the read fails, and
// the tab is still closed, though a guarded close would be refused there.
func TestBrowserReadClosesATabThatMoved(t *testing.T) {
	f := browserctltest.New()
	f.On(extension.CmdA11yTree, func(cmd *extension.Command) (any, error) {
		f.SetURL(cmd.TabID, "https://elsewhere.example/")
		return nil, errors.New(browserctl.OriginChangedCode)
	})
	n := &ReadNode{sender: originGuard{f}}
	_, err := n.Execute(permissionsDB(t, browserctl.KindChat, browserctl.LevelFull), workflow.NodeInput{}, map[string]interface{}{"url": readSite})
	if err == nil || !strings.Contains(err.Error(), browserctl.OriginChangedCode) {
		t.Fatalf("a moved page: %v", err)
	}
	if open := openTabs(f); len(open) != 0 {
		t.Fatalf("tabs left open: %v", open)
	}
	// The guarded close would have been refused on that tab.
	for _, c := range f.Calls() {
		if c.Type == extension.CmdCloseTab && c.Params[browserctl.ExpectOriginParam] != nil {
			t.Errorf("close_tab carried expectOrigin %v", c.Params[browserctl.ExpectOriginParam])
		}
	}
}

// A close the browser refuses is reported, not swallowed.
func TestBrowserReadReportsAFailedClose(t *testing.T) {
	f := browserctltest.New()
	examplePage(f)
	f.On(extension.CmdCloseTab, func(*extension.Command) (any, error) { return nil, errors.New("tab is busy") })
	_, err := readRun(permissionsDB(t, browserctl.KindChat, browserctl.LevelFull), f)
	if err == nil || !strings.Contains(err.Error(), "still open") || !strings.Contains(err.Error(), "tab is busy") {
		t.Fatalf("a failed close: %v", err)
	}
}

// A cancelled run opens no tab; several items share one read and one tab.
func TestBrowserReadHonoursCancelAndReadsOnce(t *testing.T) {
	f := browserctltest.New()
	examplePage(f)
	ctx, cancel := context.WithCancel(permissionsDB(t, browserctl.KindChat, browserctl.LevelFull))
	cancel()
	if _, err := readRun(ctx, f); !errors.Is(err, context.Canceled) || opened(f) {
		t.Fatalf("cancelled: %v (opened %v)", err, opened(f))
	}
	f = browserctltest.New()
	examplePage(f)
	items := []workflow.Item{workflow.NewItem(map[string]interface{}{"i": 1}), workflow.NewItem(map[string]interface{}{"i": 2}), workflow.NewItem(map[string]interface{}{"i": 3})}
	out, err := (&ReadNode{sender: f}).Execute(permissionsDB(t, browserctl.KindChat, browserctl.LevelFull), workflow.NodeInput{Items: items}, map[string]interface{}{"url": readSite})
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, typ := range f.Types() {
		if typ == extension.CmdCreateTab {
			created++
		}
	}
	got := out[0].Items
	if created != 1 || len(got) != 3 || got[2].JSON["i"] != 2+1 || got[2].JSON["first_paragraph"] == "" {
		t.Fatalf("three items: %d tabs, %v", created, got)
	}
}

// The title falls back to the document's name; the read carries the
// origin it was checked on; max_chars must be a number.
func TestBrowserReadTitleGuardAndConfig(t *testing.T) {
	f := browserctltest.New()
	examplePage(f)
	f.On(extension.CmdListTabs, func(*extension.Command) (any, error) { return map[string]any{"tabs": []any{}}, nil })
	res, err := readRun(permissionsDB(t, browserctl.KindChat, browserctl.LevelFull), originGuard{f}.Fake)
	if err != nil {
		t.Fatal(err)
	}
	if res["title"] != "Example Domain" {
		t.Errorf("title = %q, want the document's name", res["title"])
	}
	for _, c := range f.Calls() {
		if c.Type == extension.CmdA11yTree && c.Params[browserctl.ExpectOriginParam] != "https://example.com" {
			t.Errorf("a11y_tree expectOrigin = %v", c.Params[browserctl.ExpectOriginParam])
		}
	}
	_, err = (&ReadNode{sender: f}).Execute(testCtx(), workflow.NodeInput{}, map[string]interface{}{"url": readSite, "max_chars": "lots"})
	if !errors.Is(err, workflow.ErrInvalidConfig) {
		t.Errorf("max_chars a string: %v", err)
	}
}
