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
	n := PursuitNotice(0)
	for _, want := range []string{"accept", "$2.00", "20%", "6 wake-ups", "3 parked", "12 wake-ups", "12 judge calls", PursuitPauseCommand + " <session-id>", "closing the mission"} {
		if !strings.Contains(n, want) {
			t.Errorf("notice lacks %q: %s", want, n)
		}
	}
	if PursuitPauseCommand != "task session pursuit pause" {
		t.Fatalf("pause command = %q", PursuitPauseCommand)
	}
}

// The notice quotes the cap the host really runs with, not the default, when MONOAGENT_PURSUIT_WAKE_CAP_USD raised it.
func TestPursuitNoticeQuotesThePublishedWakeCap(t *testing.T) {
	if n := PursuitNotice(5); !strings.Contains(n, "$5.00 a wake-up") || strings.Contains(n, "$2.00") {
		t.Fatalf("notice = %s", n)
	}
}

// An unrecognised spelling is OFF, never ON: a hand-written value must fail closed.
func TestPursuitLoopValueOnlyOnOneTrueAndEmptyIsOn(t *testing.T) {
	for _, v := range []string{"", " ", "on", "ON", " 1 ", "true", "True"} {
		if PursuitLoopValueOff(v) {
			t.Errorf("%q turned the loop off", v)
		}
	}
	for _, v := range []string{"off", "OFF", "false", "FALSE", "0", "no", "n", "disabled", "yes please", "x"} {
		if !PursuitLoopValueOff(v) {
			t.Errorf("%q left the loop on", v)
		}
	}
}

func TestAHandWrittenSavedRowSpellingIsAnExplicitOff(t *testing.T) {
	f := newHostFix(t)
	for _, v := range []string{"false", "OFF", "no", "disabled", "0"} {
		if err := f.st.setConfigValue(PursuitLoopConfig, v); err != nil {
			t.Fatal(err)
		}
		if off, err := f.st.PursuitOperatorOff(); err != nil || !off {
			t.Errorf("row %q: off = %v %v", v, off, err)
		}
	}
	for _, v := range []string{"", "on", "TRUE"} {
		if err := f.st.setConfigValue(PursuitLoopConfig, v); err != nil {
			t.Fatal(err)
		}
		if off, _ := f.st.PursuitOperatorOff(); off {
			t.Errorf("row %q turned the loop off", v)
		}
	}
}

func TestPendingPursuitRestart(t *testing.T) {
	for name, c := range map[string]struct {
		off  bool
		pin  string
		h    PursuitHostInfo
		want bool
	}{
		"same, idle":                    {false, "", PursuitHostInfo{Missing: PursuitMissingJudgePin}, false},
		"pin saved after an idle start": {false, "a/b", PursuitHostInfo{Missing: PursuitMissingJudgePin}, true},
		"same pin":                      {false, "a/b", PursuitHostInfo{Enabled: true, Pin: "a/b", PinSource: "saved"}, false},
		"pin changed":                   {false, "c/d", PursuitHostInfo{Enabled: true, Pin: "a/b", PinSource: "saved"}, true},
		"pin cleared":                   {false, "", PursuitHostInfo{Enabled: true, Pin: "a/b", PinSource: "saved"}, true},
		"host pin is from env":          {false, "c/d", PursuitHostInfo{Enabled: true, Pin: "a/b", PinSource: "env"}, false},
		"off saved, host running":       {true, "a/b", PursuitHostInfo{Enabled: true, Pin: "a/b", PinSource: "saved"}, true},
		"off saved, host off":           {true, "", PursuitHostInfo{OperatorOff: true, OffSource: "saved"}, false},
		"off cleared, host off":         {false, "", PursuitHostInfo{OperatorOff: true, OffSource: "saved"}, true},
		"host off from env":             {false, "a/b", PursuitHostInfo{OperatorOff: true, OffSource: "env"}, false},
	} {
		if got := PendingPursuitRestart(c.off, c.pin, c.h); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
