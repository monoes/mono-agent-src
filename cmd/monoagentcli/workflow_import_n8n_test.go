package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/zalando/go-keyring"
)

func n8nFixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "internal", "n8nimport", "testdata", "n8n", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func n8nTestHome(t *testing.T) string {
	t.Helper()
	account.SetEnforceFromForTest(t, time.Time{})
	keyring.MockInit()
	home := t.TempDir()
	seedHomeDB(t, home)
	return home
}

func TestWorkflowConvertN8nPrintsWorkflowAndReport(t *testing.T) {
	home := n8nTestHome(t)
	out, errOut, err := runCLI(t, home, "workflow", "convert", "--from", "n8n", n8nFixture(t, "rss_to_slack.json"))
	if err != nil {
		t.Fatalf("convert: %v\n%s", err, errOut)
	}
	wf, err := workflow.ParseWorkflowFileBytes([]byte(out))
	if err != nil {
		t.Fatalf("stdout is not a workflow: %v\n%s", err, out)
	}
	if wf.Name != "RSS digest to Slack" || len(wf.Nodes) != 4 || wf.IsActive {
		t.Errorf("workflow = %s, %d nodes, active %v", wf.Name, len(wf.Nodes), wf.IsActive)
	}
	if !strings.Contains(errOut, "n8n import report") || !strings.Contains(errOut, "monoagentcli connect slack") {
		t.Errorf("report on stderr:\n%s", errOut)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "workflows")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(home, ".monoagent", "workflows"))
		if len(entries) > 0 {
			t.Errorf("convert saved files: %v", entries)
		}
	}
}

func TestWorkflowConvertN8nJSONAndExitCodes(t *testing.T) {
	home := n8nTestHome(t)
	out, _, err := runCLI(t, home, "--json", "workflow", "convert", n8nFixture(t, "unmapped.json"))
	if code := exitCodeFor(err); code != 3 {
		t.Fatalf("unmapped nodes: exit %d (%v), want 3", code, err)
	}
	var res struct {
		Workflow workflow.WorkflowFile `json:"workflow"`
		Report   struct {
			Complete bool `json:"complete"`
			Counts   struct {
				Unmapped    int `json:"unmapped"`
				Expressions int `json:"expressions_untranslated"`
			} `json:"counts"`
			Expressions []struct{ Node, Expression string } `json:"expressions"`
		} `json:"report"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("stdout: %v\n%s", err, out)
	}
	if res.Report.Complete || res.Report.Counts.Unmapped != 1 || res.Report.Counts.Expressions != 1 || len(res.Workflow.Nodes) == 0 {
		t.Errorf("json result = %+v", res.Report)
	}
	if res.Report.Expressions[0].Node != "Cross node" {
		t.Errorf("expression issue = %+v", res.Report.Expressions)
	}
	_, _, err = runCLIIn(t, home, `{"nodes": []}`, "workflow", "convert", "-")
	if code := exitCodeFor(err); code != 3 {
		t.Errorf("not a workflow: exit %d (%v), want 3", code, err)
	}
	_, _, err = runCLI(t, home, "workflow", "convert", "--from", "zapier", n8nFixture(t, "comms.json"))
	if code := exitCodeFor(err); code != 3 {
		t.Errorf("unknown --from: exit %d, want 3", code)
	}
}

func TestWorkflowImportFromN8n(t *testing.T) {
	home := n8nTestHome(t)
	out, errOut, err := runCLI(t, home, "--json", "workflow", "import", "--from", "n8n", n8nFixture(t, "if_switch_merge.json"))
	if err != nil {
		t.Fatalf("import: %v\n%s\n%s", err, out, errOut)
	}
	var res struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		N8n    struct {
			Complete bool                          `json:"complete"`
			Counts   struct{ Full, MonoNodes int } `json:"counts"`
		} `json:"n8n"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("stdout: %v\n%s", err, out)
	}
	if res.ID == "" || res.Name != "Lead routing" || !res.N8n.Complete || res.N8n.Counts.Full != 9 {
		t.Fatalf("import result = %+v", res)
	}
	if _, _, err := runCLI(t, home, "workflow", "validate", res.ID); err != nil {
		t.Errorf("imported workflow does not validate: %v", err)
	}

	// stdin, text output, and a workflow with placeholders: saved, exit 3.
	raw, err := os.ReadFile(n8nFixture(t, "unmapped.json"))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLIIn(t, home, string(raw), "workflow", "import", "--from", "n8n", "-")
	if code := exitCodeFor(err); code != 3 {
		t.Fatalf("unmapped import: exit %d (%v), want 3\n%s", code, err, out)
	}
	if !strings.Contains(out, `Imported workflow "Partly unsupported"`) || !strings.Contains(out, "result: incomplete") {
		t.Errorf("text output:\n%s", out)
	}
}

func TestWorkflowImportPositionalMonoFile(t *testing.T) {
	home := n8nTestHome(t)
	p := filepath.Join(t.TempDir(), "wf.json")
	src := `{"name":"Plain","nodes":[{"id":"t","type":"trigger.manual","name":"Start","config":{}}],"connections":[]}`
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runCLI(t, home, "workflow", "import", p)
	if err != nil || !strings.Contains(out, `Imported workflow "Plain"`) {
		t.Fatalf("positional import: %v\n%s\n%s", err, out, errOut)
	}
}
