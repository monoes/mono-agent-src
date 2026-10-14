package workflowbundle

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestHandleBundledAutomationsNothingBundled(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"name":"x"}`, `not json`, `{"automations":{}}`} {
		if items := testDeps().HandleBundledAutomations([]byte(raw), ImportOptions{}); items != nil {
			t.Errorf("%s: items = %+v, want nil", raw, items)
		}
	}
}

// Without a registry every bundled package fails; the import itself never does.
func TestHandleBundledAutomationsRegistryError(t *testing.T) {
	t.Parallel()
	items := testDeps().HandleBundledAutomations([]byte(`{"automations":{"b":{"version":"1"},"a":{"version":"2"}}}`), ImportOptions{})
	if len(items) != 2 || items[0].ID != "a" || items[0].Status != "failed" || items[0].Error != errNoRegistry.Error() || items[1].ID != "b" {
		t.Fatalf("items = %+v", items)
	}
}

// A bundled package whose bytes do not match its sha256 is never installed, and a package the
// exporter could not bundle is missing with the exporter's reason and hint.
func TestHandleBundledAutomationsUntrustedInput(t *testing.T) {
	t.Parallel()
	d := registryDeps(t)
	mpkg := base64.StdEncoding.EncodeToString([]byte("not a zip"))
	raw := `{"automations":{"acme":{"version":"1.0.0","sha256":"00","mpkg":"` + mpkg + `"},"nosum":{"version":"1","mpkg":"` + mpkg + `"}},
		"unbundledAutomations":{"legacy":{"version":"0.1","reason":"no domains","hint":"re-run with --automation-domains"}}}`
	var log bytes.Buffer
	items := d.HandleBundledAutomations([]byte(raw), ImportOptions{Yes: true, Out: &log})
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	if it := items[0]; it.ID != "acme" || it.Status != "failed" || !strings.Contains(it.Error, "sha256 mismatch") {
		t.Errorf("acme = %+v", it)
	}
	if it := items[1]; it.ID != "nosum" || it.Status != "failed" || !strings.Contains(it.Error, "has no sha256") {
		t.Errorf("nosum = %+v", it)
	}
	if it := items[2]; it.ID != "legacy" || it.Status != "missing" || !it.NotBundled ||
		it.Error != "not in the bundle: no domains; the sender can re-run with --automation-domains" ||
		it.Hint != "Ask the sender to re-run with --automation-domains." {
		t.Errorf("legacy = %+v", it)
	}
	if log.Len() != 0 {
		t.Errorf("printed a review for a package that failed its checks: %q", log.String())
	}

	var out bytes.Buffer
	PrintBundleImport(&out, append(items, ImportItem{ID: "m", Version: "2", Status: "missing"}))
	want := "Automation acme 1.0.0: not installed: bundled package acme: sha256 mismatch\n" +
		"Automation nosum 1: not installed: bundled package nosum has no sha256\n" +
		"Automation legacy 0.1: missing (not in the bundle: no domains; the sender can re-run with --automation-domains)\n" +
		"  To run this workflow here, recreate legacy on this machine or ask the sender for it.\n" +
		"Automation m 2: missing (bundled)\n" +
		"Pass --yes to `workflow import` to install bundled automations.\n"
	if out.String() != want {
		t.Errorf("PrintBundleImport:\n%s\nwant:\n%s", out.String(), want)
	}
}

// A workflow that uses no automation bundles nothing; --automation-domains for an id it does not
// use is the CLI's invalid input.
func TestBundleWorkflowAutomations(t *testing.T) {
	t.Parallel()
	d := registryDeps(t)
	file := workflow.WorkflowFile{Name: "w", Nodes: []workflow.WorkflowFileNode{{ID: "t", Type: "trigger.manual"}, {ID: "s", Type: "core.set"}}}
	b, err := d.BundleWorkflowAutomations(file, Options{})
	if err != nil || len(b.Automations) != 0 || len(b.Unbundled) != 0 || len(b.Nodes) != 2 {
		t.Fatalf("bundle = %+v, %v", b, err)
	}
	_, err = d.BundleWorkflowAutomations(file, Options{Domains: map[string][]string{"typo": {"x.com"}}})
	var ce *cliError
	if !errors.As(err, &ce) || ce.code != 3 || ce.msg != "--automation-domains typo: no such automation in this export (bundled: none)" {
		t.Fatalf("unknown --automation-domains id: %v", err)
	}
	if _, err := testDeps().BundleWorkflowAutomations(file, Options{}); !errors.Is(err, errNoRegistry) {
		t.Fatalf("no registry: %v", err)
	}
}

func TestNotices(t *testing.T) {
	t.Parallel()
	b := BundleFile{
		Unbundled:     map[string]UnbundledAutomation{"b": {Reason: "r2"}, "a": {Reason: "r1", Hint: "h1"}},
		SuggestedUsed: map[string][]string{"z": {"z.com", "*.z.com"}},
	}
	if got := strings.Join(UnbundledWarnings(b), "|"); got != "warning: automation a not bundled: r1\n  h1|warning: automation b not bundled: r2" {
		t.Errorf("UnbundledWarnings = %q", got)
	}
	if got := strings.Join(SuggestedNotices(b), "|"); got != "note: bundled z with suggested domains z.com, *.z.com (the recipient's install review shows them)" {
		t.Errorf("SuggestedNotices = %q", got)
	}
}
