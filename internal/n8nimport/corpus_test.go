package n8nimport_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dop251/goja"
	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/workflow"
)

// corpusExpect is what each fixture must convert to.
var corpusExpect = map[string]struct {
	unmapped int
	types    []string // mono node types that must appear
}{
	"if_switch_merge.json":     {0, []string{"core.if", "core.switch", "core.merge", "core.code", "core.set"}},
	"rss_to_slack.json":        {0, []string{"trigger.schedule", "system.rss_read", "core.limit", "comm.slack"}},
	"webhook_to_sheets.json":   {0, []string{"trigger.webhook", "service.google_sheets", "core.set"}},
	"ai_agent.json":            {0, []string{"trigger.manual", "agent.ask"}},
	"loop_batches.json":        {0, []string{"core.split_in_batches", "http.request", "core.wait"}},
	"error_output.json":        {0, []string{"http.request", "core.stop_error"}},
	"legacy_v1.json":           {0, []string{"trigger.schedule", "core.code", "core.if", "http.request"}},
	"transform.json":           {0, []string{"core.code"}},
	"email.json":               {0, []string{"comm.email_read", "comm.email_send", "service.gmail", "service.outlook_mail", "core.filter"}},
	"databases.json":           {0, []string{"db.postgres", "db.mysql", "db.mongodb", "db.redis"}},
	"crm.json":                 {0, []string{"service.hubspot", "service.salesforce", "service.notion", "service.airtable", "service.linear", "service.jira", "service.asana", "service.github"}},
	"comms.json":               {0, []string{"comm.telegram", "service.discord", "comm.twilio", "comm.whatsapp", "service.reddit"}},
	"data_nodes.json":          {0, []string{"data.html", "data.markdown", "data.crypto", "data.xml", "system.execute_command"}},
	"unmapped.json":            {4, nil},
	"compare_datasets.json":    {0, []string{"core.switch"}},
	"schedules_and_shops.json": {0, []string{"service.shopify", "service.stripe", "service.youtube", "service.google_drive"}},
	"llm_chains.json":          {0, []string{"agent.ask", "core.if", "core.switch"}},
	"merge_modes.json":         {0, []string{"core.merge"}},
	"todoist_app.json":         {0, []string{"app.todoist"}},
	"split_out_summarize.json": {0, []string{"data.split_out", "data.summarize"}},
	"vector_rag.json":          {0, []string{"vector.load_document", "vector.store"}},
	"http_graphql.json":        {0, []string{"http.request", "http.graphql"}},
	"app_triggers.json":        {0, []string{"trigger.webhook", "trigger.schedule", "core.code", "service.google_sheets", "service.gmail", "app.toggl", "core.remove_duplicates"}},
}

func TestCorpusConvertsAndValidates(t *testing.T) {
	files, err := filepath.Glob("testdata/n8n/*.json")
	if err != nil || len(files) < 15 {
		t.Fatalf("want at least 15 fixtures, got %d (%v)", len(files), err)
	}
	reg := noderegistry.Build(nil)
	engine := workflow.NewExpressionEngine()
	for _, f := range files {
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			exp, ok := corpusExpect[name]
			if !ok {
				t.Fatalf("no expectation for %s", name)
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			file, rep, err := n8nimport.Convert(raw)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			if rep.Counts.Unmapped != exp.unmapped || rep.Complete != (exp.unmapped == 0) {
				t.Errorf("unmapped = %d (complete %v), want %d\n%s", rep.Counts.Unmapped, rep.Complete, exp.unmapped, rep.Text())
			}
			b, err := json.Marshal(file)
			if err != nil {
				t.Fatal(err)
			}
			wf, err := workflow.ParseWorkflowFileBytes(b)
			if err != nil {
				t.Fatalf("parse converted file: %v", err)
			}
			if err := workflow.ValidateConnectionsAgainstNodes(wf.Nodes, wf.Connections); err != nil {
				t.Fatalf("connections: %v", err)
			}
			if err := workflow.ValidateForActivation(&wf); err != nil {
				t.Fatalf("validate: %v\n%s", err, b)
			}
			have := map[string]bool{}
			untranslated := map[string]bool{}
			for _, e := range rep.Expressions {
				untranslated[e.Expression] = true
			}
			for _, n := range wf.Nodes {
				have[n.Type] = true
				if !strings.HasPrefix(n.Type, "trigger.") && !reg.Has(n.Type) {
					t.Errorf("node %q has unregistered type %q", n.Name, n.Type)
				}
				checkTemplates(t, engine, n, n.Config, untranslated)
				if n.Type == "core.code" {
					checkJS(t, engine, n)
				}
			}
			for _, typ := range exp.types {
				if !have[typ] {
					t.Errorf("no %s node in the result", typ)
				}
			}
			if _, err := rep.JSON(); err != nil {
				t.Errorf("report JSON: %v", err)
			}
		})
	}
}

// checkTemplates parses every config string as a mono template; only
// expressions the report lists as untranslated may fail to parse.
func checkTemplates(t *testing.T, e *workflow.ExpressionEngine, n workflow.WorkflowNode, v interface{}, untranslated map[string]bool) {
	t.Helper()
	switch x := v.(type) {
	case string:
		if !strings.Contains(x, "{{") || untranslated[x] {
			return
		}
		if _, err := e.EvaluateString(x, workflow.ExpressionContext{}); err != nil && strings.Contains(err.Error(), "parse error") {
			t.Errorf("node %q: template does not parse: %q: %v", n.Name, x, err)
		}
	case map[string]interface{}:
		for _, val := range x {
			checkTemplates(t, e, n, val, untranslated)
		}
	case []interface{}:
		for _, val := range x {
			checkTemplates(t, e, n, val, untranslated)
		}
	}
}

// checkJS renders a core.code program the way mono will (templates
// resolved) and compiles it.
func checkJS(t *testing.T, e *workflow.ExpressionEngine, n workflow.WorkflowNode) {
	t.Helper()
	code, _ := n.Config["code"].(string)
	src, err := e.EvaluateString(code, workflow.ExpressionContext{})
	if err != nil {
		t.Errorf("node %q: code template: %v", n.Name, err)
		return
	}
	if _, err := goja.Compile(n.Name, src, false); err != nil {
		t.Errorf("node %q: generated JavaScript does not compile: %v\n%s", n.Name, err, src)
	}
}
