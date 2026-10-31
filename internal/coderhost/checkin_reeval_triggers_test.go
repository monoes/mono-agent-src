package coderhost

import (
	"strings"
	"testing"
)

// The re-evaluation line of the digest is absent unless one of the host's own counts changed in a way that triggers it,
// and each trigger produces exactly its own reason.

func quietMission() *missionFact {
	return &missionFact{State: "running", Total: 4, Done: 1, V: snapshotV, Sess: 9, DirCur: 2, ResearchCur: 1, Dirs: []dirFact{
		{ID: 1, State: "running", Total: 2, Done: 1},
		{ID: 2, State: "running", Total: 2},
	}, SpentPct: 40}
}

func TestReevalLineIsAbsentForAQuietMissionAndPresentForEachTrigger(t *testing.T) {
	quiet := missionLine(func() *missionFact { m := quietMission(); m.Reeval = reevalReasons(quietMission(), m); return m }())
	if strings.Contains(quiet, reevalMark) || reevalLine(nil) != "" || reevalLine([]string{}) != "" {
		t.Fatalf("a quiet mission got the line: %q", quiet)
	}
	// progress, a rising spend below 80 percent and a stall below the limit are no trigger
	m := quietMission()
	m.Dirs[0].Done, m.Done, m.SpentPct, m.Dirs[1].Stall = 2, 2, 79, StalledCheckins-1
	if got := reevalReasons(quietMission(), m); got != nil {
		t.Fatalf("not a trigger: %v", got)
	}

	cases := []struct {
		name   string
		change func(m *missionFact)
		want   string
	}{
		{"a direction finished", func(m *missionFact) { m.Dirs, m.NewDone = m.Dirs[1:], 1 }, "1 direction(s) finished"},
		{"a direction newly blocked", func(m *missionFact) { m.Dirs[1].Blocked, m.Dirs[1].blockNow = 1, 7 }, "1 direction(s) newly blocked"},
		{"a direction paused", func(m *missionFact) { m.Dirs[1].State = "paused" }, "1 direction(s) newly blocked"},
		{"no progress for 3 check-ins", func(m *missionFact) { m.Dirs[1].Stall = StalledCheckins }, "1 direction(s) with no progress for 3 check-ins"},
		{"spend reaches 80 percent", func(m *missionFact) { m.SpentPct = 80 }, "80% of the mission budget spent"},
		{"a research session finished", func(m *missionFact) { m.ResearchCur, m.NewResearch = 2, 1 }, "1 new research report(s)"},
	}
	for _, c := range cases {
		now := quietMission()
		c.change(now)
		got := reevalReasons(quietMission(), now)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: reasons %v, want [%s]", c.name, got, c.want)
			continue
		}
		now.Reeval = got
		line := missionLine(now)
		if !strings.Contains(line, reevalMark+": "+c.want+".") {
			t.Errorf("%s: the digest lacks the line:\n%s", c.name, line)
		}
	}
}

// What has already been announced is not announced again, and a mission with no directions never gets the line.
func TestReevalReasonsFireOnTheChangeOnly(t *testing.T) {
	was, now := quietMission(), quietMission()
	was.SpentPct, now.SpentPct = 85, 90
	was.Dirs[0].BlockCur, now.Dirs[0].BlockCur, now.Dirs[0].blockNow = 7, 7, 7
	was.Dirs[1].Stall, now.Dirs[1].Stall = StalledCheckins, StalledCheckins
	if got := reevalReasons(was, now); got != nil {
		t.Fatalf("repeated: %v", got)
	}
	empty := &missionFact{State: "running", V: snapshotV, Sess: 9, SpentPct: 95, NewResearch: 5}
	if got := reevalReasons(quietMission(), empty); got != nil {
		t.Fatalf("no directions: %v", got)
	}
	if got := reevalReasons(nil, now); got != nil {
		t.Fatalf("first look: %v", got)
	}
}
