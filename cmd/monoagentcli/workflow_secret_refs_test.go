package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// What a run stored of workflowID: its error and every step's error and
// output, as one string.
func storedRunText(t *testing.T, dbPath, workflowID string) string {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.DB.Query(`SELECT e.error_message, COALESCE(n.error_message, ''), COALESCE(n.output_items, '')
		FROM workflow_executions e LEFT JOIN workflow_execution_nodes n ON n.execution_id = e.id WHERE e.workflow_id = ?`, workflowID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var a, c, d string
		if err := rows.Scan(&a, &c, &d); err != nil {
			t.Fatal(err)
		}
		b.WriteString(a + "\n" + c + "\n" + d + "\n")
	}
	return b.String()
}

// A workflow the chat wrote resolves no @secret reference, whoever starts
// it: the step is refused, naming the reference, and the value is nowhere,
// not in the error and not in the stored run. Once the user approves the
// workflow, the same run resolves it (#845 review, item 1).
func TestAnUnapprovedWorkflowResolvesNoSecret(t *testing.T) {
	keyring.MockInit()
	_, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Add(context.Background(), db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	db.Close()

	t.Setenv(browserctl.EnvAICaller, "chat") // the chat writes and activates it
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "leaks")), &created); err != nil {
		t.Fatal(err)
	}
	add := func(typ, config string) string {
		var n workflow.WorkflowNode
		if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "node", "add", created.ID, "--type", typ, "--name", typ, "--config", config)), &n); err != nil {
			t.Fatal(err)
		}
		return n.ID
	}
	trig := add("trigger.manual", `{}`)
	stop := add("core.stop_error", `{"message":"@secret:vs"}`)
	runWorkflowSubcmd(t, cfg, "connect", created.ID, "--from", trig, "--to", stop)
	activateUnapprovedForTest(t, cfg, created.ID)
	t.Setenv(browserctl.EnvAICaller, "")

	cfg.JSONOutput = false
	_, out, err := runLogsFor(t, cfg, created.ID)
	if err != nil {
		out += err.Error()
	}
	stored := storedRunText(t, cfg.DBPath, created.ID)
	if strings.Contains(out+stored, "vsecret-777") {
		t.Fatalf("an unapproved workflow resolved the secret:\n%s\n%s", out, stored)
	}
	if !strings.Contains(stored, "@secret:vs is not read from the vault") {
		t.Errorf("the stored run does not say the reference was refused:\n%s", stored)
	}
	if strings.Contains(stored, secrets.ErrResolveRefused.Error()) {
		t.Errorf("the refusal repeats what its reason says:\n%s", stored)
	}

	cfg.JSONOutput = true
	runWorkflowSubcmd(t, cfg, "activate", created.ID) // the user approves it
	cfg.JSONOutput = false
	_, out, err = runLogsFor(t, cfg, created.ID)
	if err != nil {
		out += err.Error()
	}
	if !strings.Contains(out+storedRunText(t, cfg.DBPath, created.ID), "vsecret-777") {
		t.Errorf("the approved workflow did not resolve its secret:\n%s", out)
	}
}
