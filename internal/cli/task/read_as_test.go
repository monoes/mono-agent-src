package task

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// A command a printer suggests carries the name of the agent that runs the command, at its end: pasted
// without it, it would run as the operator (and read the Inbox, or write as the operator). The
// operator's commands carry none. A name is written out only when a shell reads it as one word and
// the store would take it; any other is the placeholder <name>, because a pasted command does what it
// says (a ; or a $( in a name would be a second command).
func TestTaskCommandCarriesTheNameOfAnAgent(t *testing.T) {
	t.Parallel()
	work := tasks.Profile{ID: "work-id", Name: "Work"}
	const base = "monoagentcli --profile work-id task list --limit 2000"
	long := strings.Repeat("a", 64)
	for _, c := range []struct{ as, want string }{
		{"", base},
		{"bot", base + " --as bot"},
		{"claude-7f3a", base + " --as claude-7f3a"},
		{"agent:claude-code#a3f9", base + " --as agent:claude-code#a3f9"},
		{"a.b_c@d:e-f", base + " --as a.b_c@d:e-f"},
		{"7", base + " --as 7"},
		{"-x", base + " --as -x"},
		{long, base + " --as " + long},
		// What a shell would not read as one word, or as a name at all, is not written out.
		{"has space", base + " --as <name>"},
		{"x; echo hi", base + " --as <name>"},
		{"$(id)", base + " --as <name>"},
		{"`id`", base + " --as <name>"},
		{"a$HOME", base + " --as <name>"},
		{"it's", base + " --as <name>"},
		{`say "hi"`, base + " --as <name>"},
		{"a|b", base + " --as <name>"},
		{"a&b", base + " --as <name>"},
		{"a>b", base + " --as <name>"},
		{"a<b", base + " --as <name>"},
		{`a\b`, base + " --as <name>"},
		{"a*", base + " --as <name>"},
		{"~", base + " --as <name>"},
		{"a\nb", base + " --as <name>"},
		{"a\tb", base + " --as <name>"},
		{"a\x00b", base + " --as <name>"},
		{"caf\U000000e9", base + " --as <name>"},
		{"#starts-a-comment", base + " --as <name>"},
		{long + "a", base + " --as <name>"},
		{blankAs, base + " --as <name>"},
		{"<name>", base + " --as <name>"},
	} {
		if got := taskCommand(work, c.as, "list --limit 2000"); got != c.want {
			t.Errorf("taskCommand with the name %q = %q, want %q", c.as, got, c.want)
		}
	}
}

// The profile is written by its id, and a pasted command does what it says: an id that a shell reads as
// one word (a UUID, default) is written as it is, and any other as <profile-id>, as a name that is no
// word is written <name>. The ids are UUIDs today, but nothing in the database says so.
func TestTaskCommandWritesTheProfileIdOnlyWhenItIsOneShellWord(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ id, want string }{
		{"default", "default"},
		{"4f6c1d52-0b8e-4c1a-9e3d-7a52b9c0d1e8", "4f6c1d52-0b8e-4c1a-9e3d-7a52b9c0d1e8"},
		{"a.b_c@d:e-f", "a.b_c@d:e-f"},
		{"7", "7"},
		// What a shell would not read as one word is not written out.
		{"odd id; echo hi", "<profile-id>"},
		{"has space", "<profile-id>"},
		{"x;y", "<profile-id>"},
		{"$(id)", "<profile-id>"},
		{"`id`", "<profile-id>"},
		{"a$HOME", "<profile-id>"},
		{"it's", "<profile-id>"},
		{`say "hi"`, "<profile-id>"},
		{"a|b", "<profile-id>"},
		{"a&b", "<profile-id>"},
		{"a>b", "<profile-id>"},
		{"a<b", "<profile-id>"},
		{`a\b`, "<profile-id>"},
		{"a*", "<profile-id>"},
		{"~", "<profile-id>"},
		{"#x", "<profile-id>"},
		{"a\nb", "<profile-id>"},
		{"a\tb", "<profile-id>"},
		{"caf\U000000e9", "<profile-id>"},
		{"", "<profile-id>"},
		{"<id>", "<profile-id>"},
		{"<profile-id>", "<profile-id>"},
	} {
		want := "monoagentcli --profile " + c.want + " task list"
		if got := taskCommand(tasks.Profile{ID: c.id}, "", "list"); got != want {
			t.Errorf("taskCommand with the profile id %q = %q, want %q", c.id, got, want)
		}
		if got := taskCommand(tasks.Profile{ID: c.id}, "bot", "list"); got != want+" --as bot" {
			t.Errorf("taskCommand with the profile id %q and an agent = %q, want %q", c.id, got, want+" --as bot")
		}
	}
	// A profile whose id is no shell word, as the commands print about it by its name: what they suggest
	// carries the placeholder, and the id is not in the text anywhere.
	db := newTaskTestDB(t)
	addTaskProfile(t, db, "odd id; echo hi", "Odd")
	seedTaskRows(t, db,
		taskSeed{profile: "odd id; echo hi", title: "one", status: "ready"},
		taskSeed{profile: "odd id; echo hi", title: "two", status: "ready"},
	)
	list, _, err := runTask(t, db, "Odd", false, "", "list", "--limit", "1")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(list, "\n"), "\n")
	if got, want := lines[len(lines)-1], moreLine("1", "<profile-id>", ""); got != want || strings.Contains(list, "echo hi") {
		t.Errorf("the list of a profile with an odd id ends with %q, want %q (and no id):\n%s", got, want, list)
	}
}

// Every command the list and show print carries the agent's name when the caller is an agent that has
// one, whether it said so with --as or with MONOAGENT_ACTOR; it carries none for the operator, and none
// for an agent that has no name (a blank --as, or an agent context marker alone): the blank is never
// written into a command.
func TestTaskHintsCarryTheNameOfTheAgentThatRunsTheCommand(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	addTaskProfile(t, db, "work-id", "Work")
	ids := seedTaskRows(t, db,
		taskSeed{profile: "work-id", title: "one", status: "ready"},
		taskSeed{profile: "work-id", title: "two", status: "ready"},
		taskSeed{profile: "work-id", title: "three", status: "ready"},
		taskSeed{profile: "work-id", title: "with a long note", status: "ready"},
	)
	long := ids[3]
	seedTaskEvent(t, db, long, time.Now(), "bot", "comment", "", "", strings.Repeat("x", 300))
	env := func(name, value string) func(t *testing.T) { return func(t *testing.T) { setenv(t, name, value) } }
	for _, c := range []struct {
		name  string
		setup func(t *testing.T)
		args  []string
		as    string // what the commands carry after --as; "" for no --as at all
	}{
		{"the operator", nil, nil, ""},
		{"an agent named by --as", nil, []string{"--as", "bot"}, "bot"},
		{"an agent named by MONOAGENT_ACTOR", env("MONOAGENT_ACTOR", "bot"), nil, "bot"},
		{"--as wins over MONOAGENT_ACTOR", env("MONOAGENT_ACTOR", "other"), []string{"--as", "bot"}, "bot"},
		{"an agent context marker and a name", env("CLAUDECODE", "1"), []string{"--as", "bot"}, "bot"},
		{"a blank --as that MONOAGENT_ACTOR names", env("MONOAGENT_ACTOR", "bot"), []string{"--as", ""}, "bot"},
		{"a name that is an MCP client's", nil, []string{"--as", "agent:claude-code#a3f9"}, "agent:claude-code#a3f9"},
		{"a name of 64 characters", nil, []string{"--as", strings.Repeat("a", 64)}, strings.Repeat("a", 64)},
		{"a blank --as", nil, []string{"--as", ""}, ""},
		{"a blank --as of spaces", nil, []string{"--as", "   "}, ""},
		{"a blank --as and a marker", env("CLAUDECODE", "1"), []string{"--as", ""}, ""},
		{"an agent context marker and no name", env("CLAUDECODE", "1"), nil, ""},
		{"a name with a ; in it", nil, []string{"--as", "x; echo hi"}, "<name>"},
		{"a name that starts a comment", nil, []string{"--as", "#x"}, "<name>"},
		{"a name with $( in it", nil, []string{"--as", "$(id)"}, "<name>"},
		{"a name of 65 characters", nil, []string{"--as", strings.Repeat("a", 65)}, "<name>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.setup != nil {
				c.setup(t)
			}
			list, _, err := runTask(t, db, "Work", false, "", append([]string{"list", "--limit", "1"}, c.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimRight(list, "\n"), "\n")
			if got, want := lines[len(lines)-1], moreLine("3", "work-id", c.as); got != want {
				t.Errorf("the list ends with %q, want %q", got, want)
			}
			show, _, err := runTask(t, db, "Work", false, "", append([]string{"show", strconv.FormatInt(long, 10)}, c.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			footer := fmt.Sprintf("(full text: monoagentcli --profile work-id task show %d --json", long)
			if c.as != "" {
				footer += " --as " + c.as
			}
			footer += ")"
			if !strings.HasSuffix(show, "\n"+footer+"\n") {
				t.Errorf("show does not end with the line %q:\n%s", footer, show)
			}
			if c.as == "" && (strings.Contains(list, "--as") || strings.Contains(show, "--as")) {
				t.Errorf("a command for a caller with no agent name carries --as:\n%s\n%s", list, show)
			}
		})
	}
}
