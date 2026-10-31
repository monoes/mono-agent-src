package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/ref/catalog"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The mission map in AGENTS.md and the trust-boundary summary in SECURITY.md state numbers and names that live in
// the code. These tests hold only the claims that can be checked mechanically, so that changing a constant, a list
// or a command without the text fails here.

// docSection is the text of the section that starts at heading, up to the next heading of the same or a higher level.
func docSection(t *testing.T, rel, heading string) string {
	t.Helper()
	text := docText(t, rel)
	i := strings.Index(text, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("%s has no section %q", rel, heading)
	}
	rest := text[i+1+len(heading):]
	level := len(heading) - len(strings.TrimLeft(heading, "#"))
	end := regexp.MustCompile(`(?m)^#{1,` + strconv.Itoa(level) + `} `).FindStringIndex(rest)
	if end != nil {
		rest = rest[:end[0]]
	}
	return strings.Join(strings.Fields(rest), " ")
}

// wantAll fails for each wanted string that the (whitespace-squashed) section does not contain.
func wantAll(t *testing.T, where, section string, wants map[string]string) {
	t.Helper()
	for what, s := range wants {
		if !strings.Contains(section, s) {
			t.Errorf("%s does not state %s (%q): the text or the code changed", where, what, s)
		}
	}
}

func frontendConst(t *testing.T, rel, name string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "wails-app", "frontend", "src", rel))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)\b` + name + `\s*=\s*(\d+)`).FindSubmatch(raw)
	if m == nil {
		t.Fatalf("%s has no %s", rel, name)
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n
}

func backtickedNames(line string) []string {
	var out []string
	for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(line, -1) {
		out = append(out, m[1])
	}
	return out
}

// listAfter returns the backticked names between marker and the next full stop followed by a space.
func listAfter(t *testing.T, section, marker string) []string {
	t.Helper()
	i := strings.Index(section, marker)
	if i < 0 {
		t.Fatalf("the section has no %q", marker)
	}
	rest := section[i+len(marker):]
	if j := strings.Index(rest, ". "); j >= 0 {
		rest = rest[:j]
	}
	return backtickedNames(rest)
}

func TestAgentsMdMissionMapMatchesTheCode(t *testing.T) {
	sec := docSection(t, "AGENTS.md", "### Missions and the session tree: how it fits together")
	def := coderhost.DefaultSettings()
	if coderhost.AskInterval != time.Minute {
		t.Errorf("AskInterval is %v: the text says one ask a minute", coderhost.AskInterval)
	}
	wantAll(t, "AGENTS.md (mission map)", sec, map[string]string{
		"the hard maximum depth":     fmt.Sprintf("a hard maximum of %d levels", tasks.HardMaxSessionDepth),
		"the working depth default":  fmt.Sprintf("(default %d, operator-only to raise to %d", def.MaxDepth, tasks.HardMaxSessionDepth),
		"the open-children fence":    fmt.Sprintf("at most %d open (draft, running or paused) children", tasks.MaxOpenChildren),
		"the live cap per tree":      fmt.Sprintf("at most %d live (not stopped) sessions below its root", coderhost.MaxLiveSessionsPerTree),
		"the spawn rate":             fmt.Sprintf("%d per %d minutes", coderhost.SpawnRateMax, int(coderhost.SpawnRateWindow.Minutes())),
		"the giant task count":       fmt.Sprintf("`tasks.GiantMaxTasks` %d tasks", tasks.GiantMaxTasks),
		"the giant budget share":     fmt.Sprintf("`tasks.GiantBudgetShare` %d%%", int(tasks.GiantBudgetShare*100)),
		"the open asks per asker":    fmt.Sprintf("%d open asks per asker, one a minute", coderhost.MaxOpenAsks),
		"the ask size":               fmt.Sprintf("%d KiB each (%d KiB open)", coderhost.MaxAskBytes>>10, coderhost.MaxOpenAskBytes>>10),
		"the open asks per ancestor": fmt.Sprintf("%d open per ancestor", coderhost.MaxOpenAsksPerTarget),
		"the ask lifetime":           fmt.Sprintf("a normal ask expires after %d hours", int(coderhost.AskTTL.Hours())),
		"the orbit's circles":        fmt.Sprintf("(at most %d drawn", frontendConst(t, filepath.Join("lib", "sessionOrbit.js"), "MAX_SATELLITES")),
		"the orbit's refresh":        fmt.Sprintf("read every %d seconds", frontendConst(t, filepath.Join("components", "bubbles", "useSessionOrbit.js"), "REFRESH_MS")/1000),
	})

	// session kinds: the list in the text is the list in the code
	kinds := listAfter(t, sec, "Kinds (`tasks.SessionKinds`):")
	if len(kinds) < len(tasks.SessionKinds) || !reflect.DeepEqual(kinds[:len(tasks.SessionKinds)], tasks.SessionKinds) {
		t.Errorf("the text lists kinds %v, the code has %v", kinds, tasks.SessionKinds)
	}

	// K (mission_directions): the range and the default in the text are the constants in the code, and K is operator-only
	if !strings.Contains(sec, fmt.Sprintf("K 0..%d, default %d", tasks.HardMissionDirections, tasks.DefaultMissionDirections)) {
		t.Errorf("the text must say \"K 0..%d, default %d\" (tasks.HardMissionDirections, tasks.DefaultMissionDirections)",
			tasks.HardMissionDirections, tasks.DefaultMissionDirections)
	}
	if !slices.Contains(coderhost.OperatorOnly, "mission_directions") {
		t.Errorf("mission_directions must be in coderhost.OperatorOnly")
	}

	// settings lists: operator-only is exactly the code's list; agent-adjustable is exactly the code's list
	tags := map[string]bool{}
	rt := reflect.TypeOf(def)
	for k := 0; k < rt.NumField(); k++ {
		tags[strings.Split(rt.Field(k).Tag.Get("json"), ",")[0]] = true
	}
	op := listAfter(t, sec, "Operator-only coder settings (`coderhost.OperatorOnly`):")
	inDoc := map[string]bool{}
	for _, n := range op {
		inDoc[n] = true
		if !tags[n] {
			t.Errorf("the text lists %q as an operator-only setting, which is not a setting", n)
		}
		if !slices.Contains(coderhost.OperatorOnly, n) {
			t.Errorf("the text lists %q as operator-only, which the code no longer says", n)
		}
	}
	for _, n := range coderhost.OperatorOnly {
		if !inDoc[n] {
			t.Errorf("setting %q is operator-only in the code and missing from the mission map", n)
		}
	}
	adj := listAfter(t, sec, "Agent-adjustable on descendants (`coderhost.AgentAdjustable`):")
	want := append([]string(nil), coderhost.AgentAdjustable...)
	sort.Strings(want)
	sort.Strings(adj)
	if !reflect.DeepEqual(adj, want) {
		t.Errorf("the text lists agent-adjustable settings %v, the code has %v", adj, want)
	}

	// the person verbs are the subcommands of `task session person`, and the operator-only verbs exist
	root := newRootCmd()
	person, _, _ := root.Find([]string{"task", "session", "person"})
	var verbs []string
	for _, c := range person.Commands() {
		if c.Name() != "help" {
			verbs = append(verbs, c.Name())
		}
	}
	m := regexp.MustCompile("`task session person ([a-z|-]+)`").FindStringSubmatch(sec)
	if m == nil {
		t.Fatal("the text does not list the person verbs as `task session person a|b|c`")
	}
	listed := strings.Split(m[1], "|")
	sort.Strings(verbs)
	sort.Strings(listed)
	if !reflect.DeepEqual(listed, verbs) {
		t.Errorf("the text lists person verbs %v, the command has %v", listed, verbs)
	}
	for _, path := range [][]string{
		{"task", "session", "retire-worker"}, {"coder", "sessions", "spawn-intents", "list"}, {"coder", "sessions", "spawn-intents", "resolve"},
		{"coder", "sessions", "mission-children", "enable"}, {"coder", "sessions", "mission-children", "disable"},
		{"coder", "sessions", "mission-children", "set"}, {"coder", "sessions", "mission-children", "list"},
		{"coder", "sessions", "mission-children", "resolve-charge"}, {"coder", "sessions", "budget", "grant"},
		{"coder", "sessions", "budget", "decline"}, {"coder", "sessions", "budget", "list"},
		{"agent", "purposes", "set"}, {"agent", "purposes", "reset"},
	} {
		if c, _, err := root.Find(path); err != nil || c == nil || c.Name() != path[len(path)-1] {
			t.Errorf("the text names the operator-only verb `%s`, which does not exist", strings.Join(path, " "))
		}
	}

	// the reserved config rows the text names are reserved
	for name, row := range map[string]string{"the purposes prefix": "agent_purposes.x", "the confirmation prefix": "coder-mission-confirm/x", "the children switch": "coder.mission_children_enabled"} {
		if !operator.ReservedConfig(row) {
			t.Errorf("%s (%s) is not a reserved config row any more", name, row)
		}
	}
	wantAll(t, "AGENTS.md (mission map)", sec, map[string]string{
		"the purposes prefix":         "`agent_purposes.`",
		"the confirmation prefix":     "`coder-mission-confirm/`",
		"the children switch":         "`coder.mission_children_enabled`",
		"the mission-children enable": "`coder sessions mission-children enable`",
	})
}

func TestSecurityMdMissionSummaryMatchesTheCode(t *testing.T) {
	sec := docSection(t, "SECURITY.md", "## Missions: summary of the trust boundaries")
	wantAll(t, "SECURITY.md (mission summary)", sec, map[string]string{
		"the hard maximum depth":  fmt.Sprintf("at most %d levels (`tasks.HardMaxSessionDepth`)", tasks.HardMaxSessionDepth),
		"the open-children fence": fmt.Sprintf("at most %d open children below a parent", tasks.MaxOpenChildren),
		"the live cap per tree":   fmt.Sprintf("one tree holds at most %d live sessions", coderhost.MaxLiveSessionsPerTree),
		"the giant task count":    fmt.Sprintf("over %d tasks", tasks.GiantMaxTasks),
		"the giant budget share":  fmt.Sprintf("over %d%% of the parent's budget", int(tasks.GiantBudgetShare*100)),
		"the reserved purposes":   "`agent_purposes.`",
		"the reserved switch":     "`coder.mission_children_enabled`",
		"the confirmation prefix": "`coder-mission-confirm/`",
		"the settings write path": "`Store.UpdateSettings`",
		"the operator-only list":  "`coderhost.OperatorOnly`",
		"the agent allowlist":     "`coderhost.AgentAdjustable`",
	})
	// every timestamp helper the text names is a function of timecmp.go, and the text names each one the file documents
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "coderhost", "timecmp.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"tsExpired", "tsLive", "tsLapsed", "tsInWindow", "tsOlderThan"} {
		if !regexp.MustCompile(`(?m)^func ` + h + `\(`).Match(raw) {
			t.Errorf("internal/coderhost/timecmp.go has no func %s, which the text names", h)
		}
		if !strings.Contains(sec, "`"+h+"`") {
			t.Errorf("the mission summary does not name the timestamp helper %s", h)
		}
	}
	for _, name := range []string{"MONOAGENT_SESSION_TOKEN", "MONOAGENT_TASK_BINDING", "MONOAGENT_TASK_SESSION", "MONOAGENT_TASK_ID"} {
		if !strings.Contains(sec, name) {
			t.Errorf("the mission summary does not name %s", name)
		}
	}
}

// The research/directions paragraph of AGENTS.md states the bounds of mission_evaluate, mission_reevaluate and the
// check-in digest line. 512 KiB (the argument cap, dynorg.maxEvaluateArgBytes) is unexported and has no exported
// accessor, so it is not pinned here.
func TestAgentsMdResearchDirectionsMatchesTheCode(t *testing.T) {
	sec := docSection(t, "AGENTS.md", "### Missions and the session tree: how it fits together")
	w := dynorg.DefaultWeights
	wantAll(t, "AGENTS.md (research and directions)", sec, map[string]string{
		"the candidate cap":     fmt.Sprintf("at most %d candidates (`dynorg.MaxCandidates`)", dynorg.MaxCandidates),
		"the default weights":   fmt.Sprintf("success %g, cost %g, risk %g, effort %g (`dynorg.DefaultWeights`)", w[dynorg.CritSuccess], w[dynorg.CritCost], w[dynorg.CritRisk], w[dynorg.CritEffort]),
		"the record size":       fmt.Sprintf("at most %d KiB, `tasks.MaxCommentBytes`", tasks.MaxCommentBytes>>10),
		"the current-list cap":  "most `HardMaxDirections` (#1234)",
		"the stall length":      fmt.Sprintf("no progress for %d check-ins (`coderhost.StalledCheckins`)", coderhost.StalledCheckins),
		"the budget trigger":    fmt.Sprintf("crossed %d %% of the mission budget (`coderhost.BudgetTriggerPct`)", coderhost.BudgetTriggerPct),
		"the open-set bound":    fmt.Sprintf("at most `coderhost.MaxOpenSessions` = %d", coderhost.MaxOpenSessions),
		"the transaction":       "`Store.EnqueueWithCheckin`",
		"the hard max of picks": fmt.Sprintf("K 0..%d", dynorg.HardMaxDirections),
	})
	if dynorg.HardMaxDirections != tasks.HardMissionDirections || dynorg.DefaultDirections != tasks.DefaultMissionDirections {
		t.Errorf("dynorg and tasks disagree on K (%d/%d against %d/%d)", dynorg.DefaultDirections, dynorg.HardMaxDirections,
			tasks.DefaultMissionDirections, tasks.HardMissionDirections)
	}
}

// The command reference shows how the operator sets K: the example is the one place a reader looks for the syntax
// (a number 0 to 6; null resets it), so it must stay in the settings set entry.
func TestSettingsSetEntryShowsMissionDirections(t *testing.T) {
	for _, c := range catalog.CommandDocs {
		if c.Name != "coder sessions settings set" {
			continue
		}
		for _, ex := range c.Examples {
			if strings.Contains(ex, `"mission_directions"`) {
				return
			}
		}
		t.Fatalf("the %q entry has no example for mission_directions (K)", c.Name)
	}
	t.Fatal("no catalog entry named coder sessions settings set")
}
