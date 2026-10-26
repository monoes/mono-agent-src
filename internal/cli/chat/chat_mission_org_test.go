package chat

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// leadToolNames reads the names of the tools the lead of the last turn was given.
func leadToolNames(t *testing.T, argsLog string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(argsLog), "lead-tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range []string{"org_spawn", "mission_propose", "mission_plan", "mission_status", "mission_next", "mission_assign", "mission_report", "mission_verify", "mission_finish"} {
		if strings.Contains(string(raw), `"`+n+`"`) {
			names = append(names, n)
		}
	}
	return strings.Join(names, ",")
}

// The mission tools reach the lead by stages: mission_propose below a confirmation, the
// planning tools with --mission, and the run tools (next, assign, report, verify, finish) only
// once the conversation has a session, which also keeps it confirmed on later turns.
func TestDynamicOrgLeadGetsMissionToolsByStage(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeOrgMonomind(t)
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessTools, monomind.CapAgentExecAccessRead)...)
	withCoderScan(t, orgScan(true))
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	store := openTestChatStore(t, dbPath)
	conv, err := store.CreateConversationMode("default", "agent", "general", "claude", "", "opus", ai.ModeCoder, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, code := runChatHistory(t, dbPath, "default", "set-org", conv.ID, "dynamic"); code != 0 {
		t.Fatalf("set-org: exit %d", code)
	}
	cs, closeCS := confirmStore(t, dbPath)
	defer closeCS()
	turn := func(id string, extra ...string) {
		t.Helper()
		args := []string{"--conversation", conv.ID, "--turn", id}
		tok := ""
		if len(extra) > 0 && extra[0] == "confirm" { // the host's token for an operator-confirmed send
			tok = confirmedToken(t, cs, conv.ID, id, 0)
		}
		t.Setenv(coderhost.MissionTokenEnv, tok)
		if out, err := runChatCmd(t, dbPath, bin, append(args, "--", "build it")...); err != nil {
			t.Fatalf("turn %s: %v\n%s", id, err, out)
		}
	}

	turn("t1")
	if got := leadToolNames(t, argsLog); got != "org_spawn,mission_propose" {
		t.Errorf("before a confirmation: %s", got)
	}
	turn("t2", "confirm")
	if got := leadToolNames(t, argsLog); got != "org_spawn,mission_propose,mission_plan,mission_status" {
		t.Errorf("confirmed, no session yet: %s", got)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	se, err := tasks.NewStore(db).CreateSession(context.Background(), "default", tasks.SessionInput{Title: "M", Goal: "g", ConversationID: conv.ID}, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		t.Fatal(err)
	}
	turn("t3") // no flag: the open session keeps it confirmed
	if got := leadToolNames(t, argsLog); got != "org_spawn,mission_propose,mission_plan,mission_status,mission_next,mission_assign,mission_report,mission_verify,mission_finish" {
		t.Errorf("with a session: %s", got)
	}
	if _, err := tasks.NewStore(db).StopSession(context.Background(), "default", se.ID, tasks.Actor{Kind: tasks.Human}); err != nil {
		t.Fatal(err)
	}
	turn("t4")
	if got := leadToolNames(t, argsLog); got != "org_spawn,mission_propose" {
		t.Errorf("after the session was stopped the conversation is unconfirmed again: %s", got)
	}
}
