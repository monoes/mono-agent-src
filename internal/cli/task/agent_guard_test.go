package task

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/tasks"
)

// badAgentNames are names the store refuses an agent: a label it writes for someone else, in any
// case (the operator's, an unnamed agent's, a capture's), and names outside its alphabet or length.
var badAgentNames = []string{
	"you", "YOU", "Agent", "capture", "Capture", "Chrome", "OS", "os",
	"has space", "x;y", "$(id)", "a/b", "caf\U000000e9", "a\x00b", "a\x1bb", "a\U0000202eb", "a\U000e0041b",
	strings.Repeat("a", 65),
}

// The store refuses a name an agent may not act under, but only once the database is open. An agent
// command judges the name first, the way the operator's commands refuse an agent first: a database
// that cannot be opened is the control (a good name gets as far as it, and fails with the plain
// error 1), a bad name is the refusal 3, says which kind of bad it is, and says how to choose another.
func TestTaskAgentVerbsRefuseABadNameBeforeTheDatabaseIsOpened(t *testing.T) {
	t.Parallel()
	newTaskTestDB(t) // the operator's environment
	db := opsBrokenDB(t)
	for _, verb := range agentVerbsFor("1") {
		if _, _, err := runTask(t, db, "default", true, "", append(slices.Clone(verb), "--as", "bot")...); exitCode(err) != 1 {
			t.Fatalf("the control, task %s with a good name on a database that cannot be opened: exit %d (%v), want the plain error 1",
				strings.Join(verb, " "), exitCode(err), err)
		}
	}
	reserved := []string{"you", "agent", "capture", "chrome", "os"}
	for _, verb := range agentVerbsFor("1") {
		for _, name := range badAgentNames {
			args := append(slices.Clone(verb), "--as", name)
			want := "characters"
			if slices.ContainsFunc(reserved, func(r string) bool { return strings.EqualFold(name, r) }) {
				want = "reserved"
			}
			exit, code, msg := agentRefusal(t, db, args...)
			if exit != 3 || code != "invalid_input" || !strings.Contains(msg, want) || !strings.Contains(msg, "--as NAME") || len(msg) > 300 {
				t.Errorf("task %s --as %.20q: exit %d, %q, %.150q; want exit 3, invalid_input, the word %q and the way to choose a name (--as NAME)", strings.Join(verb, " "), name, exit, code, msg, want)
			}
		}
	}
}

// An agent that has no name at all, or none that it said, is refused before the database is opened
// too: whatever the sign that makes it an agent, and whether the name came from the flag or from the
// environment. The operator's own comment is the one agent verb it may run, so it is not in the first row.
func TestTaskAgentVerbsRefuseAnAgentWithoutAUsableNameBeforeTheDatabaseIsOpened(t *testing.T) {
	t.Parallel()
	newTaskTestDB(t)
	db := opsBrokenDB(t)
	env := func(name, value string) func(t *testing.T) { return func(t *testing.T) { setenv(t, name, value) } }
	for _, c := range []struct {
		name        string
		setup       func(t *testing.T)
		extra       []string
		want        string // the words the refusal must hold
		operatorsOK bool   // the operator may run this verb: it is not refused when nothing makes the caller an agent
	}{
		{"nothing says an agent runs it", nil, nil, "for AI agents", true},
		{"a blank --as", nil, []string{"--as", ""}, "--as needs a name", false},
		{"a --as of spaces", nil, []string{"--as", "   "}, "--as needs a name", false},
		{"--as=", nil, []string{"--as="}, "--as needs a name", false},
		{"a marker and no name", env("CLAUDECODE", "1"), nil, "CLAUDECODE", false},
		{"a marker and a blank --as", env("CLAUDECODE", "1"), []string{"--as", ""}, "--as needs a name", false},
		{"a marker and a reserved name", env("CLAUDECODE", "1"), []string{"--as", "you"}, "reserved", false},
		{"a reserved name in MONOAGENT_ACTOR", env("MONOAGENT_ACTOR", "Agent"), nil, "reserved", false},
		{"a name with a space in MONOAGENT_ACTOR", env("MONOAGENT_ACTOR", "bot 1"), nil, "characters", false},
		{"a name of 65 characters in MONOAGENT_ACTOR", env("MONOAGENT_ACTOR", strings.Repeat("a", 65)), nil, "characters", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.setup != nil {
				c.setup(t)
			}
			for _, verb := range agentVerbsFor("1") {
				if c.operatorsOK && verb[0] == "comment" {
					continue
				}
				args := append(slices.Clone(verb), c.extra...)
				exit, code, msg := agentRefusal(t, db, args...)
				if exit != 3 || code != "invalid_input" || !strings.Contains(msg, c.want) || !strings.Contains(msg, "--as NAME") {
					t.Errorf("task %s: exit %d, %q, %.200q; want exit 3, invalid_input, the words %q and the way to choose a name (--as NAME)", strings.Join(args, " "), exit, code, msg, c.want)
				}
			}
		})
	}
}

// What is refused for its name changes nothing: not the board, not a claim, not a history.
func TestTaskAgentVerbsRefusedForTheirNameWriteNothing(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	opsAdd(t, db, "waiting", "--ready")
	held := opsAdd(t, db, "held", "--ready")
	agentRun(t, db, "claim", id(held), "--as", "bot")
	before := agentSnapshot(t, db)
	for _, name := range append([]string{"", "   "}, badAgentNames...) {
		for _, verb := range agentVerbsFor(id(held)) {
			args := append(slices.Clone(verb), "--as", name)
			if exit, _, _ := agentRefusal(t, db, args...); exit != 3 {
				t.Errorf("task %s --as %.20q: exit %d, want the refusal 3", strings.Join(verb, " "), name, exit)
			}
		}
	}
	if after := agentSnapshot(t, db); after != before {
		t.Errorf("refused commands changed the board:\nbefore\n%s\nafter\n%s", before, after)
	}
}

// The store judges a name by tasks.CheckAgentName, and the commands judge it before the database is
// opened by asking the same function: there is one rule, and the commands add only the way out. So the
// table is of what that function lets through and refuses, and of what a command says of a refusal:
// exit 3, the store's own words (not a paraphrase of them), and how to choose another name.
func TestTaskAgentNameIsJudgedByTheStoresRuleAndSaysHowToChooseAnother(t *testing.T) {
	t.Parallel()
	names := append([]string{
		"", " ", "bot", "bot-1", "claude-code#a3f9", "agent:claude-code#a3f9", "a.b_c@d:e-f", "#x", "-", ".", "_", "7",
		"agents", "youth", "you2", "myos", "os2", "chromium", "captured", "A", "Z9",
		"you", "You", "YOU", "yOu", "agent", "AGENT", "capture", "CaPtUrE", "chrome", "Chrome", "os", "OS", "Os",
		strings.Repeat("a", 64), strings.Repeat("a", 65), strings.Repeat("a", 100000), strings.Repeat("\U000000e9", 3),
		"bot\n", "\tbot", " you", "you ", "bot 1", "a;b", "a|b", "a&b", "a'b", "a\"b", "a`b", "a$b", "a(b)", "a*b", "a~b", "a!b", "a\\b", "a/b",
		"a\x00b", "a\x1bb", "a\U0000202eb", "a\U0000feffb", "a\U000e0041b", "a\xffb",
		"y\U0000043eu", "you\U0000200b", "\U0000212aey", "agent\U00000301",
	}, badAgentNames...)
	refused := 0
	for _, name := range names {
		storeErr := tasks.CheckAgentName(name)
		cliErr := td.agentNameError(name)
		if (storeErr != nil) != (cliErr != nil) {
			t.Errorf("the name %.40q: the store's rule says %v, the command says %v", name, storeErr, cliErr)
			continue
		}
		if cliErr == nil {
			continue
		}
		refused++
		msg := cliErr.Error()
		if exitCode(cliErr) != 3 || !strings.Contains(msg, storeErr.Error()) || !strings.Contains(msg, "choose another name with --as NAME") || len(msg) > 300 || !utf8.ValidString(msg) {
			t.Errorf("the refusal of the name %.40q: exit %d, %.200q; want exit 3, the store's words %q and the way out", name, exitCode(cliErr), msg, storeErr)
		}
	}
	if refused < 40 || refused == len(names) {
		t.Errorf("%d of %d names were refused: the table must hold names of both kinds", refused, len(names))
	}
}

// A mistake in the arguments is exit 3 with the JSON error document, found before the database is
// opened: cobra's own check would be exit 1 with no document, and the database would be opened by
// a call that cannot be answered. The name is judged first, as the operator's commands judge the
// caller first: the same mistakes without a name are the refusal of the name.
func TestTaskAgentCommandsRefuseTheWrongArgumentsBeforeTheDatabaseIsOpened(t *testing.T) {
	t.Parallel()
	newTaskTestDB(t)
	db := opsBrokenDB(t)
	for _, c := range []struct {
		args   []string
		phrase string
		peek   bool // the operator may run it, so with no name it is not the name that is refused: next without --claim, comment
	}{
		{[]string{"next", "extra"}, "takes no arguments", true},
		{[]string{"next", "--claim", "extra"}, "takes no arguments", false},
		{[]string{"claim"}, "takes one task id", false},
		{[]string{"claim", "1", "2"}, "takes one task id", false},
		{[]string{"claim", "x"}, "is not a task id", false},
		{[]string{"claim", "0"}, "is not a task id", false},
		{[]string{"claim", "#"}, "is not a task id", false},
		{[]string{"comment"}, "takes a task id and the text", true},
		{[]string{"comment", "1"}, "takes a task id and the text", true},
		{[]string{"comment", "x", "some text"}, "is not a task id", true},
		{[]string{"finish"}, "takes one task id", false},
		{[]string{"finish", "1", "2", "--result", "r"}, "takes one task id", false},
		{[]string{"finish", "x", "--result", "r"}, "is not a task id", false},
		{[]string{"finish", "3x", "--result", "r"}, "is not a task id", false},
		{[]string{"finish", "1"}, "exactly one of --result", false},
		{[]string{"finish", "1", "--result", "r", "--question", "q"}, "exactly one of --result", false},
		{[]string{"finish", "1", "--result", "", "--question", "q"}, "exactly one of --result", false},
		{[]string{"finish", "1", "--result", "", "--question", ""}, "exactly one of --result", false},
		{[]string{"release"}, "takes one task id", false},
		{[]string{"release", "1", "2"}, "takes one task id", false},
		{[]string{"release", "0"}, "is not a task id", false},
	} {
		named := append(slices.Clone(c.args), "--as", "bot")
		exit, code, msg := agentRefusal(t, db, named...)
		if exit != 3 || code != "invalid_input" || !strings.Contains(msg, c.phrase) {
			t.Errorf("task %s: exit %d, %q, %.200q; want exit 3, invalid_input and the words %q", strings.Join(named, " "), exit, code, msg, c.phrase)
		}
		if c.peek {
			continue
		}
		exit, code, msg = agentRefusal(t, db, c.args...)
		if exit != 3 || code != "invalid_input" || !strings.Contains(msg, "--as") || strings.Contains(msg, c.phrase) {
			t.Errorf("task %s with no name: exit %d, %q, %.200q; want the refusal of the name, not of the arguments", strings.Join(c.args, " "), exit, code, msg)
		}
	}
	// The control: with one of --result and --question and a name, the call gets as far as the database.
	if _, _, err := runTask(t, db, "default", true, "", "finish", "1", "--as", "bot", "--result", "r"); exitCode(err) != 1 {
		t.Errorf("the control, a finish that is well formed on a database that cannot be opened: exit %d (%v), want 1", exitCode(err), err)
	}
}

// What the caller sent is repeated in a refusal only in part: a huge argument or a huge name must not
// make a huge message, and the cut never splits a character (the second value is 2-byte characters).
func TestTaskAgentCommandsRepeatOnlyAShortStretchOfWhatTheyRefuse(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, huge := range []string{strings.Repeat("x", 10000), strings.Repeat("\xc3\xa9", 10000)} {
		for _, args := range [][]string{
			{"next", huge},
			{"claim", huge, "--as", "bot"},
			{"comment", huge, "text", "--as", "bot"},
			{"finish", huge, "--result", "r", "--as", "bot"},
			{"release", huge, "--as", "bot"},
			{"next", "--claim", "--as", huge},
			{"claim", "1", "--as", huge},
			{"comment", "1", "text", "--as", huge},
			{"finish", "1", "--result", "r", "--as", huge},
			{"release", "1", "--as", huge},
		} {
			out, _, err := runTask(t, db, "default", true, "", args...)
			var doc map[string]any
			if exitCode(err) != 3 || json.Unmarshal([]byte(out), &doc) != nil {
				t.Fatalf("task %s <%d bytes>: exit %d (%v), %.80q", args[0], len(huge), exitCode(err), err, out)
			}
			msg, _ := doc["error"].(string)
			if doc["code"] != "invalid_input" || len(msg) == 0 || len(err.Error()) >= 300 || len(out) > 400 || !utf8.ValidString(msg) {
				t.Errorf("task %s %.20q: a refusal of %d bytes (document %d), code %v: %.120q", args[0], args[1], len(err.Error()), len(out), doc["code"], msg)
			}
		}
	}
}

// The agent's verbs are for agents: the operator, which has no name to give, is told to choose one.
// It may look (next, digest) and comment, and nothing else.
func TestTaskTheAgentVerbsAreForAgents(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	n := opsAdd(t, db, "go", "--ready")
	before := agentSnapshot(t, db)
	for _, verb := range agentVerbsFor(id(n)) {
		if verb[0] == "comment" {
			continue
		}
		exit, code, msg := agentRefusal(t, db, verb...)
		if exit != 3 || code != "invalid_input" || !strings.Contains(msg, "for AI agents") || !strings.Contains(msg, "--as NAME") {
			t.Errorf("task %s as the operator: exit %d, %q, %.200q", strings.Join(verb, " "), exit, code, msg)
		}
	}
	if after := agentSnapshot(t, db); after != before {
		t.Errorf("a refusal changed the board:\n%s\n%s", before, after)
	}
	for _, args := range [][]string{{"next"}, {"digest"}, {"comment", id(n), "a note from the operator"}} {
		if _, _, err := runTask(t, db, "default", true, "", args...); err != nil {
			t.Errorf("the operator's task %s: %v", strings.Join(args, " "), err)
		}
	}
}

// An agent never comments as the operator. A comment is the operator's only when nothing at all
// says that an agent runs it: not a marker (every one the org-signing guard knows), not --as (blank
// or not), not MONOAGENT_ACTOR. Otherwise the comment is an agent's, which needs a name and a
// claim, and what an agent writes never reads as "you" in the history.
func TestTaskCommentOfAnAgentIsNeverTheOperators(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	n := opsAdd(t, db, "a task")
	before := agentSnapshot(t, db)
	for _, marker := range orgsign.AgentContextMarkers() {
		t.Run(marker, func(t *testing.T) {
			setenv(t, marker, "1")
			exit, code, msg := agentRefusal(t, db, "comment", id(n), "I am the operator")
			if exit != 3 || code != "invalid_input" || !strings.Contains(msg, marker) || !strings.Contains(msg, "--as NAME") {
				t.Errorf("a comment under %s: exit %d, %q, %.200q; want the refusal that says who is running it", marker, exit, code, msg)
			}
		})
	}
	for _, blank := range [][]string{{"--as", ""}, {"--as", "   "}, {"--as="}} {
		args := append([]string{"comment", id(n), "I am the operator"}, blank...)
		if exit, code, msg := agentRefusal(t, db, args...); exit != 3 || code != "invalid_input" || !strings.Contains(msg, "--as needs a name") {
			t.Errorf("task %s: exit %d, %q, %.200q", strings.Join(args, " "), exit, code, msg)
		}
	}
	if exit, code, _ := agentRefusal(t, db, "comment", id(n), "from an agent", "--as", "bot"); exit != 3 || code != "not_claimant" {
		t.Errorf("a named agent on a task it does not hold: exit %d, %q", exit, code)
	}
	setenv(t, "MONOAGENT_ACTOR", "bot")
	if exit, code, _ := agentRefusal(t, db, "comment", id(n), "from an agent"); exit != 3 || code != "not_claimant" {
		t.Errorf("an agent named by MONOAGENT_ACTOR on a task it does not hold: exit %d, %q", exit, code)
	}
	if after := agentSnapshot(t, db); after != before {
		t.Errorf("refused comments changed the board:\nbefore\n%s\nafter\n%s", before, after)
	}
	// The operator's own comment is the operator's, and an agent's, on a task it holds, is the agent's.
	setenv(t, "MONOAGENT_ACTOR", "")
	agentRun(t, db, "comment", id(n), "I will look at this tomorrow")
	if got := agentKinds(agentEvents(t, db, n)); got != "you:created you:comment" {
		t.Errorf("history after the operator's comment: %s", got)
	}
	ready := opsAdd(t, db, "go", "--ready")
	agentRun(t, db, "claim", id(ready), "--as", "bot")
	agentRun(t, db, "comment", id(ready), "reproduced", "it", "--as", "bot")
	if got := agentKinds(agentEvents(t, db, ready)); got != "you:created bot:claimed bot:comment" {
		t.Errorf("history after the agent's comment: %s", got)
	}
}
