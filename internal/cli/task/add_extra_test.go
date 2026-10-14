package task

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// errText is err's message, or "" for no error.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// watchedInput is a standard input that counts how often it is read.
type watchedInput struct {
	in    io.Reader
	reads int
}

func (w *watchedInput) Read(p []byte) (int, error) {
	w.reads++
	return w.in.Read(p)
}

// A call that is refused without its text must say so without waiting for it: on
// a standard input that never closes, the refusal would never come.
func TestTaskAddRefusalsNeverWaitForStandardInput(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	refused := func(wantCode string, args ...string) {
		t.Helper()
		in := &watchedInput{in: strings.NewReader("text")}
		out, _, err := runTaskIn(t, db, "default", true, in, args...)
		var doc map[string]any
		if exitCode(err) != 3 || json.Unmarshal([]byte(out), &doc) != nil || doc["code"] != wantCode {
			t.Errorf("task %s: exit %d, %q; want exit 3 and code %s", strings.Join(args, " "), exitCode(err), out, wantCode)
		}
		if in.reads != 0 {
			t.Errorf("task %s: a refused call read standard input %d times", strings.Join(args, " "), in.reads)
		}
	}
	refused("operator_only", "add", "--stdin", "--ready", "--as", "bot")
	refused("invalid_input", "add", "--stdin", "--source", "os", "--as", "bot")
	refused("operator_only", "add", "--stdin", "--source", "os", "--ready")
	setenv(t, "CLAUDECODE", "1")
	refused("operator_only", "add", "--stdin", "--ready")
	refused("invalid_input", "add", "--stdin", "--source", "os")

	// The control: a call that is not refused does read its standard input.
	setenv(t, "CLAUDECODE", "")
	in := &watchedInput{in: strings.NewReader("text to file")}
	if _, _, err := runTaskIn(t, db, "default", true, in, "add", "--stdin"); err != nil || in.reads == 0 {
		t.Errorf("an add that is not refused: %v, %d reads", err, in.reads)
	}
}

func TestTaskAddReadsOnlyTheFirstMebibyteOfStandardInput(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, strings.Repeat("a", 2<<20), "add", "--stdin")
	// The notice counts what was read (1 MiB), not what was sent (2 MiB).
	if want := "[truncated: 1048576 characters in the original]"; !strings.HasSuffix(added.Task.Notes, want) {
		t.Errorf("the notes end %q, want the notice %q", added.Task.Notes[max(len(added.Task.Notes)-80, 0):], want)
	}
}

func TestTaskAddRefusalsComeBeforeTheDatabaseIsOpened(t *testing.T) {
	t.Parallel()
	newTaskTestDB(t)
	// A database path under a regular file can never be opened, so initDB fails.
	file := filepath.Join(t.TempDir(), "a-regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(file, "sub", "tasks.db")
	if _, _, err := runTask(t, db, "default", true, "", "add", "x"); exitCode(err) != 1 {
		t.Fatalf("the control, an add that needs the database: exit %d (%v), want the plain error 1", exitCode(err), err)
	}
	for _, c := range []struct {
		code   string
		phrase string // words the error must also hold: the macOS phase reads this one through the CLI
		args   []string
	}{
		{"operator_only", "", []string{"add", "x", "--as", "bot", "--ready"}},
		{"operator_only", "", []string{"add", "--stdin", "--source", "os", "--ready"}},
		{"invalid_input", `source "os" is for captures`, []string{"add", "x", "--as", "bot", "--source", "os"}},
	} {
		doc := failedTaskJSON(t, db, "default", 3, c.args...)
		if msg, _ := doc["error"].(string); doc["code"] != c.code || !strings.Contains(msg, c.phrase) {
			t.Errorf("task %s: %v, want code %s and the words %q", strings.Join(c.args, " "), doc, c.code, c.phrase)
		}
	}
}

func TestTaskCommandsFallBackToTheActiveProfile(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	setActive := func(id string) {
		t.Helper()
		raw, err := storage.NewDatabase(db)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		if _, err := raw.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('work-id', 'Work')`); err != nil {
			t.Fatal(err)
		}
		if _, err := raw.DB.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('active_profile_id', ?)`, id); err != nil {
			t.Fatal(err)
		}
	}
	setActive("work-id")
	var added addedJSON
	mustTaskJSON(t, db, "", &added, "", "add", "to the active profile")
	if added.Profile.ID != "work-id" {
		t.Errorf("no --profile: the task went to %+v, want the active profile work-id", added.Profile)
	}
	setActive("a-profile-that-is-gone")
	if doc := failedTaskJSON(t, db, "", 3, "add", "x"); doc["code"] != "invalid_input" {
		t.Errorf("an active profile that does not exist: %v", doc)
	}
}

func TestTaskAddReplayIsReportedAsAlreadyAdded(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	first, _, err1 := runTask(t, db, "default", false, "", "add", "once", "--client-id", "c-9")
	second, _, err2 := runTask(t, db, "default", false, "", "add", "once", "--client-id", "c-9")
	rest, ok1 := strings.CutPrefix(first, "Added ")
	again, ok2 := strings.CutPrefix(second, "Already added as ")
	if err1 != nil || err2 != nil || !ok1 || !ok2 || rest != again || !strings.HasPrefix(rest, "#") {
		t.Errorf("first %q (%v), second %q (%v)", first, err1, second, err2)
	}
}

// --source goes to the store, which decides what each actor may claim: chrome is
// a capture surface's and app is the operator's, neither is an agent's.
func TestTaskAddSourceFlagReachesTheStore(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	if doc := failedTaskJSON(t, db, "default", 3, "add", "x", "--source", "chrome"); doc["code"] != "invalid_input" {
		t.Errorf("the operator claiming chrome: %v", doc)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "add", "x", "--as", "bot", "--source", "app"); doc["code"] != "invalid_input" {
		t.Errorf("an agent claiming app: %v", doc)
	}
}

// parsedCaller is the caller `task add <args>` sees, read the way every command
// reads it: callerFor(flagAs(cmd)).
func parsedCaller(t *testing.T, args ...string) Caller {
	t.Helper()
	add, _, err := NewCommand(testDeps(t, "", "", false)).Find([]string{"add"})
	if err != nil {
		t.Fatal(err)
	}
	if err := add.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return testDeps(t, "", "", false).callerFor(flagAs(add))
}

// An --as that is given and blank (--as "", or --as "$NAME" with NAME unset) is
// an agent that has not said its name, never the operator.
func TestTaskFlagAsReadsABlankAsAsAnAgentWithoutAName(t *testing.T) {
	t.Parallel()
	newTaskTestDB(t) // the operator's environment
	setenv(t, "MONOAGENT_ACTOR", "")
	if c := parsedCaller(t); c.isAgent() {
		t.Errorf("no --as, MONOAGENT_ACTOR empty: %+v", c)
	}
	unsetenv(t, "MONOAGENT_ACTOR") // restored when the test ends
	if c := parsedCaller(t); c.isAgent() {
		t.Errorf("no --as, MONOAGENT_ACTOR not set: %+v", c)
	}
	for _, args := range [][]string{{"--as", ""}, {"--as", "   "}, {"--as="}} {
		c := parsedCaller(t, args...)
		if !c.isAgent() || c.actor.Name != "" {
			t.Errorf("%q: %+v, want an agent with no name", args, c)
			continue
		}
		_, err := c.operator("approve a task")
		if exitCode(err) != 3 || !strings.Contains(errText(err), "--as") || !strings.Contains(errText(err), "no name") {
			t.Errorf("%q as the operator: %v", args, err)
		}
		_, err = c.agent()
		if exitCode(err) != 3 || !strings.Contains(errText(err), "--as needs a name") {
			t.Errorf("%q as an agent: %v", args, err)
		}
	}
	// MONOAGENT_ACTOR that names an agent fills in for a blank --as, as it does for none.
	setenv(t, "MONOAGENT_ACTOR", "from-env")
	if a, err := parsedCaller(t, "--as", "").agent(); err != nil || a.Name != "from-env" {
		t.Errorf("a blank --as with MONOAGENT_ACTOR: %+v, %v", a, err)
	}
	if a, err := parsedCaller(t, "--as", "flag").agent(); err != nil || a.Name != "flag" {
		t.Errorf("--as flag with MONOAGENT_ACTOR: %+v, %v", a, err)
	}
	// With an agent-context marker as well, the refusal for the operator names both.
	setenv(t, "MONOAGENT_ACTOR", "")
	setenv(t, "CLAUDECODE", "1")
	both := parsedCaller(t, "--as", "")
	if _, err := both.operator("approve a task"); !strings.Contains(errText(err), "CLAUDECODE") || !strings.Contains(errText(err), "--as") {
		t.Errorf("a marker and a blank --as, as the operator: %v", err)
	}
	if _, err := both.agent(); !strings.Contains(errText(err), "--as needs a name") {
		t.Errorf("a marker and a blank --as, as an agent: %v", err)
	}
}

// flagAs says nothing of an --as that was left out or that names someone, and
// marks the blank one that was given.
func TestTaskFlagAsMarksABlankAsAndNothingElse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"--as", "bot"}, "bot"},
		{[]string{"--as", ""}, blankAs},
		{[]string{"--as", "   "}, blankAs},
		{[]string{"--as="}, blankAs},
	} {
		add, _, err := NewCommand(testDeps(t, "", "", false)).Find([]string{"add"})
		if err != nil {
			t.Fatal(err)
		}
		if err := add.ParseFlags(c.args); err != nil {
			t.Fatal(err)
		}
		if got := flagAs(add); got != c.want {
			t.Errorf("flagAs after %q = %q, want %q", c.args, got, c.want)
		}
	}
}

// blankAs must never pass for a name: if it ever reached the store it is refused.
func TestTaskBlankAsIsNotANameTheStoreAccepts(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	raw, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_, _, err = tasks.NewStore(raw.DB).Add(context.Background(), "default", tasks.AddInput{Title: "x"}, tasks.Actor{Kind: tasks.Agent, Name: blankAs})
	if !errors.Is(err, tasks.ErrInvalid) {
		t.Errorf("an agent named %q: %v, want ErrInvalid", blankAs, err)
	}
}

func TestTaskABlankAsIsNeverTheOperator(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, blank := range []string{"", "   "} {
		doc := failedTaskJSON(t, db, "default", 3, "add", "x", "--ready", "--as", blank)
		if msg, _ := doc["error"].(string); doc["code"] != "operator_only" || !strings.Contains(msg, "--as") {
			t.Errorf("an operator-only add with --as %q: %v", blank, doc)
		}
	}
	// What is not operator-only goes through, as an agent's task.
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "from nobody", "--as", "")
	if added.Task.Source.Kind != "agent" || added.Task.Status != "inbox" {
		t.Errorf("an add with a blank --as: %+v", added.Task)
	}
}
