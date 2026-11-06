package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgproposal"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The tools of a chat session label an org proposal with the conversation they run in and judge a grant on the
// workflow store the CLI reads (the hybrid one: where a workflow file differs from its rows, the file speaks) (#1477).
func TestChatSessionToolsLabelProposalsAndReadWorkflowFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflows (id, name, profile_id) VALUES ('wf-1', 'Digest', 'default')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflow_nodes (id, workflow_id, node_type, name, config) VALUES ('wf-1-n', 'wf-1', 'core.set', 'step', '{}')`); err != nil {
		t.Fatal(err)
	}
	// The file of the same workflow has an outbound node the rows do not.
	fs, err := workflow.NewWorkflowFileStore(filepath.Join(home, ".monoagent", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.SaveWorkflow(context.Background(), &workflow.Workflow{ID: "wf-1", Name: "Digest", ProfileID: "default", Nodes: []workflow.WorkflowNode{
		{ID: "wf-1-t", WorkflowID: "wf-1", Type: "trigger.manual", Name: "start"},
		{ID: "wf-1-n", WorkflowID: "wf-1", Type: "comm.slack", Name: "step"},
	}}); err != nil {
		t.Fatal(err)
	}
	doc := orgdesign.NewOrg("growth", "grow", orgdesign.NewOrgOptions{})
	doc.Automations = append(doc.Automations, orgdesign.AutomationRef{WorkflowID: "wf-1", Alias: "summarize"})
	if _, err := orgdesign.Save(profiledir.Root(db.DB, "default"), doc); err != nil {
		t.Fatal(err)
	}

	mt := newChatMonoagentTools(db, "", home, "default", "conv-42", false, false)
	out, err := mt.Execute("set_org_grant", `{"org_name":"growth","role_id":"lead","automation":"summarize"}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		ID      string `json:"proposal_id"`
		Preview string `json:"preview_text"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	p, err := orgproposal.NewStore(db.DB).Get(context.Background(), "default", res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.ConversationID != "conv-42" {
		t.Errorf("label %q, want the conversation id", p.ConversationID)
	}
	if !strings.Contains(res.Preview, "Risk tier of the workflow: irreversible") {
		t.Errorf("the grant was not judged on the workflow file: %s", res.Preview)
	}
}
