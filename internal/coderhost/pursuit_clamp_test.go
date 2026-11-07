package coderhost

import "testing"

func TestPursuitClampSendsEveryClassToTheUserAndParks(t *testing.T) {
	for _, who := range []string{WhoUser, WhoMaster, WhoModel, "", "bogus"} {
		for _, fb := range []string{FallbackPark, FallbackTakeDefault, "", "bogus"} {
			got := PursuitClamp(Decision{Answerer: who, Fallback: fb})
			if got != (Decision{Answerer: WhoUser, Fallback: FallbackPark}) {
				t.Errorf("PursuitClamp(%q, %q) = %+v, want user and park", who, fb, got)
			}
		}
	}
}

func TestDecideInPursuitIsUserAndParkForEveryClassAndSetting(t *testing.T) {
	presets := []string{"ask-always", "ask-on-risk", "decide-and-log", "bogus", ""}
	for _, preset := range presets {
		for _, class := range append(append([]string{}, GapClasses...), "unknown_class") {
			for _, rev := range []bool{false, true} {
				for _, master := range []bool{false, true} {
					s := DefaultSettings()
					s.AutonomyPreset = preset
					s.pursuing = true
					if got := Decide(s, class, rev, master); got != (Decision{WhoUser, FallbackPark}) {
						t.Errorf("preset %q class %s rev=%v master=%v: %+v", preset, class, rev, master, got)
					}
					// a per-class override that names the model and a default changes nothing in pursuit (a non-hard class)
					s.AutonomyClasses = map[string]AutonomyChoice{class: {Who: WhoModel, Fallback: FallbackTakeDefault}}
					if got := Decide(s, class, rev, master); got != (Decision{WhoUser, FallbackPark}) {
						t.Errorf("override, class %s: %+v", class, got)
					}
				}
			}
		}
	}
}

// Without a pursuit Decide is what it was: the preset, the per-class override, then the clamp.
func TestDecideWithoutPursuitIsUnchanged(t *testing.T) {
	reference := func(s Settings, class string, rev, master bool) Decision {
		d := presetDecision(s.AutonomyPreset, class, rev)
		if c, ok := s.AutonomyClasses[class]; ok {
			d = Decision{Answerer: c.Who, Fallback: c.Fallback}
		}
		return clamp(d, class, master)
	}
	sawModel, sawMaster, sawDefault := false, false, false
	for _, preset := range []string{"ask-always", "ask-on-risk", "decide-and-log", "bogus", ""} {
		for _, class := range append(append([]string{}, GapClasses...), "unknown_class") {
			for _, rev := range []bool{false, true} {
				for _, master := range []bool{false, true} {
					for _, ov := range []map[string]AutonomyChoice{nil, {class: {Who: WhoModel, Fallback: FallbackTakeDefault}}, {class: {Who: WhoMaster, Fallback: FallbackPark}}} {
						s := DefaultSettings()
						s.AutonomyPreset, s.AutonomyClasses = preset, ov
						got, want := Decide(s, class, rev, master), reference(s, class, rev, master)
						if got != want {
							t.Errorf("preset %q class %s rev=%v master=%v ov=%v: %+v, want %+v", preset, class, rev, master, ov, got, want)
						}
						sawModel = sawModel || got.Answerer == WhoModel
						sawMaster = sawMaster || got.Answerer == WhoMaster
						sawDefault = sawDefault || got.Fallback == FallbackTakeDefault
					}
				}
			}
		}
	}
	if !sawModel || !sawMaster || !sawDefault {
		t.Errorf("the table never reached model=%v master=%v default=%v: it proves nothing", sawModel, sawMaster, sawDefault)
	}
}

func TestAwayStateWaitsForYou(t *testing.T) {
	const budget = 1000
	for _, c := range []struct {
		name  string
		a     AwayState
		waits bool
	}{
		{"nothing happened", AwayState{ParkedMax: 3, Pct: 20, BudgetMicro: budget}, false},
		{"one below the parked limit", AwayState{ParkedMax: 3, Pct: 20, BudgetMicro: budget, Parked: 2}, false},
		{"at the parked limit", AwayState{ParkedMax: 3, Pct: 20, BudgetMicro: budget, Parked: 3}, true},
		{"over the parked limit", AwayState{ParkedMax: 3, Pct: 20, BudgetMicro: budget, Parked: 9}, true},
		{"one below the spend share", AwayState{ParkedMax: 3, Pct: 20, BudgetMicro: budget, SpentMicro: 199}, false},
		{"at the spend share", AwayState{ParkedMax: 3, Pct: 20, BudgetMicro: budget, SpentMicro: 200}, true},
		{"over the spend share", AwayState{ParkedMax: 3, Pct: 20, BudgetMicro: budget, SpentMicro: 900}, true},
		{"the whole budget", AwayState{ParkedMax: 3, Pct: 100, BudgetMicro: budget, SpentMicro: 1000}, true},
		{"1 percent of a small budget", AwayState{ParkedMax: 3, Pct: 1, BudgetMicro: 150, SpentMicro: 2}, true},
		{"1 percent, below", AwayState{ParkedMax: 3, Pct: 1, BudgetMicro: 150, SpentMicro: 1}, false},
		{"limits never set", AwayState{BudgetMicro: budget}, true},
		{"no budget known, nothing spent", AwayState{ParkedMax: 3, Pct: 20}, false},
		{"no budget known, some spent", AwayState{ParkedMax: 3, Pct: 20, SpentMicro: 1}, true},
		{"negative figures count as none", AwayState{ParkedMax: 3, Pct: 20, BudgetMicro: budget, Parked: -5, SpentMicro: -5}, false},
		{"huge figures do not wrap", AwayState{ParkedMax: 3, Pct: 100, BudgetMicro: 1 << 62, SpentMicro: 1<<62 - 1}, true},
	} {
		if got := c.a.WaitingForYou(); got != c.waits {
			t.Errorf("%s: WaitingForYou = %v, want %v", c.name, got, c.waits)
		}
	}
}

func TestAwayStateCountsAndResetsOnAPersonAction(t *testing.T) {
	a := AwayState{ParkedMax: 2, Pct: 50, BudgetMicro: 100}
	a.Park()
	if a.WaitingForYou() {
		t.Fatal("one parked item of two waits")
	}
	a.Park()
	if !a.WaitingForYou() {
		t.Fatal("two parked items of two do not wait")
	}
	a.Reset()
	if a.WaitingForYou() || a.Parked != 0 || a.SpentMicro != 0 || a.ParkedMax != 2 || a.Pct != 50 || a.BudgetMicro != 100 {
		t.Errorf("after a person action: %+v (counters 0, limits and budget kept)", a)
	}
	a.Spend(49)
	if a.WaitingForYou() {
		t.Fatal("49 of 100 at 50 percent waits")
	}
	a.Spend(1)
	if !a.WaitingForYou() {
		t.Fatal("50 of 100 at 50 percent does not wait")
	}
	a.Spend(-1000) // a negative spend never lowers the count
	if !a.WaitingForYou() {
		t.Fatal("a negative spend cleared the wait")
	}
	a.Reset()
	if a.WaitingForYou() {
		t.Fatal("reset did not clear the wait")
	}
}
