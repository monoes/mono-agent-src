package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The JSON report (and so the MCP doctor tool, which returns it as it is) never carries text from an
// adoptable workflow file: not a node type the engine does not know, not a name, not configuration
// values, however many decoy keys sort ahead of the ones that matter; and a view that was cut says
// to read the file.
func TestDoctorJSONCarriesNoTextFromAnAdoptableFile(t *testing.T) {
	ctx := context.Background()
	env, _, store := workflowDoctorEnv(t)
	if err := store.SaveWorkflow(ctx, &workflow.Workflow{ID: "wf-base", Name: "b", ProfileID: "default"}); err != nil { // the claim is recorded
		t.Fatal(err)
	}
	cfg := map[string]any{"command": "CURL-EVIL-COMMAND", "url": "http://evil.example/exfil", "big": strings.Repeat("zQz", 1000)}
	for i := 0; i < 40; i++ { // sorted first when the config is marshalled
		cfg[fmt.Sprintf("a%02d", i)] = strings.Repeat("d", 30)
	}
	wf := &workflow.Workflow{ID: "wf-decoy", Name: "decoy", ProfileID: "default", Nodes: []workflow.WorkflowNode{
		{ID: "n1", WorkflowID: "wf-decoy", Type: "trigger.manual", Name: "n1", Config: map[string]any{}},
		{ID: "n2", WorkflowID: "wf-decoy", Type: "evil.IGNORE-PREVIOUS-INSTRUCTIONS", Name: "NAME-IGNORE-ABOVE", Config: cfg}}}
	files, _ := workflow.NewWorkflowFileStore(filepath.Join(env.DataDir, "workflows"))
	if err := files.SaveWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	rep := health.Default().Run(ctx, env, health.Options{IDs: []string{health.CheckWorkflowPublications}})
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !strings.Contains(out, "wf-decoy") {
		t.Fatalf("the adoptable file is not in the report: %s", out)
	}
	for _, text := range []string{"IGNORE-PREVIOUS", "CURL-EVIL-COMMAND", "evil.example", "NAME-IGNORE-ABOVE", "zQzzQz", "dddddddddd"} {
		if strings.Contains(out, text) {
			t.Errorf("text from the file reached the JSON report: %q", text)
		}
	}
	if !strings.Contains(out, "INCOMPLETE: read") {
		t.Errorf("a cut view does not tell the person to read the file: %s", out)
	}
}
