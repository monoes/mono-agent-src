package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/zalando/go-keyring"
)

// runAutomationCLI runs `monoagentcli <args>` against a throwaway HOME and
// returns stdout and stderr separately (stdout must stay pure JSON).
func runAutomationCLI(t *testing.T, home string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("MONOAGENT_TEST_NO_BROWSER", "1")
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(append(args, "--db-path", filepath.Join(home, ".monoagent", "monoagent.db")))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// mustJSON runs the command, requires success and decodes stdout into v.
func mustJSON(t *testing.T, home string, v any, args ...string) {
	t.Helper()
	out, errOut, err := runAutomationCLI(t, home, append(args, "--json")...)
	if err != nil {
		t.Fatalf("%v: %v\nstdout: %s\nstderr: %s", args, err, out, errOut)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("%v: stdout is not JSON: %v\n%s", args, err, out)
	}
}

func TestAutomationListJSONShape(t *testing.T) {
	home := t.TempDir()
	var got struct {
		Automations []map[string]json.RawMessage `json:"automations"`
	}
	mustJSON(t, home, &got, "automation", "list")
	if len(got.Automations) == 0 {
		t.Fatal("no automations listed; built-ins should be seeded")
	}
	found := false
	for _, a := range got.Automations {
		for _, k := range []string{"id", "version", "source", "enabled", "available", "actions", "session", "pendingUpdate"} {
			if _, ok := a[k]; !ok {
				t.Errorf("row missing %q: %v", k, a)
			}
		}
		var s map[string]json.RawMessage
		if err := json.Unmarshal(a["session"], &s); err != nil {
			t.Fatalf("session: %v", err)
		}
		for _, k := range []string{"loggedIn", "username", "expiresAt", "status"} {
			if _, ok := s[k]; !ok {
				t.Errorf("session missing %q", k)
			}
		}
		if string(a["id"]) == `"hackernews"` {
			found = true
		}
	}
	if !found {
		t.Error("hackernews not listed")
	}
}

func TestAutomationListSessionFromCrawlerSessions(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()
	dbPath := filepath.Join(home, ".monoagent", "monoagent.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := upsertSessionRow(context.Background(), db.DB, "default", "hackernews", "pg", []byte(`[]`)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var got struct {
		Automations []automationListRow `json:"automations"`
	}
	mustJSON(t, home, &got, "automation", "list")
	for _, a := range got.Automations {
		if a.ID == "hackernews" {
			if !a.Session.LoggedIn || a.Session.Username != "pg" || a.Session.ExpiresAt == "" || a.Session.Status != "active" {
				t.Fatalf("session = %+v, want logged in as pg", a.Session)
			}
			return
		}
	}
	t.Fatal("hackernews not listed")
}

func TestAutomationShowJSONShape(t *testing.T) {
	home := t.TempDir()
	var got struct {
		Info      *automation.InstalledInfo `json:"info"`
		Manifest  automation.Manifest       `json:"manifest"`
		Actions   []automationActionJSON    `json:"actions"`
		Fragments []string                  `json:"fragments"`
		Issues    []automation.IssueJSON    `json:"issues"`
	}
	mustJSON(t, home, &got, "automation", "show", "hackernews")
	if got.Info == nil || got.Info.ID != "hackernews" || got.Manifest.ID != "hackernews" {
		t.Fatalf("info/manifest = %+v / %+v", got.Info, got.Manifest)
	}
	if got.Fragments == nil || got.Issues == nil {
		t.Error("fragments and issues must be arrays, not null")
	}
	if len(got.Actions) != len(got.Manifest.Actions) {
		t.Fatalf("%d actions, manifest lists %d", len(got.Actions), len(got.Manifest.Actions))
	}
	for _, a := range got.Actions {
		if a.NodeType != "hackernews."+a.Name {
			t.Errorf("nodeType %q for %q", a.NodeType, a.Name)
		}
		if a.Inputs == nil || a.Outputs == nil || a.Visibility == nil {
			t.Errorf("%s: inputs/outputs/visibility must be arrays", a.Name)
		}
	}
}

// Each action lists what running it can reveal (visibility), [] when nothing.
func TestAutomationShowJSONVisibility(t *testing.T) {
	home := t.TempDir()
	var got struct {
		Actions []automationActionJSON `json:"actions"`
	}
	mustJSON(t, home, &got, "automation", "show", "linkedin")
	vis := map[string][]string{}
	for _, a := range got.Actions {
		if a.Visibility == nil {
			t.Fatalf("%s: visibility must be an array", a.Name)
		}
		vis[a.Name] = a.Visibility
	}
	if v := vis["scrape_profile_info"]; len(v) != 1 || v[0] != "profile_view_visible_to_owner" {
		t.Errorf("scrape_profile_info visibility = %v", v)
	}
	if v := vis["find_by_keyword"]; len(v) != 1 || v[0] != "search_may_be_saved" {
		t.Errorf("find_by_keyword visibility = %v", v)
	}
	if v := vis["list_post_comments"]; len(v) != 0 {
		t.Errorf("list_post_comments visibility = %v, want []", v)
	}
}

func TestAutomationShowUnknownPrintsJSONError(t *testing.T) {
	home := t.TempDir()
	out, _, err := runAutomationCLI(t, home, "automation", "show", "no-such-automation", "--json")
	if err == nil {
		t.Fatal("expected an error")
	}
	var e map[string]string
	if jerr := json.Unmarshal([]byte(out), &e); jerr != nil || e["error"] == "" {
		t.Fatalf("stdout = %q, want {\"error\":…}", out)
	}
}

func TestAutomationExportUninstallInstallRestore(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "gemini.mpkg")

	var exp struct{ File, Sha256 string }
	mustJSON(t, home, &exp, "automation", "export", "gemini", "-o", file)
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if exp.Sha256 != hex.EncodeToString(sum[:]) || exp.File == "" {
		t.Fatalf("export result %+v does not match the file", exp)
	}

	var un struct {
		OK   bool                      `json:"ok"`
		Info *automation.InstalledInfo `json:"info"`
	}
	mustJSON(t, home, &un, "automation", "uninstall", "gemini")
	if !un.OK {
		t.Fatal("uninstall not ok")
	}

	// Non-interactive without --yes must refuse, with a JSON error.
	out, _, err := runAutomationCLI(t, home, "automation", "install", file, "--json")
	if err == nil || !strings.Contains(out, "confirmation required") {
		t.Fatalf("install without --yes: err=%v stdout=%s", err, out)
	}

	var dry automation.InstallResult
	mustJSON(t, home, &dry, "automation", "install", file, "--dry-run")
	if !dry.DryRun || dry.Installed || dry.ID != "gemini" {
		t.Fatalf("dry run = %+v", dry)
	}
	sum = sha256.Sum256(b)
	if dry.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("dry run sha256 %q, file %x", dry.SHA256, sum)
	}
	// A wrong pin is refused; the reviewed hash installs.
	if _, _, err := runAutomationCLI(t, home, "automation", "install", file, "--yes", "--json",
		"--expect-sha256", strings.Repeat("0", 64)); err == nil {
		t.Fatal("install with a wrong --expect-sha256 succeeded")
	}

	var inst automation.InstallResult
	mustJSON(t, home, &inst, "automation", "install", file, "--yes", "--expect-sha256", dry.SHA256)
	if !inst.Installed || inst.ID != "gemini" {
		t.Fatalf("install = %+v", inst)
	}

	mustJSON(t, home, &un, "automation", "uninstall", "gemini")
	// The binary ships no built-in copy any more: restore points at the
	// library instead (exit 2, not_found).
	out, _, err = runAutomationCLI(t, home, "automation", "restore", "gemini", "--json")
	if exitCodeFor(err) != 2 || !strings.Contains(out, "library install automation gemini") || !strings.Contains(out, "library login") {
		t.Fatalf("restore: err=%v stdout=%s", err, out)
	}
}

// writeLegacyAction writes a pre-package ActionDef file (platform field).
func writeLegacyAction(t *testing.T, dir, platform, name string) string {
	t.Helper()
	def := map[string]any{
		"actionType": name, "platform": platform, "description": "test action",
		"sideEffects": "none",
		"steps": []map[string]any{
			{"id": "open", "type": "navigate", "url": "https://acme.test/home"},
			{"id": "title", "type": "extract_text", "selector": "h1"},
		},
	}
	b, _ := json.MarshalIndent(def, "", "  ")
	p := filepath.Join(dir, name+".json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestActionTemplateInstallAliasAndLifecycle(t *testing.T) {
	home := t.TempDir()
	file := writeLegacyAction(t, home, "ACME", "ping")

	out, errOut, err := runAutomationCLI(t, home, "action", "template", "install", file, "--json")
	if err != nil {
		t.Fatalf("template install: %v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(errOut, "deprecated") {
		t.Errorf("no deprecation note on stderr: %q", errOut)
	}
	var res automation.InstallResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.ID != "acme" || !res.Installed {
		t.Fatalf("result %s (err %v)", out, err)
	}

	var info struct {
		OK   bool                      `json:"ok"`
		Info *automation.InstalledInfo `json:"info"`
	}
	mustJSON(t, home, &info, "automation", "disable", "acme")
	if info.Info == nil || info.Info.Enabled {
		t.Fatalf("disable: %+v", info.Info)
	}
	mustJSON(t, home, &info, "automation", "enable", "acme")
	if info.Info == nil || !info.Info.Enabled || info.Info.Source != automation.SourceLocal {
		t.Fatalf("enable: %+v", info.Info)
	}
}

func TestActionExportImport(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "one.mpkg")
	var exp map[string]string
	mustJSON(t, home, &exp, "action", "export", "hackernews.list_comments", "-o", file)
	if exp["file"] == "" {
		t.Fatalf("export = %v", exp)
	}
	src, err := automation.OpenFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Manifest.Actions) != 1 || src.Manifest.Actions[0] != "list_comments" {
		t.Fatalf("exported actions = %v", src.Manifest.Actions)
	}

	out, _, err := runAutomationCLI(t, home, "action", "import", file, "--into", "hn-copy", "--json")
	if err == nil || !strings.Contains(out, "confirmation required") {
		t.Fatalf("import without --yes: err=%v out=%s", err, out)
	}
	var res automation.InstallResult
	mustJSON(t, home, &res, "action", "import", file, "--into", "hn-copy", "--yes")
	if res.ID != "hn-copy" || !res.Installed {
		t.Fatalf("import = %+v", res)
	}
}

func TestAutomationValidateActionFileAndTest(t *testing.T) {
	home := t.TempDir()
	file := writeLegacyAction(t, home, "acme", "ping")
	var v struct {
		OK     bool                   `json:"ok"`
		Issues []automation.IssueJSON `json:"issues"`
	}
	mustJSON(t, home, &v, "automation", "validate", file)
	if v.Issues == nil {
		t.Error("issues must be an array")
	}

	var tr struct {
		Results []fixtureResult `json:"results"`
	}
	mustJSON(t, home, &tr, "automation", "test", "hackernews")
	if len(tr.Results) == 0 || !strings.HasPrefix(tr.Results[0].Message, "validate:") {
		t.Fatalf("results = %+v", tr.Results)
	}
	for _, r := range tr.Results[1:] {
		// No browser in tests: a fixture is skipped (and ok), never passed.
		if r.Status != "skipped" || !r.OK {
			t.Errorf("fixture %s: status %q ok %v (%s)", r.Fixture, r.Status, r.OK, r.Message)
		}
	}

	if _, _, err := runAutomationCLI(t, home, "automation", "test", "hackernews", "--live", "--json"); err == nil {
		t.Error("--live should report not supported")
	}
}

func TestAutomationNewValidatePack(t *testing.T) {
	root, err := automationTemplatesRoot()
	if err != nil {
		t.Skip("templates not bundled:", err)
	}
	names := automationTemplateNames(root)
	if len(names) == 0 {
		t.Skip("no templates bundled yet")
	}
	home := t.TempDir()
	for _, tmpl := range names {
		dir := filepath.Join(home, "pkg-"+tmpl)
		var n map[string]string
		mustJSON(t, home, &n, "automation", "new", "acme-"+tmpl, "--template", tmpl,
			"--dir", dir, "--name", "Acme", "--start-url", "https://app.acme.test/")
		if n["dir"] == "" {
			t.Fatalf("%s: new = %v", tmpl, n)
		}
		p, err := automation.OpenDir(dir)
		if err != nil {
			t.Fatalf("%s: %v", tmpl, err)
		}
		if p.Manifest.ID != "acme-"+tmpl || p.Manifest.Site.StartURL != "https://app.acme.test/" {
			t.Errorf("%s: manifest %+v", tmpl, p.Manifest)
		}
		var v struct {
			OK     bool                   `json:"ok"`
			Issues []automation.IssueJSON `json:"issues"`
		}
		mustJSON(t, home, &v, "automation", "validate", dir)
		if !v.OK {
			t.Errorf("%s: scaffold does not validate: %+v", tmpl, v.Issues)
			continue
		}
		var pk map[string]string
		mustJSON(t, home, &pk, "automation", "pack", dir, "-o", dir+".mpkg")
		if pk["sha256"] == "" {
			t.Errorf("%s: pack = %v", tmpl, pk)
		}
	}
}

func TestAutomationInstallInvalidReportsIssues(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "bad")
	if err := os.MkdirAll(filepath.Join(dir, "actions"), 0o755); err != nil {
		t.Fatal(err)
	}
	// An imported package without site.domains is invalid.
	manifest := `{"schema":"monoagent.automation/v1","id":"bad-pkg","name":"Bad","version":"0.1.0",
		"site":{"startUrl":"","domains":[]},"permissions":{"steps":["log"],"scripts":[]},
		"actions":["a"],"policy":{"tier":"standard"}}`
	action := `{"actionType":"a","automation":"bad-pkg","steps":[{"id":"s","type":"log"}]}`
	os.WriteFile(filepath.Join(dir, "automation.json"), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(dir, "actions", "a.json"), []byte(action), 0o644)

	out, _, err := runAutomationCLI(t, home, "automation", "install", dir, "--yes", "--json")
	if err == nil {
		t.Fatal("expected install to fail")
	}
	var body struct {
		Error  string                    `json:"error"`
		Issues []automation.IssueJSON    `json:"issues"`
		Result *automation.InstallResult `json:"result"`
	}
	if jerr := json.Unmarshal([]byte(out), &body); jerr != nil {
		t.Fatalf("stdout %q: %v", out, jerr)
	}
	if body.Error == "" || len(body.Issues) == 0 || body.Result == nil {
		t.Fatalf("error body = %+v", body)
	}
}

func TestAutomationValidateBuiltinDir(t *testing.T) {
	home := t.TempDir()
	// A package without domains is valid only as a built-in.
	dir := filepath.Join(home, "data", "automations", "plain")
	if err := os.MkdirAll(filepath.Join(dir, "actions"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":"monoagent.automation/v1","id":"plain","name":"Plain","version":"1.0.0",
		"site":{"startUrl":"","domains":[]},"permissions":{"steps":[],"scripts":[]},
		"actions":["a"],"policy":{"tier":"standard"}}`
	action := `{"actionType":"a","automation":"plain","sideEffects":"none","steps":[{"id":"s","type":"log"}]}`
	os.WriteFile(filepath.Join(dir, "automation.json"), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(dir, "actions", "a.json"), []byte(action), 0o644)
	if !isBuiltinSourceDir(dir) {
		t.Fatal("dir under data/automations not detected")
	}
	// The current layout: <checkout>/automations/<id> next to embed.go.
	moved := filepath.Join(home, "automations", "plain")
	os.MkdirAll(moved, 0o755)
	if isBuiltinSourceDir(moved) {
		t.Fatal("automations/<id> without embed.go (e.g. an installed registry) must not count as the official source")
	}
	os.WriteFile(filepath.Join(home, "automations", "embed.go"), []byte("package automations\n"), 0o644)
	if !isBuiltinSourceDir(moved) {
		t.Fatal("automations/<id> in a checkout not detected")
	}
	var v struct {
		OK     bool                   `json:"ok"`
		Issues []automation.IssueJSON `json:"issues"`
	}
	mustJSON(t, home, &v, "automation", "validate", dir)
	if !v.OK {
		t.Fatalf("as built-in: %+v", v.Issues)
	}
	other := filepath.Join(home, "plain")
	if err := os.Rename(dir, other); err != nil {
		t.Fatal(err)
	}
	out, _, _ := runAutomationCLI(t, home, "automation", "validate", other, "--json")
	if !strings.Contains(out, `"ok": false`) {
		t.Fatalf("as local package it should fail: %s", out)
	}
	mustJSON(t, home, &v, "automation", "validate", other, "--builtin")
	if !v.OK {
		t.Fatalf("--builtin: %+v", v.Issues)
	}
}
