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

// extSession builds an extension task of kind with its queue record and a tab
// on tabURL, and returns the browser test and the ids. vouch makes the process
// the apply-kind agent of that task (its token); without it the process only
// carries the task id in its environment, as a turn whose kind token could not
// be written would.
func extSession(t *testing.T, kind, tabURL string, vouch bool) (bt *browserTest, tab int, id string, snap string) {
	t.Helper()
	bt = newBrowserTest(t, true)
	id = aitask.NewID()
	if _, err := (&aitask.Store{DB: bt.db().DB, Profile: "default"}).Insert(aitask.Task{ID: id, Kind: kind,
		ConversationID: "c", Status: aitask.StatusRunning, Goal: "g", Access: aitask.AccessWorkspace, BudgetUSD: 1}); err != nil {
		t.Fatal(err)
	}
	if err := browserctl.SaveAllowlist(bt.db().DB, []string{browserctl.Origin(tabURL)}); err != nil {
		t.Fatal(err)
	}
	snap = aitask.SnapshotDir(filepath.Dir(bt.cfg.DBPath), id)
	for _, d := range []string{"posting", "facts", "document"} {
		if err := os.MkdirAll(filepath.Join(snap, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(snap), "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	bt.fake.AddTab("https://other.example/", "Coder · Agent", false)
	tab = bt.fake.AddTab(tabURL, "Coder · Agent", true)
	rec, _ := json.Marshal(aitask.ExtensionTarget{Tab: tab, Origin: browserctl.Origin(tabURL)})
	for name, text := range map[string]string{aitask.ExtensionTargetFile: string(rec), "facts/facts.txt": "Name: Ada", "document/cv.pdf": "%PDF cv"} {
		if err := os.WriteFile(filepath.Join(snap, filepath.FromSlash(name)), []byte(text), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	if vouch {
		browserctltest.AsApplyTask(t, id) // after the rig's HOME: the record lives under it
	} else {
		t.Setenv(aitask.EnvTaskID, id)
	}
	bt.fake.On(extension.CmdA11yTree, func(*extension.Command) (any, error) { return map[string]any{"nodes": []any{}}, nil })
	bt.fake.On(extension.CmdSetFiles, func(*extension.Command) (any, error) { return map[string]any{"filesSet": true, "count": 1}, nil })
	return bt, tab, id, snap
}

func pendingHIL(t *testing.T, bt *browserTest) int {
	t.Helper()
	var n int
	if err := bt.db().DB.QueryRow(`SELECT COUNT(*) FROM hil_pending`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The pin must not fail open: a session that is an extension task's but whose
// task cannot be looked up, or that no apply token vouches for, may not use the
// browser at all, and in particular may not reach another tab.
func TestExtensionSessionFailsClosed(t *testing.T) {
	// The task row is gone while the tab sits on another origin.
	bt, tab, id, _ := extSession(t, aitask.KindExtensionFill, "https://jobs.example.com/apply", true)
	if _, err := bt.db().DB.Exec(`DELETE `+`FROM ai_tasks WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	other := bt.fake.AddTab("https://other.example/", "Coder · Agent", false)
	for _, args := range [][]string{{"a11y", "--tab", itoa(other)}, {"a11y"}, {"a11y", "--tab", itoa(tab)}} {
		if out, err := bt.run(args...); err == nil {
			t.Errorf("task row gone, browser %v worked: %s", args, out)
		}
	}
	if _, err := bt.run("upload", "input[type=file]", "/etc/hostname"); err == nil {
		t.Error("task row gone, an unscoped upload worked")
	}

	// The process carries the task id but no apply token (the record could not be written).
	bt, tab, _, _ = extSession(t, aitask.KindExtensionFill, "https://jobs.example.com/apply", false)
	other = bt.fake.AddTab("https://other.example/", "Coder · Agent", false)
	for _, args := range [][]string{{"a11y", "--tab", itoa(other)}, {"a11y"}, {"a11y", "--tab", itoa(tab)}} {
		if out, err := bt.run(args...); err == nil {
			t.Errorf("no apply token, browser %v worked: %s", args, out)
		}
	}
	for _, c := range bt.fake.Calls() {
		if c.TabID == other && c.Type != extension.CmdListTabs {
			t.Fatalf("a command reached the other tab: %s", c.Type)
		}
	}
	// A task that is not a browser kind is not scoped by this.
	bt, _, _, _ = extSession(t, "generic", "http://localhost:3000/", false)
	if _, err := bt.run("a11y"); err != nil {
		t.Errorf("a full-access task's session was refused: %v", err)
	}
}

// A page that moves its tab to an origin of its choosing must not make the agent
// raise an "allow this site" approval for it: the pin is compared first.
func TestPinnedTabRaisesNoSiteApprovalForAnotherOrigin(t *testing.T) {
	bt, tab, _, _ := extSession(t, aitask.KindExtensionAsk, "https://jobs.example.com/apply", true)
	before := pendingHIL(t, bt)
	bt.fake.SetURL(tab, "https://evil-lookalike.example/?d=facts")
	_, err := bt.run("a11y")
	if err == nil || jsonCode(err) != "tab_moved" {
		t.Fatalf("a11y after the tab left its origin: %v (%s)", err, jsonCode(err))
	}
	if n := pendingHIL(t, bt); n != before {
		t.Fatalf("the refusal raised %d approval item(s) naming the page's origin", n-before)
	}
}

// An approved fill or upload is carried out only on the queued tab and origin.
func TestApprovedFillAndUploadMustBeForTheQueuedTab(t *testing.T) {
	bt, tab, _, _ := extSession(t, aitask.KindExtensionFill, "http://localhost:3000/form", true)
	other := bt.fake.AddTab("http://localhost:3000/other", "Coder · Agent", false)
	for name, c := range map[string]struct {
		url string
		tab int
	}{"another tab": {"http://localhost:3000/form", other}, "another origin": {"https://other.example/", tab}} {
		for _, via := range []string{browserctl.ViaFill, browserctl.ViaUpload} {
			it, err := browserctl.CreateSubmitItem(t.Context(), bt.db().DB, browserctl.SubmitRequest{
				Profile: "default", URL: c.url, Via: via, TabID: c.tab, Instance: bt.fake.Instance, Digest: "d"})
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLAUDECODE", "")
			if _, err := runHIL(t, bt.cfg, "approve", it.ID); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLAUDECODE", "1")
			verb := "fill"
			if via == browserctl.ViaUpload {
				verb = "upload"
			}
			if _, err := bt.run(verb, "--hil", it.ID); err == nil || jsonCode(err) != "tab_pinned" {
				t.Errorf("%s/%s: %v (%s)", name, verb, err, jsonCode(err))
			}
		}
	}
}

// An extension task attaches the files of document/ and nothing else in its
// context: not the facts, the page description or the queue record.
func TestExtensionUploadsOnlyFromTheDocumentFolder(t *testing.T) {
	bt, _, _, snap := extSession(t, aitask.KindExtensionFill, "http://localhost:3000/form", true)
	for _, p := range []string{"facts/facts.txt", aitask.ExtensionTargetFile} {
		if _, err := bt.run("upload", "input[type=file]", filepath.Join(snap, filepath.FromSlash(p))); err == nil || jsonCode(err) != "path_not_allowed" {
			t.Errorf("%s: %v (%s)", p, err, jsonCode(err))
		}
	}
	// A registered document passes the scope; the upload then waits for a person
	// (local pages included for these kinds).
	out, err := bt.run("upload", "input[type=file]", filepath.Join(snap, "document", "cv.pdf"))
	if jsonCode(err) == "path_not_allowed" || !strings.Contains(out, "awaiting_approval") {
		t.Fatalf("a registered document: %v %s", err, out)
	}
}
