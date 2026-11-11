package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

// The lead's close refusal asks the goal store, and whether the host runs goal checks comes from what the host published: a pin asked
// for counts as on (the file can only add a refusal), a missing file counts as off only while no check has ever run.
func TestTheLeadsGoalCloseGateFollowsTheHostsPublishedChecks(t *testing.T) {
	db := testdb.Open(t).DB
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, chat_mode, created_at, updated_at) VALUES ('default', 'c1', 'mission', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	person := tasks.Actor{Kind: tasks.Human}
	se, err := tasks.NewStore(db).CreateSession(ctx, "default", tasks.SessionInput{Title: "M", ConversationID: "c1", Kind: tasks.SessionKindMission}, person)
	if err != nil {
		t.Fatal(err)
	}
	gs := missionpursuit.NewStore(db)
	if _, err := gs.Set(ctx, "default", se.ID, []missionpursuit.Criterion{{Kind: "file", Spec: "a.txt", Expected: "exists"}, {Kind: "rubric", Spec: "it is good"}}, person); err != nil {
		t.Fatal(err)
	}
	if _, err := gs.Start(ctx, "default", se.ID, missionpursuit.AwayLimits{}, person); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "mono.db")
	gate := goalCloseGate(db, "default", dbPath)
	if why := gate(ctx, se.ID); why != "" {
		t.Errorf("no host published and no check ran: %q", why)
	}
	publish := func(doc string) {
		dir := coderhost.HostDir(dbPath)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, coderhost.HostInfoFile), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	publish(`{"pursuit":{"enabled":true,"verifierPin":"gemini/checker-1"}}`)
	why := gate(ctx, se.ID)
	if why == "" || strings.Contains(why, "it is good") {
		t.Errorf("the host runs goal checks and none passed: %q", why)
	}
	publish(`{"pursuit":{"enabled":true}}`)
	if why := gate(ctx, se.ID); why != "" {
		t.Errorf("the host does not run goal checks: %q", why)
	}
}
