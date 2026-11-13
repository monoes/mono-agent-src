package coderhost

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/operator"
)

func TestPursuitSwitchRowsAreReservedAndDefaultToOnWithNoPin(t *testing.T) {
	for _, n := range []string{PursuitLoopConfig, PursuitJudgeConfig} {
		if !operator.ReservedConfig(n) {
			t.Fatalf("%s is not reserved: config set or an agent write path could change the pursuit switch", n)
		}
	}
	f := newHostFix(t)
	if off, err := f.st.PursuitOperatorOff(); err != nil || off {
		t.Fatalf("default operator-off = %v %v, want false (default is ON)", off, err)
	}
	if pin, err := f.st.SavedPursuitJudge(); err != nil || pin != "" {
		t.Fatalf("default saved judge = %q %v, want none (no model is picked for the person)", pin, err)
	}
}

func TestPursuitOperatorOffAndSavedJudgeRoundTrip(t *testing.T) {
	f := newHostFix(t)
	if err := f.st.SetPursuitOperatorOff(true); err != nil {
		t.Fatal(err)
	}
	if off, _ := f.st.PursuitOperatorOff(); !off {
		t.Fatal("explicit OFF was not kept")
	}
	if err := f.st.SetPursuitOperatorOff(false); err != nil {
		t.Fatal(err)
	}
	if off, _ := f.st.PursuitOperatorOff(); off {
		t.Fatal("`on` did not clear the explicit OFF")
	}
	if err := f.st.SetSavedPursuitJudge("openrouter/some-model:free"); err != nil {
		t.Fatal(err)
	}
	if pin, _ := f.st.SavedPursuitJudge(); pin != "openrouter/some-model:free" {
		t.Fatalf("pin = %q", pin)
	}
	if err := f.st.SetSavedPursuitJudge("not a pin"); err == nil {
		t.Fatal("a pin that does not parse was saved")
	}
	if err := f.st.SetSavedPursuitJudge(""); err != nil {
		t.Fatal(err)
	}
	if pin, _ := f.st.SavedPursuitJudge(); pin != "" {
		t.Fatalf("clear left %q", pin)
	}
}

// The default flip must not move any cap: the notice quotes them, so pin the values here.
func TestPursuitCapsAreUnchangedByTheDefaultFlip(t *testing.T) {
	if DefaultWakeCapUSD != 2.0 || missionpursuit.DefaultPct != 20 || missionpursuit.DefaultWakesMax != 6 ||
		missionpursuit.DefaultParkedMax != 3 || MaxPursuitWakesPerHour != 12 || MaxPursuitJudgeCallsPerHour != 12 {
		t.Fatalf("a pursuit cap changed: wake %v pct %v wakes %v parked %v host wakes %v judge %v",
			DefaultWakeCapUSD, missionpursuit.DefaultPct, missionpursuit.DefaultWakesMax, missionpursuit.DefaultParkedMax, MaxPursuitWakesPerHour, MaxPursuitJudgeCallsPerHour)
	}
}

func TestPursuitNoticeNamesEveryCapAndThePauseCommand(t *testing.T) {
	n := PursuitNotice()
	for _, want := range []string{"accept", "$2.00", "20%", "6 wake-ups", "3 parked", "12 wake-ups", "12 judge calls", PursuitPauseCommand, "closing the mission"} {
		if !strings.Contains(n, want) {
			t.Errorf("notice lacks %q: %s", want, n)
		}
	}
	if PursuitPauseCommand != "task session pursuit pause" {
		t.Fatalf("pause command = %q", PursuitPauseCommand)
	}
}
