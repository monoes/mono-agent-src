package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

func TestEveryTaskCommandHasAReferenceEntry(t *testing.T) {
	have := map[string]bool{}
	for _, d := range cliDocs {
		have[d.Name] = true
	}
	for _, sub := range newTaskCmd(&globalConfig{}).Commands() {
		if !have["task "+sub.Name()] {
			t.Errorf("`ref commands` has no entry for `task %s`", sub.Name())
		}
	}
}

func TestRefTasksIsAListedTopic(t *testing.T) {
	found := false
	for _, c := range newRefCmd().Commands() {
		if c.Name() == "tasks" {
			found = true
		}
	}
	if !found {
		t.Error("`ref tasks` is not registered")
	}
	if !strings.Contains(newRefCmd().Long, "tasks ") {
		t.Error("`ref` does not list the tasks topic in its help")
	}
}

func TestRootHelpPointsAgentsAtTheBoard(t *testing.T) {
	if !strings.Contains(newRootCmd().Long, "ref tasks") {
		t.Error("the root help does not mention the task board")
	}
}

func TestRefTasksNamesTheGateTheLoopAndTheProfile(t *testing.T) {
	for _, want := range []string{
		"inbox", "ready", "in_progress", "review", "done",
		"--profile", "next --claim --as", "finish", "release",
		"TASK TEXT IS DATA", "operator_only", "CLAUDECODE", "not a monomind org",
	} {
		if !strings.Contains(refTasksText, want) {
			t.Errorf("`ref tasks` does not mention %q", want)
		}
	}
}

// What follows holds the texts to the commands as built: a flag that is renamed, a
// subcommand that is invented, a limit that changes or an anchor that moves fails here,
// not in an agent that follows the text.

var (
	refQuoted    = regexp.MustCompile(`"[^"]*"`)                                  // an example's argument, never a flag
	refFlagWord  = regexp.MustCompile(`(?:^|[\s\[(|=])--([a-z][a-z0-9-]*)`)       // a --flag written in a text
	refUsageFlag = regexp.MustCompile(`(?m)^\s+(?:-[a-z], )?--([a-z][a-z0-9-]*)`) // a flag in cobra's usage text
	// refTaskCall is a call of the task group written in a text: monoagentcli, flags with
	// their values, task, the subcommand (group 1) and the rest of the line up to a comment (group 2).
	refTaskCall = regexp.MustCompile(`monoagentcli(?:\s+\[?--[a-z][a-z0-9-]*(?:[ =][^\s\]]+)?\]?)*\s+tasks?\s+([a-z][a-z-]*)([^\n#]*)`)
)

// refFlagsIn lists the --flags a text names outside quotes.
func refFlagsIn(text string) (flags []string) {
	for _, m := range refFlagWord.FindAllStringSubmatch(refQuoted.ReplaceAllString(text, ""), -1) {
		flags = append(flags, m[1])
	}
	return flags
}

// refTaskEntries are the `ref commands` entries of the task group, by subcommand.
func refTaskEntries() map[string]cmdDoc {
	entries := map[string]cmdDoc{}
	for _, d := range cliDocs {
		if name, ok := strings.CutPrefix(d.Name, "task "); ok {
			entries[name] = d
		}
	}
	return entries
}

func TestRefTasksEntriesDescribeTheirOwnCommand(t *testing.T) {
	for name, d := range refTaskEntries() {
		if refTaskSub(name) == nil {
			t.Errorf("`ref commands` has an entry for `task %s`, which is no command", name)
		}
		if d.Short == "" || len(d.Examples) == 0 {
			t.Errorf("`task %s` needs a one-line description and an example", name)
		}
		if !strings.HasPrefix(d.Usage, "monoagentcli ") || !strings.Contains(d.Usage, " task "+name) {
			t.Errorf("the usage of `task %s` is %q: it does not call that command", name, d.Usage)
		}
		for _, ex := range d.Examples {
			calls := refTaskCall.FindAllStringSubmatch(ex, -1)
			if len(calls) != 1 {
				t.Errorf("the example %q of `task %s` is not one call of the task group", ex, name)
				continue
			}
			// A group's examples call its subcommands (task os install), and no other entry's do.
			if path, _, _ := refTaskCallee(calls[0]); path != name && !strings.HasPrefix(path, name+" ") {
				t.Errorf("the example %q of `task %s` is not a call of that command", ex, name)
			}
		}
	}
}

func TestRefTasksEntriesUseOnlyFlagsTheCommandHas(t *testing.T) {
	for name, d := range refTaskEntries() {
		sub := refTaskSub(name)
		if sub == nil {
			continue
		}
		for _, text := range append([]string{d.Usage, d.Flags}, d.Examples...) {
			for _, flag := range refFlagsIn(text) {
				if !refHasFlag(sub, flag) {
					t.Errorf("`ref commands` shows `task %s` with --%s, which it does not have", name, flag)
				}
			}
		}
	}
}

func TestRefTasksEntriesNameEveryFlagOfTheirCommand(t *testing.T) {
	for path, sub := range refTaskCommands() {
		d := refTaskEntries()[path] // no entry at all is TestEveryTaskCommandHasAReferenceEntry's to say
		documented := append(refFlagsIn(d.Usage), refFlagsIn(d.Flags)...)
		for _, m := range refUsageFlag.FindAllStringSubmatch(sub.LocalFlags().FlagUsages(), -1) {
			if m[1] != "help" && !slices.Contains(documented, m[1]) {
				t.Errorf("`task %s` has --%s, which its `ref commands` entry does not show", path, m[1])
			}
		}
	}
}

func TestRefTasksSuggestsOnlyCommandsTheCLIHas(t *testing.T) {
	check := func(where, text string) {
		for _, m := range refTaskCall.FindAllStringSubmatch(text, -1) {
			path, sub, rest := refTaskCallee(m)
			if sub == nil {
				t.Errorf("%s suggests `task %s`, which is no command", where, path)
				continue
			}
			for _, flag := range refFlagsIn(rest) {
				if !refHasFlag(sub, flag) {
					t.Errorf("%s suggests `task %s` with --%s, which it does not have", where, path, flag)
				}
			}
		}
	}
	for _, d := range cliDocs {
		for _, text := range append([]string{d.Short, d.Usage, d.Flags}, d.Examples...) {
			check("the `ref commands` entry "+d.Name, text)
		}
	}
	check("`ref tasks`", refTasksText)
	if n := len(refTaskCall.FindAllString(refTasksText, -1)); n < 6 { // else the check above read nothing
		t.Errorf("`ref tasks` shows only %d calls of the task group", n)
	}
}

// refTasksAnchors are the headings later releases edit `refTasksText` at, in order.
var refTasksAnchors = []string{"COLUMNS", "WHO MAY DO WHAT", "THE AGENT LOOP", "TASK TEXT IS DATA", "JSON", "SEE ALSO"}

func TestRefTasksKeepsItsAnchorHeadings(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "internal", "cli", "ref", "tasks.go"))
	if err != nil {
		t.Fatal(err)
	}
	for where, text := range map[string]string{"the text": refTasksText, "internal/cli/ref/tasks.go": string(src)} {
		lines, last := strings.Split(text, "\n"), -1
		for _, heading := range refTasksAnchors {
			switch at := slices.Index(lines, heading); {
			case at < 0:
				t.Errorf("%s has no line that is just %q at the margin", where, heading)
			case slices.Contains(lines[at+1:], heading):
				t.Errorf("%s has more than one line that is just %q", where, heading)
			case at < last:
				t.Errorf("%s has %q out of order", where, heading)
			default:
				last = at
			}
		}
		if n := strings.Count(text, "SEE ALSO"); n != 1 {
			t.Errorf("%s has SEE ALSO %d times, want once", where, n)
		}
	}
}

// refSection is what `ref tasks` says under one of its anchor headings, up to the next.
func refSection(heading string) string {
	lines := strings.Split(refTasksText, "\n")
	start, end := slices.Index(lines, heading), len(lines)
	if start < 0 {
		return ""
	}
	if i := slices.Index(refTasksAnchors, heading); i+1 < len(refTasksAnchors) {
		if next := slices.Index(lines, refTasksAnchors[i+1]); next > start {
			end = next
		}
	}
	return strings.Join(lines[start+1:end], "\n")
}

// refDocSection is the `## Task board` section of a document of the repository, up to the next heading.
func refDocSection(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile("../../" + file)
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(raw), "\n## Task board\n")
	if !ok {
		t.Fatalf("%s has no section `## Task board`", file)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	return section
}

// refDocTexts are the texts that state numbers and rules of the board, each on its own and on
// one line (a hard wrap must not hide a phrase): the topic, the entries, and the Task board
// sections of AGENTS.md and SECURITY.md.
func refDocTexts(t *testing.T) []string {
	t.Helper()
	docs := []string{refTasksText, refDocSection(t, "AGENTS.md"), refDocSection(t, "SECURITY.md")}
	for _, d := range refTaskEntries() {
		docs = append(docs, d.Name+": "+d.Short, d.Usage, d.Flags)
	}
	for i, text := range docs {
		docs[i] = strings.Join(strings.Fields(text), " ")
	}
	return docs
}

// refStates holds every statement of one number in the texts to the code: the pattern
// has one group, the number as written. A number no text states fails too.
func refStates(t *testing.T, what, pattern, want string) {
	t.Helper()
	found := regexp.MustCompile(pattern).FindAllStringSubmatch(strings.Join(refDocTexts(t), "\n"), -1)
	if len(found) == 0 {
		t.Errorf("no text states %s (%s), which is %s in the code", what, pattern, want)
	}
	for _, m := range found {
		if m[1] != want {
			t.Errorf("a text says %q for %s, which is %s in the code", m[0], what, want)
		}
	}
}

// refNames says whether a text names a command or a flag as a word: "unarchive" does not name
// "archive", "task_list" does not name "list".
func refNames(text, word string) bool {
	return regexp.MustCompile(`(?:^|[^\w-])` + regexp.QuoteMeta(word) + `(?:[^\w-]|$)`).MatchString(text)
}

// thousands writes n (at least 1,000) the way the texts do: 2,000.
func thousands(n int) string { return fmt.Sprintf("%d,%03d", n/1000, n%1000) }

func TestRefTasksStatesTheLimitsAndDefaultsTheCodeHas(t *testing.T) {
	refStates(t, "a profile's open tasks", `([\d,]+) open tasks`, thousands(tasks.MaxOpenTasks))
	refStates(t, "a title", `titles? (?:is |are )?\(?(\d+) characters`, fmt.Sprint(tasks.MaxTitleRunes))
	refStates(t, "notes", `notes (?:are )?\(?(\d+) KiB`, fmt.Sprint(tasks.MaxNotesBytes>>10))
	refStates(t, "a comment", `(?:note|comments) \(?(\d+) KiB`, fmt.Sprint(tasks.MaxCommentBytes>>10))
	refStates(t, "the events that stop comments", `(\d+) events takes no more comments`, fmt.Sprint(tasks.MaxEventsPerTask))
	refStates(t, "the events that stop claims", `([\d,]+) events no more claims`, thousands(tasks.MaxEventsToClaim))
	refStates(t, "a lease", `(\d+) minutes`, fmt.Sprint(int(tasks.DefaultLease.Minutes())))
	refStates(t, "the longest lease", `(\d+) hours`, fmt.Sprint(int(tasks.MaxLease.Hours())))
	refStates(t, "what --stdin reads", `at most (\d+) MiB`, fmt.Sprint(maxStdinBytes>>20))
	// The defaults the flags declare: a flag that is renamed or re-defaulted shows up here.
	defaultOf := func(command, flag string) string {
		sub := refTaskSub(command)
		if sub == nil || sub.Flags().Lookup(flag) == nil {
			t.Fatalf("`task %s` has no --%s", command, flag)
		}
		return sub.Flags().Lookup(flag).DefValue
	}
	lease := fmt.Sprintf("default %dm, at most %dh", int(tasks.DefaultLease.Minutes()), int(tasks.MaxLease.Hours()))
	for _, c := range []struct{ entry, want string }{
		{"list", fmt.Sprintf("default %d, at most %s", tasks.DefaultListLimit, thousands(tasks.MaxListLimit))},
		{"board", "default " + defaultOf("board", "done-limit")},
		{"add", defaultOf("add", "source") + " (default)"},
		{"next", lease},
		{"claim", lease},
	} {
		if !strings.Contains(refTaskEntries()[c.entry].Flags, c.want) {
			t.Errorf("the `ref commands` entry of `task %s` does not say %q", c.entry, c.want)
		}
	}
	// A comment does not add the lease that was asked for: it extends the claim to 30 minutes from
	// the comment, if that is later, and never shortens it, so with a long --lease it changes nothing
	// until fewer than 30 minutes remain. The texts that explain the lease say so, and no sentence says
	// that a comment renews it or that one should comment before its last 30 minutes.
	minutes := int(tasks.DefaultLease.Minutes())
	extends, fewer := fmt.Sprintf("extends it to %d minutes from the comment", minutes), fmt.Sprintf("fewer than %d minutes", minutes)
	for where, text := range map[string]string{"THE AGENT LOOP of `ref tasks`": refSection("THE AGENT LOOP"), "AGENTS.md": refDocSection(t, "AGENTS.md")} {
		for _, want := range []string{extends, "never shortens it", fewer} {
			if !strings.Contains(strings.Join(strings.Fields(text), " "), want) {
				t.Errorf("%s does not say %q", where, want)
			}
		}
	}
	for _, text := range refDocTexts(t) {
		for _, s := range regexp.MustCompile(`\.\s+`).Split(text, -1) {
			if regexp.MustCompile(`(?i)\brenew|before its last`).MatchString(s) && regexp.MustCompile(`(?i)\bcomments?\b`).MatchString(s) {
				t.Errorf("a text gives a wrong account of what a comment does to a lease (it extends the claim to %d minutes from the comment, if that is later: it adds nothing while more than that remains, and it does not renew): %q", minutes, s)
			}
		}
	}
}

func TestRefTasksNamesEveryErrorCodeTheCLIAnswers(t *testing.T) {
	jsonText := refSection("JSON")
	for _, c := range []struct {
		err  error
		exit int
	}{
		{tasks.ErrNotFound, 2}, {tasks.ErrInvalid, 3}, {tasks.ErrOperatorOnly, 3}, {tasks.ErrNotReady, 3},
		{&tasks.ClaimedError{By: "someone", Until: time.Now()}, 3}, {tasks.ErrNotClaimant, 3}, {tasks.ErrLimit, 3},
	} {
		err := taskErr(fmt.Errorf("%w: a refusal", c.err))
		fields, ok := err.(jsonErrorFields)
		if !ok {
			t.Fatalf("the CLI gives %v no code", c.err)
		}
		code, _ := fields.JSONErrorFields()["code"].(string)
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(code) + `\b`).MatchString(jsonText) {
			t.Errorf("the JSON section of `ref tasks` does not name the error code %q", code)
		}
		if got := exitCodeFor(err); got != c.exit {
			t.Errorf("%q is exit %d, but the test (and the text) say %d", code, got, c.exit)
		}
	}
	for _, want := range []string{"not_found (exit 2)", "limit (exit 3)"} {
		if !strings.Contains(jsonText, want) {
			t.Errorf("the JSON section of `ref tasks` does not say %q", want)
		}
	}
}

func TestRefTasksStatesTheNameRulesOfTheStore(t *testing.T) {
	refStates(t, "the length of an agent's name", `1 to (\d+) characters of letters`, fmt.Sprint(tasks.MaxNameLen))
	for _, want := range []string{"characters of letters, digits and ._#@:-", "the labels you, agent, capture, chrome and os are reserved"} {
		if !strings.Contains(refSection("WHO MAY DO WHAT"), want) {
			t.Errorf("the WHO MAY DO WHAT section of `ref tasks` does not say %q", want)
		}
	}
	for _, label := range []string{"you", "agent", "capture", "chrome", "os"} {
		if tasks.CheckAgentName(label) == nil || tasks.CheckAgentName(strings.ToUpper(label)) == nil {
			t.Errorf("the text calls %q reserved, in any case, but the store takes it as a name", label)
		}
	}
	for _, c := range "._#@:-" {
		if err := tasks.CheckAgentName("a" + string(c)); err != nil {
			t.Errorf("the text says %q is a name character, but the store refuses it: %v", c, err)
		}
	}
	if long := strings.Repeat("a", tasks.MaxNameLen); tasks.CheckAgentName(long) != nil || tasks.CheckAgentName(long+"a") == nil {
		t.Errorf("the text says a name is up to %d characters: the store disagrees", tasks.MaxNameLen)
	}
}

func TestRefTasksJSONSectionShowsTheDocumentsTheCommandsPrint(t *testing.T) {
	jsonText := refSection("JSON")
	db := newTaskTestDB(t)
	// Each step is a call, the document the text says it prints and the word the text names the
	// call by. The steps are in order, so that every call has a task to work on: it is added,
	// approved, claimed, finished, moved back, claimed again, released, archived and restored.
	for _, step := range []struct {
		doc, word string
		args      []string
	}{
		{`{"profile","created","task"}`, "add", []string{"add", "write the docs"}},
		{`{"profile","tasks"}`, "approve", []string{"approve", "1"}},
		{`{"profile","task"}`, "next", []string{"next", "--as", "bot"}},
		{`{"profile","task"}`, "next", []string{"next", "--claim", "--as", "bot"}},
		{`{"profile","task"}`, "comment", []string{"comment", "1", "halfway", "--as", "bot"}},
		{`{"profile","task","events"}`, "show", []string{"show", "1"}},
		{`{"profile","tasks"}`, "list", []string{"list"}},
		{`{"profile","rev","counts","tasks"}`, "board", []string{"board"}},
		{`{"profile","task"}`, "finish", []string{"finish", "1", "--result", "done", "--as", "bot"}},
		{`{"profile","task"}`, "move", []string{"move", "1", "ready"}},
		{`{"profile","task"}`, "claim", []string{"claim", "1", "--as", "bot"}},
		{`{"profile","task"}`, "release", []string{"release", "1", "--as", "bot"}},
		{`{"profile","task"}`, "edit", []string{"edit", "1", "--title", "write the docs well"}},
		{`{"profile","tasks"}`, "archive", []string{"archive", "1"}},
		{`{"profile","tasks"}`, "unarchive", []string{"unarchive", "1"}},
		{`{"profile","archived"}`, "archive --status", []string{"archive", "--status", "ready"}},
		{`{"profile","ready","next"}`, "digest", []string{"digest"}},
	} {
		call := "task " + strings.Join(step.args, " ")
		out, _, err := runTask(t, db, "default", true, "", step.args...)
		var doc map[string]json.RawMessage
		if err != nil || json.Unmarshal([]byte(out), &doc) != nil {
			t.Fatalf("%s: %v\n%s", call, err, out)
		}
		var got, want []string
		for k := range doc {
			got = append(got, k)
		}
		for _, m := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(step.doc, -1) {
			want = append(want, m[1])
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s prints a document with the keys %v, but the test (and `ref tasks`) say %s", call, got, step.doc)
		}
		stated := false
		for _, line := range strings.Split(jsonText, "\n") {
			stated = stated || strings.Contains(line, step.doc) && refNames(line, step.word)
		}
		if !stated {
			t.Errorf("the JSON section of `ref tasks` has no line that gives %s for %q", step.doc, step.word)
		}
	}
	// Nothing is ready now: the document says so with null, as the text does.
	out, _, _ := runTask(t, db, "default", true, "", "next", "--as", "bot")
	if !regexp.MustCompile(`"task":\s*null`).MatchString(out) || !strings.Contains(jsonText, "task null when nothing is ready") {
		t.Errorf("`task next` with nothing ready prints %s, and the JSON section must say `task null when nothing is ready`", out)
	}
}

func TestRefTasksGivesNoWayRoundTheGate(t *testing.T) {
	if !strings.Contains(refTasksText, "ask the person") {
		t.Error("`ref tasks` does not tell an agent that is refused to ask the person")
	}
	for _, advice := range []string{"unset", "bypass"} {
		if strings.Contains(strings.ToLower(refTasksText), advice) {
			t.Errorf("`ref tasks` mentions %q: it must not hint at a way round the gate", advice)
		}
	}
}

func TestRefTasksIsPrintedAndListed(t *testing.T) {
	run := func(args ...string) string {
		return captureStdout(t, func() {
			ref := newRefCmd()
			ref.SetArgs(append([]string{}, args...)) // never nil: cobra would read the test binary's own flags
			if err := ref.Execute(); err != nil {
				t.Fatalf("ref %v: %v", args, err)
			}
		})
	}
	if printed := run("tasks"); printed != refTasksText {
		t.Errorf("`ref tasks` prints %d bytes that are not its text (%d bytes)", len(printed), len(refTasksText))
	}
	if listing := run(); !regexp.MustCompile(`(?m)^  tasks\s+\S`).MatchString(listing) {
		t.Errorf("`ref` does not list the tasks topic:\n%s", listing)
	}
}

func TestRefTasksIsInAGENTSMDsTopicTableAndMONOAGENTACTORInItsVariableTable(t *testing.T) {
	raw, err := os.ReadFile("../../AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{"| `ref tasks` |", "| `MONOAGENT_ACTOR` |"} {
		if !strings.Contains(string(raw), row) {
			t.Errorf("AGENTS.md has no table row that starts %s", row)
		}
	}
}

func TestRootHelpPointsAgentsAtTheBoardInEveryLocale(t *testing.T) {
	for _, lang := range []string{"en", "es"} {
		raw, err := os.ReadFile("../../internal/i18n/locales/" + lang + ".json")
		var locale map[string]string
		if err != nil || json.Unmarshal(raw, &locale) != nil {
			t.Fatalf("%s.json: %v", lang, err)
		}
		if !strings.Contains(locale["root.long"], "monoagentcli ref tasks") {
			t.Errorf("the %s root help does not point agents at `monoagentcli ref tasks`", lang)
		}
	}
}
