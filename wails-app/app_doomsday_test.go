//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	doomsdayProbe.invalidate()
}

func callCount(t *testing.T, log string) int {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(string(raw)), "\n"))
}

// doomsdayArgv runs overview, feed, probe and stop and nothing else; a flag the verb does not list is refused. start and
// render are refused here on purpose: they take a charter and a phrase and have their own fixed argv (below).
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
	if strings.Join(keys, ",") != "feed,overview,probe,stop" {
		t.Fatalf("the verb allowlist is %v", keys)
	}
}

// The App has exactly these Doomsday bindings; a new one fails here until it is reviewed. The read bindings, the probe
// and Stop were four; the open-ended start form adds three, reviewed as a set: OpenEntry (the probe verb), Render (the
// preview) and Start (operator-gated, the typed phrase re-derived by the CLI, TestDoomsdayRenderAndStartNeedTheOperator).
func TestDoomsdayBindingsAreExactlyTheReviewedOnes(t *testing.T) {
	typ := reflect.TypeOf(&App{})
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		if n := typ.Method(i).Name; strings.Contains(n, "Doomsday") {
			got = append(got, n)
		}
	}
	if strings.Join(got, ",") != "DoomsdayFeed,DoomsdayOpenEntry,DoomsdayOverview,DoomsdayProbe,DoomsdayRender,DoomsdayStart,DoomsdayStop" {
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
// The CLI's read-failure document (exit 1, JSON on stdout) reaches the page verbatim as a structured error: not an
// {"error"} wrapper of the exit status and never the "off" document, for every error_code and for the feed too.
func TestDoomsdayReadErrorDocumentOnNonZeroExitIsStructured(t *testing.T) {
	root := filepath.Join("..", "internal", "cli", "doomsday", "testdata")
	for _, tc := range []struct{ verb, file, code string }{
		{"overview", "controlroom_overview_error.golden.json", "db_unavailable"},
		{"feed", "controlroom_feed_error.golden.json", "read_timeout"},
	} {
		raw, err := os.ReadFile(filepath.Join(root, tc.file))
		if err != nil {
			t.Fatal(err)
		}
		taskCLIFake(t, string(raw), 1)
		a := newTaskTestApp(t)
		got := a.DoomsdayOverview()
		if tc.verb == "feed" {
			got = a.DoomsdayFeed(0, 0)
		}
		var d struct {
			Available bool   `json:"available"`
			Reason    string `json:"reason"`
			ErrorCode string `json:"error_code"`
			Error     string `json:"error"`
		}
		if json.Unmarshal([]byte(got), &d) != nil || d.Available || d.Reason != "error" || d.ErrorCode != tc.code || d.Error != "" {
			t.Errorf("%s on exit 1 = %q", tc.verb, got)
		}
	}
	// A non-zero exit with some other document is still a failed command.
	taskCLIFake(t, dmOff, 1)
	if got := newTaskTestApp(t).DoomsdayOverview(); strings.Contains(got, `"reason":"off"`) {
		t.Errorf("an off document on a failed exit was passed through: %q", got)
	}
}

// A failed read says nothing about the mode or the run: the probe answers an error (the nav keeps its last good
// answer) and does not cache it, rather than answering "unavailable".
func TestDoomsdayProbeOfAReadErrorIsAnErrorAndNotCached(t *testing.T) {
	resetProbe()
	raw, err := os.ReadFile(filepath.Join("..", "internal", "cli", "doomsday", "testdata", "controlroom_overview_error.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	log, _ := taskCLIFake(t, string(raw), 1)
	a := newTaskTestApp(t)
	for i := 0; i < 3; i++ {
		got := a.DoomsdayProbe()
		if !strings.Contains(got, `"error"`) || strings.Contains(got, "mode_available") {
			t.Fatalf("probe of a read error = %s", got)
		}
	}
	if n := callCount(t, log); n != 3 {
		t.Fatalf("an error answer was cached: CLI ran %d times for 3 probes", n)
	}
}

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
	got := newTaskTestApp(t).DoomsdayStop()
	// A broken guard has Stop release the mutex we hold: fail cleanly instead of panicking on a double Unlock.
	if doomsdayStopMu.TryLock() {
		doomsdayStopMu.Unlock()
		t.Fatalf("the stop guard let a second stop through: %q", got)
	}
	doomsdayStopMu.Unlock()
	if !strings.Contains(got, `"code":"busy"`) {
		t.Fatalf("second stop: %q", got)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("the CLI ran")
	}
}

// Stop's reply must not wait for a probe that is still running the CLI, and that probe's late answer must not be cached
// over the invalidation.
func TestDoomsdayStopDoesNotWaitForAnInFlightProbe(t *testing.T) {
	dir := t.TempDir()
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t,
		"case \"$*\" in *overview*) : > '"+started+"'; i=0\n"+
			"while [ ! -e '"+release+"' ] && [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done\n"+
			"echo '"+dmOverview+"';; *) echo stopped;; esac\n"))
	resetProbe()
	a := newTaskTestApp(t)
	probed := make(chan string, 1)
	go func() { probed <- a.DoomsdayProbe() }()
	waitFor(t, func() bool { _, err := os.Stat(started); return err == nil })
	stopped := make(chan string, 1)
	go func() { stopped <- a.DoomsdayStop() }()
	select {
	case got := <-stopped:
		if !strings.Contains(got, `"ok":true`) {
			t.Errorf("stop doc %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Error("Stop waited for the in-flight probe")
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probed:
	case <-time.After(10 * time.Second):
		t.Fatal("the probe never returned")
	}
	doomsdayProbe.mu.Lock()
	cachedAt := doomsdayProbe.at
	doomsdayProbe.mu.Unlock()
	if !cachedAt.IsZero() {
		t.Errorf("a probe that began before Stop was cached over the invalidation")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("condition not reached in 5s")
}

// The real CLI prints the control room's error document and then main's generic envelope on stdout. Only the first
// value reaches the page, as one parseable document.
func TestDoomsdayReadErrorWithTheRealTwoDocumentOutput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "internal", "cli", "doomsday", "testdata", "controlroom_overview_error.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	first := strings.TrimSpace(string(raw))
	taskCLIFake(t, first+"\n"+`{"error":"doomsday: the run's database could not be read"}`+"\n", 1)
	a := newTaskTestApp(t)
	got := a.DoomsdayOverview()
	var d struct {
		Reason    string `json:"reason"`
		ErrorCode string `json:"error_code"`
		Error     string `json:"error"`
	}
	if json.Unmarshal([]byte(got), &d) != nil || d.Reason != "error" || d.ErrorCode == "" || d.Error != "" {
		t.Fatalf("two-document read error = %q", got)
	}
}

func TestDoomsdayReadErrorParsesTheFirstValueOnly(t *testing.T) {
	good := `{"schema_version":1,"available":false,"reason":"error","error_code":"read_failed"}`
	for _, tc := range []struct {
		name, out string
		ok        bool
	}{
		{"single", good, true},
		{"two documents", good + "\n" + `{"error":"x"}`, true},
		{"trailing garbage", good + "\nnot json at all", true},
		{"empty", "", false},
		{"garbage", "not json", false},
		{"truncated", good[:len(good)-6], false},
		{"healthy first", dmOverview + "\n" + good, false},
		{"off first", dmOff + "\n" + good, false},
		{"envelope first", `{"error":"x"}` + "\n" + good, false},
	} {
		first, ok := doomsdayReadError(tc.out)
		if ok != tc.ok || (ok && first != good) {
			t.Errorf("%s: (%q, %v)", tc.name, first, ok)
		}
	}
}

// Anything that is not a definite document is an error that is not cached, so the page keeps its last good probe.
func TestDoomsdayProbeFailuresAreErrorsAndNotCached(t *testing.T) {
	errDoc, _ := os.ReadFile(filepath.Join("..", "internal", "cli", "doomsday", "testdata", "controlroom_overview_error.golden.json"))
	for _, tc := range []struct {
		name, body string
	}{
		{"timeout", "exec sleep 5\n"},
		{"exit1 empty", "exit 1\n"},
		{"exit0 garbage", "echo not json\n"},
		{"exit0 empty", "exit 0\n"},
		{"old cli error doc", `echo '{"error":"unknown command doomsday"}'` + "\nexit 0\n"},
		{"refusal", `echo '{"error":"operator only","code":"operator_only"}'` + "\nexit 3\n"},
		{"unavailable doc on a failed exit", "echo '" + dmOff + "'\nexit 1\n"},
		{"unknown reason", `echo '{"schema_version":1,"available":false,"reason":"weird"}'` + "\n"},
		{"wrong schema", `echo '{"schema_version":2,"available":true}'` + "\n"},
		{"read error doc", "echo '" + strings.TrimSpace(string(errDoc)) + "'\nexit 1\n"},
		{"spawn failure", ""},
	} {
		resetProbe()
		log := filepath.Join(t.TempDir(), "calls")
		if tc.body == "" {
			t.Setenv("MONOAGENTCLI_BIN", filepath.Join(t.TempDir(), "missing"))
		} else {
			t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "echo x >> '"+log+"'\n"+tc.body))
		}
		a := newTaskTestApp(t)
		if tc.name == "timeout" {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			a.ctx = ctx
		}
		for i := 0; i < 2; i++ {
			got := a.DoomsdayProbe()
			if !strings.Contains(got, `"error"`) || strings.Contains(got, "mode_available") {
				t.Errorf("%s: probe = %s", tc.name, got)
			}
		}
		if tc.body != "" && tc.name != "timeout" { // the shared deadline has passed for the second probe
			if n := callCount(t, log); n != 2 {
				t.Errorf("%s: an error answer was cached (CLI ran %d times for 2 probes)", tc.name, n)
			}
		}
	}
}

// The two verbs that take a charter have one fixed argv each: the charter on stdin, the phrase and the boss session as
// --flag=value, nothing else. No other verb gets a charter, and no flag of the script start or the lift is ever built.
func TestDoomsdayCharterArgvIsFixed(t *testing.T) {
	for _, verb := range []string{"stop", "overview", "probe", "finish", "rubric", "status", "start; stop", ""} {
		if _, err := doomsdayCharterArgv("p", verb, "x", 0); err == nil {
			t.Errorf("doomsday %q was allowed with a charter", verb)
		}
	}
	if _, err := doomsdayCharterArgv("", "render", "", 0); err == nil {
		t.Fatal("no profile: the call must be refused")
	}
	got, err := doomsdayCharterArgv("p", "start", "-x --yes-i-lift", 7)
	want := []string{"--profile", "p", "--json", "doomsday", "start", "--charter", "-", "--gui-phrase=-x --yes-i-lift", "--boss-session=7"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, %v", got, err)
	}
	got, _ = doomsdayCharterArgv("p", "render", "ignored", 9)
	want = []string{"--profile", "p", "--json", "doomsday", "render", "--charter", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("render argv = %q", got)
	}
	for _, a := range got {
		for _, bad := range []string{"--yes-i-lift", "--hash", "--everything", "--lift"} {
			if a == bad {
				t.Fatalf("argv has %s", bad)
			}
		}
	}
}
