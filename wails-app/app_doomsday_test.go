//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

const (
	dmOverview = `{"schema_version":1,"available":true,"run":{"run_id":"p/1","state":"running","active":true}}`
	dmNoRun    = `{"schema_version":1,"available":false,"reason":"no_run"}`
	dmOff      = `{"schema_version":1,"available":false,"reason":"off"}`
)

func resetProbe() {
	doomsdayProbe.mu.Lock()
	doomsdayProbe.at = time.Time{}
	doomsdayProbe.mu.Unlock()
}

func callCount(t *testing.T, log string) int {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(string(raw)), "\n"))
}

// The app can run overview, feed and stop and nothing else; a flag the verb does not list is refused.
func TestDoomsdayArgvIsAnAllowlist(t *testing.T) {
	for _, verb := range []string{"start", "approve", "grant", "pause", "resume", "finish", "rubric", "render", "status", "", "stop; start", "overview "} {
		if _, err := doomsdayArgv("p", verb); err == nil {
			t.Errorf("doomsday %q was allowed", verb)
		}
	}
	for _, bad := range []struct{ verb, flag string }{
		{"stop", "--yes"}, {"stop", "--limit=1"}, {"overview", "--before=1"}, {"feed", "--yes-i-lift=1"},
		{"feed", "--limit=-1"}, {"feed", "--limit=x"}, {"feed", "--limit"}, {"feed", "--limit=1 --x"},
	} {
		if _, err := doomsdayArgv("p", bad.verb, bad.flag); err == nil {
			t.Errorf("doomsday %s %s was allowed", bad.verb, bad.flag)
		}
	}
	got, err := doomsdayArgv("p", "feed", "--before=40", "--limit=20")
	want := []string{"--profile", "p", "--json", "doomsday", "feed", "--before=40", "--limit=20"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, %v", got, err)
	}
	if _, err := doomsdayArgv("", "overview"); err == nil {
		t.Fatal("no profile: the call must be refused")
	}
	keys := make([]string, 0)
	for k := range doomsdayVerbs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "feed,overview,stop" {
		t.Fatalf("the verb allowlist is %v", keys)
	}
}

// The App has exactly these Doomsday bindings; a new one fails here until it is reviewed.
func TestDoomsdayBindingsAreFour(t *testing.T) {
	typ := reflect.TypeOf(&App{})
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		if n := typ.Method(i).Name; strings.Contains(n, "Doomsday") {
			got = append(got, n)
		}
	}
	if strings.Join(got, ",") != "DoomsdayFeed,DoomsdayOverview,DoomsdayProbe,DoomsdayStop" {
		t.Fatalf("doomsday bindings: %v", got)
	}
}

func TestDoomsdayReadsRunExactArgvAndReturnVerbatim(t *testing.T) {
	log, _ := taskCLIFake(t, dmOverview, 0)
	a := newTaskTestApp(t)
	if got := a.DoomsdayOverview(); got != dmOverview {
		t.Fatalf("overview = %q", got)
	}
	a.DoomsdayFeed(0, 0)
	a.DoomsdayFeed(40, 20)
	want := []string{
		"--profile|work|--json|doomsday|overview|",
		"--profile|work|--json|doomsday|feed|",
		"--profile|work|--json|doomsday|feed|--before=40|--limit=20|",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv:\n%q\nwant:\n%q", got, want)
	}
	for _, bad := range [][2]int64{{-1, 0}, {0, -1}, {0, 201}} {
		if got := a.DoomsdayFeed(bad[0], bad[1]); !strings.Contains(got, "invalid_input") {
			t.Errorf("feed(%v) = %q", bad, got)
		}
	}
}

func TestDoomsdayReadShowsTheCLIsRefusal(t *testing.T) {
	taskCLIFake(t, `{"error":"operator only","code":"operator_only"}`, 3)
	a := newTaskTestApp(t)
	if got := a.DoomsdayOverview(); !strings.Contains(got, "operator_only") {
		t.Fatalf("refusal lost: %q", got)
	}
}

// A document over the cap is refused, not buffered whole (the work is bounded by the cap).
func TestDoomsdayReadOutputIsCapped(t *testing.T) {
	big := `{"schema_version":1,"x":"` + strings.Repeat("a", doomsdayMaxOutput+10) + `"}`
	taskCLIFake(t, big, 0)
	a := newTaskTestApp(t)
	if got := a.DoomsdayOverview(); !strings.Contains(got, "too large") || len(got) > 1000 {
		t.Fatalf("oversized document: %.80q", got)
	}
}

func TestCappedBufferKeepsTheHeadAndNeverBlocks(t *testing.T) {
	c := &cappedBuffer{max: 4}
	n, err := c.Write([]byte("abcdefgh"))
	if n != 8 || err != nil || string(c.buf) != "abcd" || !c.truncated {
		t.Fatalf("n=%d err=%v buf=%q", n, err, c.buf)
	}
	if n, _ := c.Write([]byte("zz")); n != 2 || string(c.buf) != "abcd" {
		t.Fatalf("write after full: %d %q", n, c.buf)
	}
}

func TestDoomsdayProbeAnswersAndCaches(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{dmOverview, `{"mode_available":true,"run_exists":true}`},
		{dmNoRun, `{"mode_available":true,"run_exists":false}`},
		{dmOff, `{"mode_available":false,"run_exists":false}`},
		{`{"error":"unknown command doomsday"}`, `{"mode_available":false,"run_exists":false}`},
		{`not json`, `{"mode_available":false,"run_exists":false}`},
	} {
		resetProbe()
		taskCLIFake(t, tc.doc, 0)
		if got := newTaskTestApp(t).DoomsdayProbe(); got != tc.want {
			t.Errorf("probe of %.40q = %s, want %s", tc.doc, got, tc.want)
		}
	}
	resetProbe()
	log, _ := taskCLIFake(t, dmOverview, 0)
	a := newTaskTestApp(t)
	for i := 0; i < 5; i++ {
		a.DoomsdayProbe()
	}
	if n := callCount(t, log); n != 1 {
		t.Fatalf("5 probes ran the CLI %d times, want 1 (cached)", n)
	}
}

func TestDoomsdayStopRunsOnlyStopAndShowsTheOutcome(t *testing.T) {
	log, _ := taskCLIFake(t, "stopped\n", 0)
	a := newTaskTestApp(t)
	var d struct {
		OK, Stopped bool
		Output      string
	}
	if json.Unmarshal([]byte(a.DoomsdayStop()), &d) != nil || !d.OK || !d.Stopped || d.Output != "stopped" {
		t.Fatalf("stop doc %+v", d)
	}
	if got := loggedArgs(t, log); len(got) != 1 || got[0] != "--profile|work|--json|doomsday|stop|" {
		t.Fatalf("argv %q", got)
	}
}

func TestDoomsdayStopNotStoppedIsNeverOK(t *testing.T) {
	taskCLIFake(t, "NOT stopped: 2 process(es) survive: 11, 12\n", 1)
	var d struct {
		OK, Stopped    bool
		Output, Detail string
	}
	if json.Unmarshal([]byte(newTaskTestApp(t).DoomsdayStop()), &d) != nil || d.OK || d.Stopped ||
		d.Output != "NOT stopped: 2 process(es) survive: 11, 12" {
		t.Fatalf("stop doc %+v", d)
	}
}

func TestDoomsdayStopShowsTheCLIGateRefusal(t *testing.T) {
	taskCLIFake(t, `{"error":"operator only","code":"operator_only"}`, 3)
	var d struct {
		OK           bool
		Detail, Code string
	}
	if json.Unmarshal([]byte(newTaskTestApp(t).DoomsdayStop()), &d) != nil || d.OK || d.Code != "operator_only" || d.Detail != "operator only" {
		t.Fatalf("stop doc %+v", d)
	}
}

// Defence in depth: a process the gate refuses never reaches the CLI.
func TestDoomsdayStopIsRefusedForANonOperatorWithoutRunningTheCLI(t *testing.T) {
	opgate.SetForTest(t, func(bool) string { return "an agent's shell" })
	log, _ := taskCLIFake(t, "stopped\n", 0)
	var d struct {
		OK           bool
		Detail, Code string
	}
	if json.Unmarshal([]byte(newTaskTestApp(t).DoomsdayStop()), &d) != nil || d.OK || d.Code != "operator_only" {
		t.Fatalf("stop doc %+v", d)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("the CLI ran")
	}
}

func TestDoomsdayStopRefusesASecondStopInFlight(t *testing.T) {
	log, _ := taskCLIFake(t, "stopped\n", 0)
	doomsdayStopMu.Lock()
	defer doomsdayStopMu.Unlock()
	if got := newTaskTestApp(t).DoomsdayStop(); !strings.Contains(got, `"code":"busy"`) {
		t.Fatalf("second stop: %q", got)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("the CLI ran")
	}
}
