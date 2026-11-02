package task

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// saveRootSettings stores the operator's settings for a mission root's chat.
func saveRootSettings(t *testing.T, dbPath, profile, conv, patch string) {
	t.Helper()
	d, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	st := coderhost.NewStore(d.DB)
	set, _, err := coderhost.DefaultSettings().Apply([]byte(patch))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(profile, conv, set); err != nil {
		t.Fatal(err)
	}
}

func openItems(t *testing.T, dbPath string) []coderhost.PersonItem {
	t.Helper()
	d, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	items, err := coderhost.NewStore(d.DB).OpenPersonItems("default")
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// The lead's identity is its verified coder-session token (the resolver `approver`), never a label:
// the lead of the parent approves a session below it only when the operator allows it, giant work
// raises a person item, and the operator's later plain approval is the operator's.
func TestLeadApprovesWithItsRealTokenAndGiantWorkWaitsForThePerson(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	root := sessionNew(t, db, "root", "--conversation", "conv-root", "--budget-usd", "100", "--kind", "mission")
	small := sessionNew(t, db, "small", "--parent", id(root), "--budget-usd", "10", "--max-tasks", "10")
	big := sessionNew(t, db, "big", "--parent", id(root), "--budget-usd", "10", "--max-tasks", "30")
	sessionAdd(t, db, small, "a")
	sessionAdd(t, db, big, "a")
	lead := mintSessionToken(t, db, "default", "conv-root")
	other := mintSessionToken(t, db, "default", "conv-other")
	approve := func(session int64) map[string]any {
		return failedTaskJSON(t, db, "default", 3, "session", "approve", id(session), "--as", "lead-bot")
	}

	setenv(t, "MONOAGENT_SESSION_TOKEN", lead)
	if doc := approve(small); doc["code"] != "operator_only" || !strings.Contains(doc["error"].(string), "agentApproval is off") {
		t.Fatalf("approval is off by default: %v", doc)
	}
	saveRootSettings(t, db, "default", "conv-root", `{"agent_approval":"lead","approval_ceiling_usd":50}`)

	setenv(t, "MONOAGENT_SESSION_TOKEN", other)
	if doc := approve(small); doc["code"] != "operator_only" {
		t.Fatalf("the lead of another chat: %v", doc)
	}
	setenv(t, "MONOAGENT_SESSION_TOKEN", "")
	if doc := approve(small); doc["code"] != "operator_only" {
		t.Fatalf("a bare label with no token is not a lead: %v", doc)
	}
	setenv(t, "MONOAGENT_SESSION_TOKEN", lead)
	setenv(t, "MONOAGENT_CONVERSATION", "conv-root") // a label never sets the identity
	doc := approve(root)
	if doc["code"] != "operator_only" || !strings.Contains(doc["error"].(string), "root") {
		t.Fatalf("the root is the operator's: %v", doc)
	}

	doc = approve(big)
	if doc["code"] != "operator_only" || !strings.Contains(doc["error"].(string), "needs the person") {
		t.Fatalf("giant: %v", doc)
	}
	if items := openItems(t, db); len(items) != 1 || items[0].Kind != coderhost.ItemPlanApproval || items[0].RefID != big {
		t.Fatalf("items: %+v", items)
	}
	if st := sessionState(t, db, big); st.Session.State != "draft" {
		t.Fatalf("giant work stayed a draft: %+v", st.Session)
	}

	var ok map[string]map[string]any
	mustTaskJSON(t, db, "default", &ok, "", "session", "approve", id(small), "--as", "lead-bot")
	if se := ok["session"]; se["state"] != "running" || se["approval_basis"] != "lead" || se["approved_by"] != "lead-bot" {
		t.Fatalf("lead approval: %+v", se)
	}
	// Later the operator runs a plain approve: it is recorded as the operator's, even with an item open.
	setenv(t, "MONOAGENT_SESSION_TOKEN", "")
	setenv(t, "MONOAGENT_ACTOR", "")
	var person map[string]map[string]any
	mustTaskJSON(t, db, "default", &person, "", "session", "approve", id(big))
	if se := person["session"]; se["state"] != "running" || se["approval_basis"] != "operator" || se["approved_by"] != "you" {
		t.Fatalf("the person's approval: %+v", se)
	}
	if items := openItems(t, db); len(items) != 0 {
		t.Fatalf("an approved session's item must close: %+v", items)
	}
}
