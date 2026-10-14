package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/automation"
)

// bundleTestWorkflow is a workflow using the acme-test automation.
const bundleTestWorkflow = `{
  "name": "bundle-wf",
  "nodes": [
    {"id": "t", "type": "trigger.manual", "name": "Trigger", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "a", "type": "acme-test.get_title", "name": "Title", "position": {"x": 200, "y": 0}, "config": {}}
  ],
  "connections": [{"id": "t-a", "source": "t", "target": "a"}]
}`

// TestWorkflowBundleRoundTrip: export with --bundle-automations on one
// machine, import into a fresh HOME: --json without --yes reports the
// package as missing, --yes installs it.
func TestWorkflowBundleRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installTestAutomation(t)
	cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "src.db"), JSONOutput: true, ProfileID: "default"}

	src := filepath.Join(t.TempDir(), "wf.json")
	if err := os.WriteFile(src, []byte(bundleTestWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	var imported struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "import", "--file", src)), &imported); err != nil {
		t.Fatal(err)
	}
	bundled := filepath.Join(t.TempDir(), "bundled.json")
	runWorkflowSubcmd(t, cfg, "export", imported.ID, "--bundle-automations", "-o", bundled)

	raw, err := os.ReadFile(bundled)
	if err != nil {
		t.Fatal(err)
	}
	var doc workflowBundleFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Automations) != 1 || doc.Automations["acme-test"].Mpkg == "" || doc.Automations["acme-test"].Version != "1.0.0" {
		t.Fatalf("bundle should hold exactly acme-test (not trigger): %v", keysOf(doc.Automations))
	}
	// Backward compatible: the plain workflow parser still reads it.
	if wf, err := parseWorkflowDefinition(raw); err != nil || len(wf.Nodes) != 2 {
		t.Fatalf("plain parse of a bundled file: %v (%d nodes)", err, len(wf.Nodes))
	}
	// Without the flag nothing is bundled.
	plain := filepath.Join(t.TempDir(), "plain.json")
	runWorkflowSubcmd(t, cfg, "export", imported.ID, "-o", plain)
	var plainDoc workflowBundleFile
	if b, _ := os.ReadFile(plain); json.Unmarshal(b, &plainDoc) != nil || plainDoc.Automations != nil {
		t.Fatal("export without --bundle-automations must not add an automations field")
	}

	// A fresh machine.
	t.Setenv("HOME", t.TempDir())
	cfg2 := &globalConfig{DBPath: filepath.Join(t.TempDir(), "dst.db"), JSONOutput: true, ProfileID: "default"}
	report := func(out string) map[string]string {
		t.Helper()
		var res struct {
			Automations []bundleImportItem `json:"automations"`
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("parse import output %q: %v", out, err)
		}
		m := map[string]string{}
		for _, it := range res.Automations {
			m[it.ID] = it.Status + it.Error
		}
		return m
	}
	missingOut := runWorkflowSubcmd(t, cfg2, "import", "--file", bundled)
	if got := report(missingOut); got["acme-test"] != "missing" {
		t.Fatalf("--json without --yes should report missing, got %v", got)
	}
	// A missing package carries its dry-run review, as text and as data.
	var withReview struct {
		Automations []bundleImportItem `json:"automations"`
	}
	if err := json.Unmarshal([]byte(missingOut), &withReview); err != nil || len(withReview.Automations) != 1 {
		t.Fatalf("parse %q: %v", missingOut, err)
	}
	it := withReview.Automations[0]
	if !strings.HasPrefix(it.Review, "Bundled automation acme-test 1.0.0") {
		t.Errorf("review = %q", it.Review)
	}
	d := it.ReviewDetail
	if d == nil || d.ID != "acme-test" || d.Version != "1.0.0" || len(d.Domains) == 0 || len(d.Capabilities) == 0 || d.Replaces != nil {
		t.Errorf("reviewDetail = %+v", d)
	}
	// The install review's confirmation fields ride along (not needed here).
	if d != nil && (d.ReplaceRequired || d.TrustChange != nil || d.Visibility == nil) {
		t.Errorf("reviewDetail confirmation fields = %+v", d)
	}
	if !strings.Contains(missingOut, `"visibility"`) || !strings.Contains(missingOut, `"replaceRequired"`) {
		t.Errorf("reviewDetail JSON lacks visibility/replaceRequired: %s", missingOut)
	}
	reg, err := openAutomationRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Info("acme-test"); err == nil {
		t.Fatal("nothing may be installed without --yes")
	}
	if got := report(runWorkflowSubcmd(t, cfg2, "import", "--file", bundled, "--yes")); got["acme-test"] != "installed" {
		t.Fatalf("--yes should install, got %v", got)
	}
	info, err := reg.Info("acme-test")
	if err != nil || info.Version != "1.0.0" {
		t.Fatalf("acme-test after bundle import: %+v, %v", info, err)
	}
	if got := report(runWorkflowSubcmd(t, cfg2, "import", "--file", bundled, "--yes")); got["acme-test"] != "present" {
		t.Fatalf("an installed package should be reported present, got %v", got)
	}
}

// packBundle packs the fixture package with manifest id pkgID into a
// bundle entry.
func packBundle(t *testing.T, pkgID string) bundledAutomation {
	t.Helper()
	var buf bytes.Buffer
	if err := automation.Pack(writeTestAutomationID(t, pkgID), &buf); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return bundledAutomation{Version: "1.0.0", SHA256: hex.EncodeToString(sum[:]),
		Mpkg: base64.StdEncoding.EncodeToString(buf.Bytes())}
}

func bundleDoc(t *testing.T, entries map[string]bundledAutomation) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"name": "x", "nodes": []any{}, "connections": []any{}, "automations": entries})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustNotBeInstalled(t *testing.T, id string) {
	t.Helper()
	reg, err := openAutomationRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Info(id); err == nil {
		t.Fatalf("%s must not be installed", id)
	}
}

func TestWorkflowBundleRefusesBadSHA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	good := packBundle(t, "acme-test")
	tampered := good
	tampered.SHA256 = strings.Repeat("0", 64)
	unpinned := good
	unpinned.SHA256 = ""
	for name, b := range map[string]bundledAutomation{"mismatch": tampered, "missing": unpinned} {
		var log bytes.Buffer
		items := handleBundledAutomations(bundleDoc(t, map[string]bundledAutomation{"acme-test": b}),
			bundleImportOptions{yes: true, out: &log})
		if len(items) != 1 || items[0].Status != "failed" {
			t.Fatalf("%s sha256: %+v", name, items)
		}
		mustNotBeInstalled(t, "acme-test")
	}
}

func TestWorkflowBundleRefusesKeyIDMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// The bundle key says "harmless" but the package inside is acme-test.
	var log bytes.Buffer
	items := handleBundledAutomations(bundleDoc(t, map[string]bundledAutomation{"harmless": packBundle(t, "acme-test")}),
		bundleImportOptions{yes: true, out: &log})
	if len(items) != 1 || items[0].Status != "failed" || !strings.Contains(items[0].Error, "declares id") {
		t.Fatalf("key/id mismatch: %+v", items)
	}
	mustNotBeInstalled(t, "harmless")
	mustNotBeInstalled(t, "acme-test")
}

func TestWorkflowBundleNeverReplacesBuiltin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	reg, err := openAutomationRegistry()
	if err != nil {
		t.Fatal(err)
	}
	infos, err := reg.List(false)
	if err != nil {
		t.Fatal(err)
	}
	var builtin automation.InstalledInfo
	for _, in := range infos {
		if in.Source == automation.SourceBuiltin {
			builtin = in
			break
		}
	}
	if builtin.ID == "" {
		t.Skip("no built-in automations seeded")
	}
	entries := map[string]bundledAutomation{builtin.ID: packBundle(t, builtin.ID)}
	var log bytes.Buffer
	// Same id and version, other content: reported "differs", and even the
	// explicit --replace-automations opt-in never replaces a built-in.
	items := handleBundledAutomations(bundleDoc(t, entries), bundleImportOptions{yes: true, replace: true, out: &log})
	if len(items) != 1 || items[0].Status != "differs" || !strings.Contains(items[0].Error, "built-in") ||
		!items[0].Builtin || items[0].Replaceable == nil || *items[0].Replaceable || items[0].Hint == "" {
		t.Fatalf("installed built-in: %+v", items)
	}
	after, err := reg.Info(builtin.ID)
	if err != nil || after.Source != automation.SourceBuiltin || after.Version != builtin.Version {
		t.Fatalf("built-in %s was replaced: %+v %v", builtin.ID, after, err)
	}
	// An uninstalled built-in is a conflict, not an install slot.
	if err := reg.Uninstall(builtin.ID); err != nil {
		t.Fatal(err)
	}
	items = handleBundledAutomations(bundleDoc(t, entries), bundleImportOptions{yes: true, out: &log})
	if len(items) != 1 || items[0].Status != "conflict" {
		t.Fatalf("removed built-in: %+v", items)
	}
	all, err := reg.List(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range all {
		if in.ID == builtin.ID && (!in.Removed || in.Source != automation.SourceBuiltin) {
			t.Fatalf("removed built-in %s was touched: %+v", builtin.ID, in)
		}
	}
}

func TestWorkflowBundlePrintsReviewLineWithYes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var log bytes.Buffer
	items := handleBundledAutomations(bundleDoc(t, map[string]bundledAutomation{"acme-test": packBundle(t, "acme-test")}),
		bundleImportOptions{yes: true, out: &log})
	if len(items) != 1 || items[0].Status != "installed" {
		t.Fatalf("install: %+v", items)
	}
	line := log.String()
	for _, want := range []string{"acme-test", "1.0.0", "publisher", "example.com", "navigate"} {
		if !strings.Contains(line, want) {
			t.Fatalf("review line lacks %q: %q", want, line)
		}
	}
}

func keysOf(m map[string]bundledAutomation) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBundleReviewDetailCarriesConfirmation: replaceRequired, trustChange
// and visibility come through from the dry-run review unchanged.
func TestBundleReviewDetailCarriesConfirmation(t *testing.T) {
	r := &automation.InstallResult{ID: "acme", Version: "2.0.0", Review: automation.Review{
		Domains:         []string{"acme.test"},
		ReplaceRequired: true,
		TrustChange:     &automation.TrustChange{From: "local", To: "imported"},
		Visibility:      map[string][]string{"visits_profiles": {"open_profile"}},
		Replaces:        &automation.Replaced{ID: "acme", Source: "local", Trust: "local", Version: "1.0.0"},
	}}
	d := newBundleReviewDetail(r)
	if !d.ReplaceRequired || d.TrustChange == nil || d.TrustChange.To != "imported" ||
		len(d.Visibility["visits_profiles"]) != 1 || d.Replaces == nil {
		t.Fatalf("detail = %+v", d)
	}
	if d := newBundleReviewDetail(&automation.InstallResult{ID: "x"}); d.Visibility == nil {
		t.Fatal("visibility must be an object, not null")
	}
}
