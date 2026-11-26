package hil

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// twoProfileQueue is a database with a pending item in "default" (a send), one
// in "p2" (Work; a workflow review) and a decided one in "p2".
func twoProfileQueue(t *testing.T) (dbPath, send string) {
	t.Helper()
	dbPath = newHILTestDB(t)
	send = queueSend(t, dbPath) // also clears the agent markers
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO profiles (id, name) VALUES ('p2', 'Work')`,
		`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id)
		 VALUES ('work-1', 'ex9', 'wf9', 'review', 'Review post', 'pending', '{}', '{"caption":"hi"}', '{}', 'p2'),
		        ('work-done', 'ex9', 'wf9', 'review', 'Old review', 'approved', '{}', '{}', '{}', 'p2')`,
	} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return dbPath, send
}

func pendingJSON(t *testing.T, d Deps, args ...string) []map[string]any {
	t.Helper()
	d.JSONOutput = func() bool { return true }
	out, err := runAge(t, d, append([]string{"pending"}, args...)...)
	if err != nil {
		t.Fatalf("pending %v: %v\n%s", args, err, out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("pending %v: %v\n%s", args, err, out)
	}
	return rows
}

func operatorDeps(cfg *testConfig) Deps {
	d := testDeps(cfg)
	d.RequireOperator = func(string) error { return nil }
	return d
}

// `hil pending --all` is the user's view of every profile's queue: each item
// names its profile, decided ones are left out, and nothing changes.
func TestHILPendingAllListsEveryProfile(t *testing.T) {
	dbPath, send := twoProfileQueue(t)
	rows := pendingJSON(t, operatorDeps(&testConfig{DBPath: dbPath}), "--all")
	got := map[string]string{}
	for _, r := range rows {
		got[r["id"].(string)] = r["profile_id"].(string) + "/" + r["profile_name"].(string)
	}
	if len(rows) != 2 || !strings.HasPrefix(got[send], "default/") || got["work-1"] != "p2/Work" {
		t.Fatalf("rows = %v", got)
	}
	for _, r := range rows {
		if r["id"] == send && (r["kind"] != "send" || r["send"].(map[string]any)["text"] != "Hi Ada") {
			t.Errorf("the send card = %+v", r)
		}
	}
	out, err := runAge(t, operatorDeps(&testConfig{DBPath: dbPath}), "pending", "--all")
	if err != nil || !strings.Contains(out, "Work") || !strings.Contains(out, "work-1") || strings.Contains(out, "work-done") {
		t.Fatalf("table: %v\n%s", err, out)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var pending int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM hil_pending WHERE status = 'pending'`).Scan(&pending); err != nil || pending != 2 {
		t.Fatalf("listing changed the queue: %d pending (%v)", pending, err)
	}
}

// Without --all the command lists the one profile it was given, and an agent
// may read that as it may `hil list`.
func TestHILPendingListsOnlyTheNamedProfile(t *testing.T) {
	dbPath, send := twoProfileQueue(t)
	for profile, want := range map[string]string{"default": send, "p2": "work-1"} {
		rows := pendingJSON(t, testDeps(&testConfig{DBPath: dbPath, ProfileID: profile}))
		if len(rows) != 1 || rows[0]["id"] != want || rows[0]["profile_id"] != profile {
			t.Errorf("--profile %s: %+v", profile, rows)
		}
	}
}

// Crossing profiles is the user's alone: an agent is refused and shown nothing,
// whichever check finds it, and an unwired check refuses too.
func TestHILPendingAllRefusesAnAgent(t *testing.T) {
	dbPath, send := twoProfileQueue(t)
	refused := func(what string, d Deps) {
		t.Helper()
		var out string
		var err error
		stdout := captureStdout(t, func() { out, err = runAge(t, d, "pending", "--all") })
		if err == nil {
			t.Fatalf("%s: allowed\n%s%s", what, out, stdout)
		}
		for _, secret := range []string{send, "work-1", "Hi Ada"} {
			if strings.Contains(out+stdout, secret) {
				t.Errorf("%s: the refusal shows %q", what, secret)
			}
		}
	}
	refused("the operator check", testDeps(&testConfig{DBPath: dbPath}))
	unwired := testDeps(&testConfig{DBPath: dbPath})
	unwired.RequireOperator = nil
	refused("no operator check", unwired)
	markers := append(orgsign.AgentContextMarkers(), hilsend.EnvAgentContext, "MONOAGENT_ACTOR", workflow.EnvWorkflowRun, browserctl.EnvAICaller)
	for _, marker := range markers {
		t.Setenv(marker, "agent")
		refused(marker, operatorDeps(&testConfig{DBPath: dbPath}))
		t.Setenv(marker, "")
	}
}

// --all and a named profile contradict each other.
func TestHILPendingAllTakesNoProfile(t *testing.T) {
	dbPath, _ := twoProfileQueue(t)
	_, err := runAge(t, operatorDeps(&testConfig{DBPath: dbPath, ProfileID: "p2"}), "pending", "--all")
	if exitCodeFor(err) != 3 {
		t.Fatalf("--all with a profile: %v", err)
	}
}
