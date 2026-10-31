package chat

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

type toggleFixture struct {
	dbPath string
	conv   ai.Conversation
	cfg    *Deps
}

func newToggleFixture(t *testing.T) toggleFixture {
	t.Helper()
	dbPath := newChatCLITestDB(t)
	conv, err := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return toggleFixture{dbPath, conv, testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})}
}

func (f toggleFixture) run(t *testing.T, cfg *Deps, args ...string) (sessionSettingsView, string, int) {
	t.Helper()
	out, code := runSessionsCLI(t, cfg, args...)
	var v sessionSettingsView
	if code == 0 {
		decodeChatJSON(t, out, &v)
	}
	return v, out, code
}

// openMission gives the fixture's conversation a mission session and returns its id.
func (f toggleFixture) openMission(t *testing.T) int64 {
	t.Helper()
	db, err := storage.NewDatabase(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	se, err := tasks.NewStore(db.DB).CreateSession(context.Background(), "default", tasks.SessionInput{Title: "M", Goal: "g", ConversationID: f.conv.ID, Kind: tasks.SessionKindMission}, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		t.Fatal(err)
	}
	return se.ID
}

func (f toggleFixture) endMission(t *testing.T, id int64, stop bool) { // stop, else close
	t.Helper()
	db, err := storage.NewDatabase(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ts, h := tasks.NewStore(db.DB), tasks.Actor{Kind: tasks.Human}
	if stop {
		_, err = ts.StopSession(context.Background(), "default", id, h)
	} else {
		_, err = ts.CloseSession(context.Background(), "default", id, tasks.SessionDone, "", h)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestMissionToggleSetAndReadBack(t *testing.T) {
	f := newToggleFixture(t)
	v, _, code := f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if code != 0 || v.Mission.On || v.Mission.Locked || !v.Mission.Available || v.Settings.Mission {
		t.Fatalf("default: exit %d %+v", code, v.Mission)
	}
	v, out, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "on")
	if code != 0 || !v.Mission.On || v.Mission.Locked || !v.Settings.Mission || strings.Join(v.Changed, ",") != "mission" {
		t.Fatalf("on: exit %d %s", code, out)
	}
	if v, _, _ = f.run(t, f.cfg, "settings", "get", f.conv.ID); !v.Mission.On {
		t.Fatalf("not stored: %+v", v.Mission)
	}
	if v, _, code = f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "off"); code != 0 || v.Mission.On {
		t.Fatalf("off with no mission session: exit %d %+v", code, v.Mission)
	}
	for _, bad := range []string{"yes", "true", ""} {
		if _, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", bad); code == 0 {
			t.Errorf("--mission %q was accepted", bad)
		}
	}
}

func TestMissionToggleIsLockedWhileASessionIsOpen(t *testing.T) {
	f := newToggleFixture(t)
	f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "on")
	id := f.openMission(t)
	sid := strconv.FormatInt(id, 10)

	v, _, _ := f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if !v.Mission.On || !v.Mission.Locked || v.Mission.SessionID != id || !strings.Contains(v.Mission.Reason, "running a mission") {
		t.Fatalf("locked view: %+v", v.Mission)
	}
	for _, args := range [][]string{
		{"settings", "set", f.conv.ID, "--mission", "off"},
		{"settings", "set", f.conv.ID, "--patch", `{"mission": false}`}, // the patch path is no way round
	} {
		out, code := runSessionsCLI(t, f.cfg, args...)
		if code == 0 || !strings.Contains(out, "session "+sid) || !strings.Contains(out, "task session stop "+sid) {
			t.Errorf("%v: exit %d %s", args, code, out)
		}
	}
	if v, _, _ = f.run(t, f.cfg, "settings", "get", f.conv.ID); !v.Mission.On || !v.Settings.Mission {
		t.Fatalf("a refused change turned it off: %+v", v.Mission)
	}
	// An unrelated change is not blocked by the lock.
	if _, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--patch", `{"checkin_minutes": 7}`); code != 0 {
		t.Fatal("the lock blocked an unrelated setting")
	}
	// Paused is still open.
	db, _ := storage.NewDatabase(f.dbPath)
	_, _ = tasks.NewStore(db.DB).PauseSession(context.Background(), "default", id, tasks.Actor{Kind: tasks.Human})
	db.Close()
	if _, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "off"); code == 0 {
		t.Fatal("a paused mission unlocked the toggle")
	}
	f.endMission(t, id, true)
	if v, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "off"); code != 0 || v.Mission.On || v.Mission.Locked {
		t.Fatalf("after the stop: exit %d %+v", code, v.Mission)
	}
}

func TestMissionToggleLockHoldsForAMissionStartedByTheCard(t *testing.T) {
	f := newToggleFixture(t) // the flag was never turned on
	id := f.openMission(t)
	if v, _, _ := f.run(t, f.cfg, "settings", "get", f.conv.ID); !v.Mission.On || !v.Mission.Locked || v.Settings.Mission {
		t.Fatalf("an open session reads as on and locked: %+v", v.Mission)
	}
	if _, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "off"); code == 0 {
		t.Fatal("off was accepted while the card's mission is open")
	}
	f.endMission(t, id, true)
	if v, _, _ := f.run(t, f.cfg, "settings", "get", f.conv.ID); v.Mission.On || v.Mission.Locked {
		t.Fatalf("a closed session still locks: %+v", v.Mission)
	}
}

func TestMissionToggleIsTheOperatorsOnly(t *testing.T) {
	f := newToggleFixture(t)
	agent := testDeps(&testConfig{DBPath: f.dbPath, ProfileID: "default", JSONOutput: true})
	agent.RequireOperator = func(what string) error { return errors.New("operator only: " + what) }
	for _, on := range []string{"on", "off"} {
		if _, _, code := f.run(t, agent, "settings", "set", f.conv.ID, "--mission", on); code == 0 {
			t.Errorf("a non-operator set --mission %s", on)
		}
	}
	if v, _, _ := f.run(t, f.cfg, "settings", "get", f.conv.ID); v.Mission.On {
		t.Fatal("a refused change turned it on")
	}
	if !strings.Contains(strings.Join(coderhost.OperatorOnly, ","), "mission") {
		t.Fatal("mission must be an operator-only setting")
	}
}

func TestMissionToggleNeedsMissionAccess(t *testing.T) {
	f := newToggleFixture(t)
	db, _ := storage.NewDatabase(f.dbPath)
	defer db.Close()
	ts := tasks.NewStore(db.DB)
	scoped := ai.Conversation{ID: f.conv.ID, Access: ai.AccessScoped}
	err := guardMissionToggle(context.Background(), db.DB, "default", scoped, true)
	if err == nil || !strings.Contains(err.Error(), "coding, workspace or full access") {
		t.Fatalf("scoped: %v", err)
	}
	if v := missionToggleOf(context.Background(), ts, "default", scoped, false); v.Available || v.Reason == "" {
		t.Fatalf("view: %+v", v)
	}
	for _, lvl := range []string{ai.AccessCoding, ai.AccessWorkspace, ai.AccessFull} {
		if err := guardMissionToggle(context.Background(), db.DB, "default", ai.Conversation{ID: f.conv.ID, Access: lvl}, true); err != nil {
			t.Errorf("%s: %v", lvl, err)
		}
	}
}
