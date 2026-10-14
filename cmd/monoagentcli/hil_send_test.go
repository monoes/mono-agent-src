package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
)

// sendEnv is a migrated DB under a scratch HOME. The process starts as the
// operator; asAgentProcess makes it an agent's.
func sendEnv(t *testing.T) *globalConfig {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	asOperatorProcess(t)
	return &globalConfig{DBPath: newHILTestDB(t), ProfileID: "default", JSONOutput: true}
}

func asOperatorProcess(t *testing.T) {
	t.Helper()
	for _, k := range append(orgsign.AgentContextMarkers(), hilsend.EnvAgentContext, "MONOAGENT_ACTOR",
		hilsend.EnvApprovalID, hilsend.EnvApprovalProof, hilsend.EnvConversation, hilsend.EnvAgentRole, hilsend.EnvAgentID) {
		t.Setenv(k, "")
	}
}

func asAgentProcess(t *testing.T) {
	t.Helper()
	asOperatorProcess(t)
	t.Setenv(hilsend.EnvAgentContext, "coder")
	t.Setenv(hilsend.EnvConversation, "conv-7")
	t.Setenv(hilsend.EnvAgentRole, "outreach")
}

func runSendCLI(t *testing.T, cfg *globalConfig, cmd interface {
	SetArgs([]string)
	Execute() error
}, args ...string) (string, error) {
	t.Helper()
	cmd.SetArgs(args)
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	return out, err
}

// queueViaNodeRun is what an agent does: run a send node. Nothing is sent
// (the node would need an SMTP server): it is queued and exit is clean.
func queueViaNodeRun(t *testing.T, cfg *globalConfig, config string) string {
	t.Helper()
	asAgentProcess(t)
	out, err := runSendCLI(t, cfg, newNodeRunCmd(cfg), "comm.email_send", "--config", config, "--output", "json")
	if err != nil {
		t.Fatalf("agent node run failed: %v\n%s", err, out)
	}
	var res map[string][]map[string]interface{}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	item, _ := res["main"][0]["json"].(map[string]interface{})
	if item["pending"] != true || item["status"] != "queued for approval" || item["hil_id"] == "" {
		t.Fatalf("output = %v", item)
	}
	return item["hil_id"].(string)
}

const sendConfig = `{"smtp_host":"127.0.0.1","from":"me@example.test","to":"ada@example.test","subject":"Hi","body":"Original","password":"hunter2"}`

func swapRunner(t *testing.T) *[]hilsend.Run {
	t.Helper()
	var calls []hilsend.Run
	old := hilSendRunner
	hilSendRunner = func(*globalConfig) hilsend.Runner {
		return func(_ context.Context, r hilsend.Run) (json.RawMessage, error) {
			calls = append(calls, r)
			return json.RawMessage(`{"main":[{"success":true}]}`), nil
		}
	}
	t.Cleanup(func() { hilSendRunner = old })
	return &calls
}

func TestAgentNodeRunQueuesAndHILListShowsIt(t *testing.T) {
	cfg := sendEnv(t)
	id := queueViaNodeRun(t, cfg, sendConfig)
	asOperatorProcess(t)
	items, err := runHIL(t, cfg, "list")
	if err != nil || len(items) != 1 {
		t.Fatalf("hil list: %v %+v", err, items)
	}
	it := items[0]
	if it.ID != id || it.Kind != "send" || it.Send == nil || it.Send.State != hilsend.StatePending ||
		it.Send.Text != "Original" || it.Send.PreparedBy.ConversationID != "conv-7" || it.Send.PreparedBy.Role != "outreach" {
		t.Fatalf("item = %+v send=%+v", it, it.Send)
	}
	if strings.Contains(sendJSON(t, it), "hunter2") {
		t.Fatal("a secret reached hil list")
	}
}

func sendJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestResolveApproveRunsOnceRejectNever(t *testing.T) {
	cfg := sendEnv(t)
	calls := swapRunner(t)
	approveID := queueViaNodeRun(t, cfg, sendConfig)
	rejectID := queueViaNodeRun(t, cfg, sendConfig)
	asOperatorProcess(t)

	out, err := runSendCLI(t, cfg, newHILCmd(cfg), "resolve", approveID, "--approve", "--edit-text", "Edited by me")
	if err != nil {
		t.Fatal(err)
	}
	var p hilsend.Payload
	if err := json.Unmarshal([]byte(out), &p); err != nil || p.State != hilsend.StateSent || p.Text != "Edited by me" {
		t.Fatalf("approve: %v %+v", err, p)
	}
	if len(*calls) != 1 || (*calls)[0].Config["body"] != "Edited by me" || (*calls)[0].ApprovalID != approveID {
		t.Fatalf("runner calls = %+v", *calls)
	}
	if _, leaked := (*calls)[0].Config["password"]; leaked {
		t.Fatal("secret in the replayed config")
	}
	// Again (and via the older approve command): still one send.
	if _, err := runSendCLI(t, cfg, newHILCmd(cfg), "resolve", approveID, "--approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := runSendCLI(t, cfg, newHILCmd(cfg), "approve", approveID); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("sent %d times", len(*calls))
	}

	out, err = runSendCLI(t, cfg, newHILCmd(cfg), "resolve", rejectID, "--reject")
	if err != nil || !strings.Contains(out, `"state": "rejected"`) && !strings.Contains(out, `"state":"rejected"`) {
		t.Fatalf("reject: %v %s", err, out)
	}
	if _, err := runSendCLI(t, cfg, newHILCmd(cfg), "approve", rejectID); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("a rejected send ran: %+v", *calls)
	}
	if _, err := runSendCLI(t, cfg, newHILCmd(cfg), "resolve", approveID); err == nil {
		t.Fatal("resolve with neither --approve nor --reject succeeded")
	}
	if _, err := runSendCLI(t, cfg, newHILCmd(cfg), "resolve", "nope", "--approve"); err == nil || exitCodeFor(err) != 2 {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestAgentCannotDecideEditOrAllow(t *testing.T) {
	cfg := sendEnv(t)
	calls := swapRunner(t)
	id := queueViaNodeRun(t, cfg, sendConfig) // leaves the process an agent
	for _, args := range [][]string{
		{"resolve", id, "--approve"},
		{"resolve", id, "--reject"},
		{"approve", id},
		{"reject", id},
		{"edit", id, "--text", "sneaky"},
		{"allow", "add", "--action", "email", "--platform", "email", "--recipient", "ada@example.test"},
		{"allow", "remove", "x"},
	} {
		cmd := newHILCmd(cfg)
		if _, err := runSendCLI(t, cfg, cmd, args...); err == nil {
			t.Errorf("agent ran `hil %s`", strings.Join(args, " "))
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("agent got a send performed: %+v", *calls)
	}
	// The agent can still read what it queued.
	if _, err := runSendCLI(t, cfg, newHILCmd(cfg), "show", id); err != nil {
		t.Fatalf("show: %v", err)
	}
	asOperatorProcess(t)
	items, _ := runHIL(t, cfg, "list")
	if len(items) != 1 || items[0].Send.State != hilsend.StatePending || items[0].Send.Text != "Original" {
		t.Fatalf("item changed: %+v", items)
	}
}

func TestAlwaysAllowThroughResolveSkipsTheQueueNextTime(t *testing.T) {
	cfg := sendEnv(t)
	swapRunner(t)
	id := queueViaNodeRun(t, cfg, sendConfig)
	asOperatorProcess(t)
	if _, err := runSendCLI(t, cfg, newHILCmd(cfg), "resolve", id, "--approve", "--always-allow"); err != nil {
		t.Fatal(err)
	}
	out, err := runSendCLI(t, cfg, newHILCmd(cfg), "allow", "list")
	if err != nil || !strings.Contains(out, "ada@example.test") {
		t.Fatalf("allow list: %v %s", err, out)
	}
	// Same destination from an agent: not queued (it would go on to run the
	// node, so use a node that fails fast without network: an unreachable
	// host on a closed port).
	asAgentProcess(t)
	out, err = runSendCLI(t, cfg, newNodeRunCmd(cfg), "comm.email_send", "--output", "json", "--config",
		`{"smtp_host":"127.0.0.1","smtp_port":1,"from":"me@example.test","to":"ada@example.test","subject":"Hi","body":"x"}`)
	if err == nil || strings.Contains(out, "queued for approval") {
		t.Fatalf("an always-allowed destination was queued: %v %s", err, out)
	}
	// A different recipient still waits.
	queueViaNodeRun(t, cfg, strings.Replace(sendConfig, "ada@", "bob@", 1))
}

func TestAttachmentCommandConfinesPaths(t *testing.T) {
	cfg := sendEnv(t)
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	root := profiledir.Root(db.DB, "default")
	db.Close()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "cover.png")
	if err := os.WriteFile(inside, []byte("PNGDATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := `{"smtp_host":"127.0.0.1","from":"me@example.test","to":"ada@example.test","subject":"Hi","body":"x","attachments":["` +
		inside + `","` + outside + `","` + root + `/../../../etc/passwd"]}`
	id := queueViaNodeRun(t, cfg, config)
	asOperatorProcess(t)

	out, err := runSendCLI(t, cfg, newHILCmd(cfg), "attachment", id, "0")
	if err != nil {
		t.Fatal(err)
	}
	var f hilsend.File
	if err := json.Unmarshal([]byte(out), &f); err != nil || f.Size != 7 || f.Kind != "image" || f.Mime != "image/png" {
		t.Fatalf("file = %+v %v", f, err)
	}
	if out, err = runSendCLI(t, cfg, newHILCmd(cfg), "attachment", id, "0", "--stream"); err != nil || out != "PNGDATA" {
		t.Fatalf("stream = %q %v", out, err)
	}
	for _, n := range []string{"1", "2", "9", "x"} {
		if out, err := runSendCLI(t, cfg, newHILCmd(cfg), "attachment", id, n); err == nil {
			t.Errorf("attachment %s served: %s", n, out)
		}
	}
}

func TestPublicationRegisterFromAgentNeedsApproval(t *testing.T) {
	cfg := sendEnv(t)
	swapRunner(t)
	id := queueViaNodeRun(t, cfg, sendConfig)
	entry := `{"platform":"x","kind":"post","body":"hello","remote_id":"1","published_at":"2026-10-13T10:00:00Z"}`
	register := func(args ...string) error {
		cmd := newPublicationCmd(cfg)
		cmd.SetIn(strings.NewReader(entry))
		_, err := runSendCLI(t, cfg, cmd, append([]string{"register", "--stdin-json"}, args...)...)
		return err
	}
	if err := register(); err == nil {
		t.Fatal("an agent registered a publication with no approval")
	}
	if err := register("--approval", id); err == nil {
		t.Fatal("a still-pending send counted as approved")
	}
	asOperatorProcess(t)
	if _, err := runSendCLI(t, cfg, newHILCmd(cfg), "resolve", id, "--approve"); err != nil {
		t.Fatal(err)
	}
	asAgentProcess(t)
	if err := register("--approval", id); err != nil {
		t.Fatalf("register with a sent approval: %v", err)
	}
	asOperatorProcess(t)
	if err := register(); err != nil {
		t.Fatalf("operator register: %v", err)
	}
}

func TestCoderSendApprovalSetting(t *testing.T) {
	cfg := sendEnv(t)
	out, err := runSendCLI(t, cfg, newCoderSetCmd(cfg))
	if err != nil || !strings.Contains(out, `"sendApproval":true`) {
		t.Fatalf("default: %v %s", err, out)
	}
	asAgentProcess(t)
	if _, err := runSendCLI(t, cfg, newCoderSetCmd(cfg), "--send-approval=false"); err == nil {
		t.Fatal("an agent turned send approval off")
	}
	asOperatorProcess(t)
	out, err = runSendCLI(t, cfg, newCoderSetCmd(cfg), "--send-approval=false")
	if err != nil || !strings.Contains(out, `"sendApproval":false`) {
		t.Fatalf("operator off: %v %s", err, out)
	}
	// Off means an agent's send is no longer queued.
	asAgentProcess(t)
	out, err = runSendCLI(t, cfg, newNodeRunCmd(cfg), "comm.email_send", "--output", "json", "--config",
		`{"smtp_host":"127.0.0.1","smtp_port":1,"from":"me@example.test","to":"ada@example.test","subject":"Hi","body":"x"}`)
	if err == nil || strings.Contains(out, "queued for approval") {
		t.Fatalf("send was queued with the setting off: %v %s", err, out)
	}
}
