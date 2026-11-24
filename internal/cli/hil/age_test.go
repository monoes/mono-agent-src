package hil

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// queueSend makes one pending send card and returns its id.
func queueSend(t *testing.T, dbPath string) string {
	t.Helper()
	for _, k := range append(operator.MarkerEnv(), hilsend.EnvAgentRole, hilsend.EnvAgentID) {
		t.Setenv(k, "")
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	ctx = vault.ContextWithExecIDs(workflow.WithAIAuthored(ctx), "wf1", "ex1")
	out, handled, err := hilsend.Guard(ctx, "comm.email_send", workflow.NodeInput{}, map[string]interface{}{
		"smtp_host": "smtp.example.test", "from": "me@example.test", "to": "ada@example.test", "subject": "Hello", "body": "Hi Ada"})
	if err != nil || !handled {
		t.Fatalf("queue: handled=%v err=%v", handled, err)
	}
	return out[0].Items[0].JSON["hil_id"].(string)
}

func ageCard(t *testing.T, dbPath, id string, days int) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	if _, err := db.DB.Exec(`UPDATE hil_pending SET readonly_data = json_set(readonly_data, '$.createdAt', ?) WHERE id = ?`, old, id); err != nil {
		t.Fatal(err)
	}
}

func runAge(t *testing.T, d Deps, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := d.newHILCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

// A card that waited says so; one that waited a week can be expired by hand; nothing expires on its own (R10-12, R28-4).
func TestHILSendsNudgesAndExpire(t *testing.T) {
	dbPath := newHILTestDB(t)
	id := queueSend(t, dbPath)
	ageCard(t, dbPath, id, 9)
	d := testDeps(&testConfig{DBPath: dbPath})

	out, err := runAge(t, d, "sends")
	if err != nil || !strings.Contains(out, "(waiting 9 days)") {
		t.Fatalf("sends: %v\n%s", err, out)
	}
	if _, err := runAge(t, d, "expire", id); exitCodeFor(err) != 5 {
		t.Fatalf("an agent expiring a card: %v", err)
	}
	d.RequireOperator = func(string) error { return nil }
	if _, err := runAge(t, d, "expire", "--older-than-days", "30"); err != nil {
		t.Fatal(err)
	}
	if out, _ := runAge(t, d, "sends", "--state", "pending"); !strings.Contains(out, id) {
		t.Fatalf("a card younger than the cut-off was expired:\n%s", out)
	}
	out, err = runAge(t, d, "expire", "--older-than-days", "7")
	if err != nil || !strings.Contains(out, "Expired 1") {
		t.Fatalf("expire: %v\n%s", err, out)
	}
	if out, _ := runAge(t, d, "sends", "--state", "rejected"); !strings.Contains(out, id) {
		t.Fatalf("the expired card is not rejected:\n%s", out)
	}
}

// The gate-off setting shows as a loud line in `hil sends` (R10-16).
func TestHILSendsWarnsWhenApprovalIsOff(t *testing.T) {
	dbPath := newHILTestDB(t)
	d := testDeps(&testConfig{DBPath: dbPath})
	if out, _ := runAge(t, d, "sends"); strings.Contains(out, "WARNING") {
		t.Fatalf("a warning with the gate on:\n%s", out)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO configs (name, config_data) VALUES ('coder_mode', '{"sendApproval": false}')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if out, _ := runAge(t, d, "sends"); !strings.Contains(out, SendApprovalOffWarning) {
		t.Fatalf("no warning with the gate off:\n%s", out)
	}
}
