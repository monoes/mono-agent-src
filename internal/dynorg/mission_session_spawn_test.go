package dynorg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// spawnerFake is a fakeSpawner that can also start sessions.
type spawnerFake struct {
	*fakeSpawner
	got []SessionSpawnArgs
	err error
}

func (s *spawnerFake) SpawnSession(_ context.Context, a SessionSpawnArgs) (SessionSpawnResult, error) {
	s.got = append(s.got, a)
	if s.err != nil {
		return SessionSpawnResult{}, s.err
	}
	return SessionSpawnResult{ConversationID: "conv-1", TaskSessionID: 7, Kind: a.Kind, Title: a.Title, Depth: 2, Access: "coding"}, nil
}

func spawnRig(t *testing.T) (*missionRig, *spawnerFake) {
	t.Helper()
	r, fs := childRig(t, "off", rigOpts{})
	sp := &spawnerFake{fakeSpawner: fs}
	r.mc.Children = sp
	return r, sp
}

func TestMissionSessionSpawnPassesTheArgumentsToTheHostAndNothingElse(t *testing.T) {
	r, sp := spawnRig(t)
	r.add(spec{title: "a", files: "a.go"})
	r.approve()
	out, err := r.c.Handle(tbg, ToolMissionSessionSpawn, []byte(`{"title":"Docs","goal":"write","kind":"direction","reason":"parallel","task_ids":[3,4],"budget_usd":2.5,"access":"coding","session":99,"cwd":"/","caller":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.got) != 1 {
		t.Fatalf("calls = %+v", sp.got)
	}
	g := sp.got[0]
	if g.Title != "Docs" || g.Kind != "direction" || g.Reason != "parallel" || len(g.TaskIDs) != 2 || g.BudgetUSD != 2.5 || g.Access != "coding" {
		t.Fatalf("args = %+v", g)
	}
	var res SessionSpawnResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.TaskSessionID != 7 || !strings.Contains(res.Note, "operator approves") {
		t.Fatalf("result = %s (%v)", out, err)
	}
}

func TestMissionSessionSpawnNeedsAHostThatCanAndAConfirmedMission(t *testing.T) {
	r, _ := childRig(t, "off", rigOpts{}) // a spawner that cannot start sessions
	r.add(spec{title: "a", files: "a.go"})
	r.approve()
	if _, err := r.c.Handle(tbg, ToolMissionSessionSpawn, []byte(`{"title":"x","kind":"task","reason":"parallel"}`)); err == nil || !strings.Contains(err.Error(), "workers of this turn") {
		t.Fatalf("no session spawner: %v", err)
	}
	r2, _ := spawnRig(t)
	r2.mc.Children = nil
	if _, err := r2.c.Handle(tbg, ToolMissionSessionSpawn, []byte(`{"title":"x","kind":"task","reason":"parallel"}`)); err == nil {
		t.Fatal("no children at all")
	}
}

func TestMissionSessionSpawnAtTheWorkingDepthSaysSoOnceAndKeepsTheError(t *testing.T) {
	r, sp := spawnRig(t)
	r.add(spec{title: "a", files: "a.go"})
	r.approve()
	sp.err = fmt.Errorf("%w: %w", ErrSpawnDepth, errors.New("limit reached: depth 4 would pass max_depth 3"))
	for i := 0; i < 2; i++ {
		if _, err := r.c.Handle(tbg, ToolMissionSessionSpawn, []byte(`{"title":"x","kind":"task","reason":"parallel"}`)); !errors.Is(err, ErrSpawnDepth) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := countNotices(r.em, "mission.child_fallback"); n != 1 {
		t.Fatalf("notices = %d, want one", n)
	}
	sp.err = ErrSpawnAdvised
	if _, err := r.c.Handle(tbg, ToolMissionSessionSpawn, []byte(`{"title":"x","kind":"task","reason":"parallel"}`)); !errors.Is(err, ErrSpawnAdvised) {
		t.Fatalf("advice: %v", err)
	}
}

func TestMissionSessionSpawnIsOnlyTheLeadsTool(t *testing.T) {
	if !strings.HasPrefix(missionSessionSpawnSpec().Name, "mission_") {
		t.Fatal("name")
	}
	for _, s := range childToolSpecs([]string{"read-only"}) {
		if s.Name == ToolMissionSessionSpawn {
			t.Fatal("a helper the lead let spawn has the lead's session tool")
		}
	}
	for _, s := range toolSpecs([]string{"read-only"}) {
		if s.Name == ToolMissionSessionSpawn {
			t.Fatal("the base org tools of every lead include it: only a lead of a confirmed mission with a plan has it")
		}
	}
	m := &MissionConfig{Store: missionStore(t), ProfileID: missionProfile, ConversationID: "c1", Actor: "mission-lead", Access: "coding"}
	if got := MissionRunToolSpecs(context.Background(), m); len(got) != 0 {
		t.Fatalf("an unconfirmed lead has %d mission tools", len(got))
	}
}

// coderhost reads the plan meta line with its own copy of the format (it cannot import this package); the two must agree.
func TestCoderhostReadsThePlanMetaLineThisPackageWrites(t *testing.T) {
	notes := notesWithMeta("some context\nmore", TaskMeta{Files: []string{"b.go", "a.go"}, Size: "s", Risk: "high"})
	files, size, risk, ok := coderhost.ParsePlanMeta(notes)
	want, wok := ParseTaskMeta(notes)
	if !ok || !wok || size != want.Size || risk != want.Risk || len(files) != len(want.Files) || files[0] != want.Files[0] || size != "s" || risk != "high" {
		t.Fatalf("coderhost read %v %q %q %v; dynorg read %+v", files, size, risk, ok, want)
	}
	if _, _, _, ok := coderhost.ParsePlanMeta("a task the operator wrote"); ok {
		t.Fatal("a meta line out of nothing")
	}
}
