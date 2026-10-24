package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/extension"
)

// extRig is an extension-task agent session: its token names an open task of
// kind, whose queue record (in the context snapshot) pins one tab and origin.
type extRig struct {
	bt  *browserTest
	tab int
	id  string
}

func newExtRig(t *testing.T, kind, tabURL string) *extRig {
	t.Helper()
	bt := newBrowserTest(t, true)
	id := aitask.NewID()
	if _, err := (&aitask.Store{DB: bt.db().DB, Profile: "default"}).Insert(aitask.Task{ID: id, Kind: kind,
		ConversationID: "c", Status: aitask.StatusRunning, Goal: "g", Access: aitask.AccessWorkspace, BudgetUSD: 1}); err != nil {
		t.Fatal(err)
	}
	if err := browserctl.SaveAllowlist(bt.db().DB, []string{browserctl.Origin(tabURL), "https://other.example"}); err != nil { // the person allowed this site
		t.Fatal(err)
	}
	snap := aitask.SnapshotDir(filepath.Dir(bt.cfg.DBPath), id)
	if err := os.MkdirAll(filepath.Join(snap, "posting"), 0o700); err != nil {
		t.Fatal(err)
	}
	other := bt.fake.AddTab("https://other.example/", "Coder · Agent", false) // not the queued tab
	_ = other
	tab := bt.fake.AddTab(tabURL, "Coder · Agent", true)
	rec, _ := json.Marshal(aitask.ExtensionTarget{Tab: tab, Origin: browserctl.Origin(tabURL)})
	if err := os.WriteFile(filepath.Join(snap, filepath.FromSlash(aitask.ExtensionTargetFile)), rec, 0o400); err != nil {
		t.Fatal(err)
	}
	browserctltest.AsApplyTask(t, id) // after the rig's HOME: the record lives under it
	return &extRig{bt: bt, tab: tab, id: id}
}

// The tab comes from the queue record: --tab and --browser naming anything
// else are refused, and with none the queued tab is the one acted on.
func TestExtensionTaskIsPinnedToItsQueuedTab(t *testing.T) {
	r := newExtRig(t, aitask.KindExtensionFill, "https://jobs.example.com/apply")
	for name, args := range map[string][]string{
		"another tab":     {"a11y", "--tab", "999"},
		"another browser": {"a11y", "--browser", "inst-2"},
		"a listing":       {"tabs", "--browser", "inst-2"},
	} {
		if _, err := r.bt.run(args...); err == nil || (name != "a listing" && jsonCode(err) != "tab_pinned") {
			t.Errorf("%s: %v (%s)", name, err, jsonCode(err))
		}
	}
	for _, c := range r.bt.fake.Calls() {
		if c.TabID != 0 && c.TabID != r.tab {
			t.Fatalf("a command reached tab %d, not the queued %d: %s", c.TabID, r.tab, c.Type)
		}
	}
	r.bt.fake.On(extension.CmdA11yTree, func(*extension.Command) (any, error) { return map[string]any{"nodes": []any{}}, nil })
	if _, err := r.bt.run("a11y"); err != nil {
		t.Fatalf("on the queued tab: %v", err)
	}
	if _, err := r.bt.run("a11y", "--tab", itoa(r.tab)); err != nil {
		t.Fatalf("naming the queued tab itself: %v", err)
	}
}

// A tab that moved to another origin since it was queued is not worked on.
func TestExtensionTaskStopsWhenItsTabLeavesTheOrigin(t *testing.T) {
	r := newExtRig(t, aitask.KindExtensionAsk, "https://jobs.example.com/apply")
	r.bt.fake.On(extension.CmdA11yTree, func(*extension.Command) (any, error) { return map[string]any{"nodes": []any{}}, nil })
	r.bt.fake.SetURL(r.tab, "https://jobs.example.com/apply/step-2") // same origin: fine
	if _, err := r.bt.run("a11y"); err != nil {
		t.Fatalf("same origin: %v", err)
	}
	r.bt.fake.SetURL(r.tab, "https://other.example/?d=facts") // allowed by the person, still not the queued origin
	before := len(r.bt.fake.Calls())
	if _, err := r.bt.run("a11y"); err == nil || jsonCode(err) != "tab_moved" {
		t.Fatalf("another origin: %v (%s)", err, jsonCode(err))
	}
	for _, c := range r.bt.fake.Calls()[before:] {
		if c.Type == extension.CmdA11yTree {
			t.Fatal("the page was read after it left its origin")
		}
	}
}

// On a local page a click on a submit control waits for a person for the
// extension kinds; an application-apply task on the same page is unchanged.
func TestExtensionTaskSubmitsAreHeldOnLocalPages(t *testing.T) {
	for kind, wantHeld := range map[string]bool{aitask.KindExtensionFill: true, "application-apply": false} {
		r := newExtRig(t, kind, "http://localhost:3000/transfer")
		fp := &browserctltest.FormPage{Action: "http://localhost:3000/pay", Method: "post", Selector: "#pay-form", Submit: true,
			Fields: []browserctl.FormField{{Name: "amount", Type: "text", Value: "10"}}}
		r.bt.fake.On("eval_cdp", fp.Handler())
		out, err := r.bt.run("click", "#pay")
		held := exitCodeFor(err) == 3 && strings.Contains(out, "awaiting_approval")
		if held != wantHeld {
			t.Errorf("%s: held = %v (%v %s), want %v", kind, held, err, out, wantHeld)
		}
	}
}
