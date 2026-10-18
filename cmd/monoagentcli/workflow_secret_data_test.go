package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/cli/workflowrun"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The user's approved workflow interpolates its input into a step. An agent
// that runs it with "@secret:vs" as that input gets no secret: the step is
// refused as one whose reference came from data, and the value is not in the
// output or the stored run. The user's own reference in an approved workflow
// still resolves when the agent runs it (#845 review: secret references as
// data).
func TestASecretReferenceInARunsInputResolvesNowhere(t *testing.T) {
	keyring.MockInit()
	_, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	workflowrun.FullOutputs = false // a global --full-outputs binds; no flag is parsed here
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

	// The user builds and activates both.
	build := func(name, message string) string {
		var created workflow.Workflow
		if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", name)), &created); err != nil {
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
		stop := add("core.stop_error", `{"message":`+message+`}`)
		runWorkflowSubcmd(t, cfg, "connect", created.ID, "--from", trig, "--to", stop)
		runWorkflowSubcmd(t, cfg, "activate", created.ID)
		return created.ID
	}
	echo := build("echo", `"{{ $json.msg }}"`)
	own := build("own", `"@secret:vs"`)

	t.Setenv("CLAUDECODE", "1") // an agent runs them
	run := func(id string, args ...string) string {
		cfg.JSONOutput = false
		cmd := newWorkflowCmd(cfg)
		cmd.SetArgs(append([]string{"run", id, "--timeout", "30s"}, args...))
		var runErr error
		out := captureStdout(t, func() { runErr = cmd.Execute() })
		if runErr != nil {
			out += runErr.Error()
		}
		return out + storedRunText(t, cfg.DBPath, id)
	}
	got := run(echo, "--input", `{"msg":"@secret:vs"}`)
	if strings.Contains(got, "vsecret-777") {
		t.Fatalf("the run's input resolved the secret:\n%s", got)
	}
	if !strings.Contains(got, secrets.ErrDataReference.Error()) {
		t.Errorf("the run does not say the reference came from data:\n%s", got)
	}
	if got := run(own); !strings.Contains(got, "vsecret-777") {
		t.Errorf("the approved workflow's own reference did not resolve:\n%s", got)
	}
}
