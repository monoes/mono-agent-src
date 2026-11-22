### Task 1: The bindings

**Files:**
- Create: `wails-app/app_tasks.go`
- Test: `wails-app/app_tasks_test.go`

**Interfaces:**
- Consumes: `tasks.NewStore(*sql.DB)`, `(*tasks.Store).Board(ctx, profileID string, doneLimit int, actor tasks.Actor) (tasks.Board, error)` (it refuses every actor but the operator, `tasks.Actor{Kind: tasks.Human}`, with `tasks.ErrOperatorOnly`) and `(*tasks.Store).Add` (tests), the `tasks.Board` document (Task 0 checks them); `findMonoAgentCLI() (string, error)`, `hideWindow(*exec.Cmd)`, `cliResultJSON(cliBin string, stdout []byte, runErr error) string` (stdout verbatim on success; on failure the CLI's own `{"error",…}` stdout when it printed one), `aiError(err error) string`, `codedError{msg, code string}` (`aiError(&codedError{...})` gives `{"code","error"}`), `(*App).getActiveProfileID()`, `a.ctx`; test helpers `fakeCLI(t, body string) string` (`app_health_stream_test.go`, `!windows`), `loggedArgs(t, path string) []string` (`app_hil_test.go`), `newTestApp(t) *App` (`app_workflows_test.go`); `orgsign.AgentContextMarker()`, `orgsign.AgentContextMarkers()`.
- Produces (bound to the frontend; Task 3 writes their JS entries):
  - `(*App).TaskBoard(doneLimit int) string`
  - `(*App).TaskShow(id int64) string`
  - `(*App).TaskAdd(spec string) string` (spec JSON `{"title","notes","text","ready"}`)
  - `(*App).TaskEdit(id int64, spec string) string` (spec JSON `{"title"?, "notes"?}`)
  - `(*App).TaskMove(id int64, status, where string, ref int64) string` (where `""`, `top`, `bottom`, `before`, `after`)
  - `(*App).TaskApprove(ids []int64, top bool) string`, `(*App).TaskArchive(ids []int64) string`, `(*App).TaskUnarchive(ids []int64) string`
  - `(*App).TaskComment(id int64, text string) string`
  - `(*App).TaskAgentShell() string`
  - unexported: `taskCLI(stdin string, args ...string) string`, `taskRefusal(msg string) string`, `taskIDArgs`, `taskBoardStatuses`, `taskCLITimeout`, `boardDoneLimit` (50), `boardLimit(n int) int`, `maxArgNotes` (30,000), `notesTooLong`; test helper `addTestTask(t, a, profileID, title)` (Task 2 uses it too).
- `TaskBoard` reads the board in process (`Store.Board`, one snapshot, asked as the operator) and returns the `tasks.Board` document, the same one `task board --json` prints, or `{"error"}`: P1 made `task board` operator-only (the CLI refuses an agent-driven caller, and the store every actor but the operator), and an app started from an agent's shell must still show its board, so it asks as the operator there too (Ruling). Every other binding but `TaskAgentShell` runs the CLI and returns its stdout verbatim, or `{"code":"invalid_input","error":…}` when it refuses its input before running the CLI.

- [ ] **Step 1: Write the failing tests**

Create `wails-app/app_tasks_test.go`:

```go
//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/tasks"
)

// addTestTask adds a task to a profile's board in process, as the operator.
func addTestTask(t *testing.T, a *App, profileID, title string) {
	t.Helper()
	_, _, err := tasks.NewStore(a.db).Add(context.Background(), profileID, tasks.AddInput{Title: title}, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		t.Fatal(err)
	}
}

// taskCLIFake installs a monoagentcli stand-in that logs each call as one
// line with every argument followed by "|" (so where an argument ends shows),
// saves its standard input, prints out and exits with code. It returns the
// paths of the log and of the saved standard input.
func taskCLIFake(t *testing.T, out string, code int) (argsLog, stdinFile string) {
	t.Helper()
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	stdinFile = filepath.Join(dir, "stdin")
	outFile := filepath.Join(dir, "out.json")
	if err := os.WriteFile(outFile, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t,
		"for a in \"$@\"; do printf '%s|' \"$a\"; done >> '"+argsLog+"'\n"+
			"echo >> '"+argsLog+"'\n"+
			"cat > '"+stdinFile+"'\n"+
			"cat '"+outFile+"'\n"+
			"exit "+strconv.Itoa(code)+"\n"))
	return argsLog, stdinFile
}

func newTaskTestApp(t *testing.T) *App {
	t.Helper()
	a := newTestApp(t)
	a.ctx = context.Background()
	a.setActiveProfileID("work")
	return a
}

// Every binding that runs the CLI runs `task …` for the active profile with
// its arguments exactly so: user text after "--" or in the --flag=value form,
// so text that starts with a dash is never read as a flag. Each returns
// stdout verbatim.
func TestTaskBindingsPassTheirArgumentsExactly(t *testing.T) {
	log, _ := taskCLIFake(t, `{"ok":true}`, 0)
	a := newTaskTestApp(t)
	for i, got := range []string{
		a.TaskShow(12),
		a.TaskAdd(`{"title":"-x fix the bug","notes":"--help me"}`),
		a.TaskAdd(`{"title":"urgent","ready":true}`),
		a.TaskEdit(12, `{"title":"--new"}`),
		a.TaskEdit(12, `{"notes":""}`),
		a.TaskMove(12, "ready", "", 0),
		a.TaskMove(12, "done", "top", 0),
		a.TaskMove(12, "review", "before", 9),
		a.TaskMove(12, "in_progress", "after", 9),
		a.TaskApprove([]int64{3, 4}, false),
		a.TaskApprove([]int64{3}, true),
		a.TaskArchive([]int64{5}),
		a.TaskUnarchive([]int64{5, 6}),
		a.TaskComment(12, "-looks odd"),
	} {
		if got != `{"ok":true}` {
			t.Fatalf("call %d did not return the CLI's stdout verbatim: %q", i, got)
		}
	}
	p := "--profile|work|--json|task|"
	want := []string{
		p + "show|12|",
		p + "add|--source|app|--notes=--help me|--|-x fix the bug|",
		p + "add|--source|app|--ready|--|urgent|",
		p + "edit|12|--title=--new|",
		p + "edit|12|--notes=|",
		p + "move|12|ready|",
		p + "move|12|done|--top|",
		p + "move|12|review|--before|9|",
		p + "move|12|in_progress|--after|9|",
		p + "approve|3|4|",
		p + "approve|3|--top|",
		p + "archive|5|",
		p + "unarchive|5|6|",
		p + "comment|12|--|-looks odd|",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Text of several lines goes on standard input: the CLI makes its first
// line the title and all of it the notes (spec 4.6).
func TestTaskAddSendsTextOnStandardInput(t *testing.T) {
	log, stdin := taskCLIFake(t, `{"created":true}`, 0)
	a := newTaskTestApp(t)
	text := "Reply to Sam\n--about the invoice"
	spec, _ := json.Marshal(map[string]any{"text": text, "ready": true})
	if got := a.TaskAdd(string(spec)); got != `{"created":true}` {
		t.Fatalf("TaskAdd = %q", got)
	}
	if got := loggedArgs(t, log); len(got) != 1 || got[0] != "--profile|work|--json|task|add|--source|app|--ready|--stdin|" {
		t.Fatalf("CLI call: %q", got)
	}
	if raw, err := os.ReadFile(stdin); err != nil || string(raw) != text {
		t.Fatalf("standard input %q (%v), want %q", raw, err, text)
	}
}

// Input no command could accept is refused here, as {"error","code"}, and
// the CLI never runs.
func TestTaskBindingsRefuseBadInputWithoutRunningTheCLI(t *testing.T) {
	log, _ := taskCLIFake(t, `{"ok":true}`, 0)
	a := newTaskTestApp(t)
	long := `{"notes":"` + strings.Repeat("x", maxArgNotes+1) + `"}`
	longAdd := `{"title":"a","notes":"` + strings.Repeat("x", maxArgNotes+1) + `"}`
	for name, got := range map[string]string{
		"a title of spaces":   a.TaskAdd(`{"title":"  "}`),
		"title and text":      a.TaskAdd(`{"title":"a","text":"b"}`),
		"notes with text":     a.TaskAdd(`{"text":"b","notes":"n"}`),
		"long notes on add":   a.TaskAdd(longAdd),
		"not JSON":            a.TaskAdd(`{`),
		"an edit of nothing":  a.TaskEdit(3, `{}`),
		"edit id 0":           a.TaskEdit(0, `{"title":"x"}`),
		"notes over the cap":  a.TaskEdit(3, long),
		"show id -1":          a.TaskShow(-1),
		"move to archived":    a.TaskMove(3, "archived", "", 0),
		"an unknown place":    a.TaskMove(3, "ready", "middle", 0),
		"before no card":      a.TaskMove(3, "ready", "before", 0),
		"after itself":        a.TaskMove(3, "ready", "after", 3),
		"approve nothing":     a.TaskApprove(nil, false),
		"archive id 0":        a.TaskArchive([]int64{0}),
		"unarchive nothing":   a.TaskUnarchive([]int64{}),
		"a comment of spaces": a.TaskComment(3, " \n"),
	} {
		var doc struct{ Error, Code string }
		if err := json.Unmarshal([]byte(got), &doc); err != nil || doc.Error == "" || doc.Code != "invalid_input" {
			t.Errorf("%s: %q, want an invalid_input refusal", name, got)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("the CLI ran for refused input: %v", loggedArgs(t, log))
	}
}

// A refusal comes back as the CLI printed it, code and all, so the page can
// tell operator_only from claimed or not_found.
func TestTaskBindingsReturnTheCLIRefusalVerbatim(t *testing.T) {
	refusal := `{"code":"operator_only","error":"CLAUDECODE is set, so an agent is running this command: only the operator can move a task"}`
	taskCLIFake(t, refusal, 3)
	a := newTaskTestApp(t)
	if got := a.TaskMove(7, "ready", "", 0); got != refusal {
		t.Fatalf("TaskMove = %q", got)
	}
}

// The board is read in process, in the document `task board --json` prints,
// because the CLI refuses `task board` to an agent-driven caller: under an
// inherited CLAUDECODE the app still shows its board, while an action still
// runs the CLI, whose guard refuses it, and the page shows the banner. The
// store refuses a whole-board read to every actor but the operator, so the
// board coming back here means the binding asks as the operator: the marker
// says what started the app, not who is looking at its window.
func TestTaskBoardIsReadInProcessEvenUnderAnInheritedMarker(t *testing.T) {
	refusal := `{"code":"operator_only","error":"CLAUDECODE is set, so an agent is running this command: only the operator can move a task"}`
	log, _ := taskCLIFake(t, refusal, 3)
	t.Setenv("CLAUDECODE", "1")
	a := newTestApp(t)
	a.ctx = context.Background()
	addTestTask(t, a, "default", "first")
	addTestTask(t, a, "default", "second")
	// An agent asking for the same board is refused, so the board below was
	// asked for as the operator, not as the actor the marker would make.
	if _, err := tasks.NewStore(a.db).Board(a.ctx, "default", 0, tasks.Actor{Kind: tasks.Agent, Name: "bot"}); !errors.Is(err, tasks.ErrOperatorOnly) {
		t.Fatalf("the store's board for an agent = %v, want ErrOperatorOnly", err)
	}
	raw := a.TaskBoard(0)
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil || len(top) != 4 || top["profile"] == nil || top["rev"] == nil || top["counts"] == nil || top["tasks"] == nil {
		t.Fatalf("TaskBoard = %s (%v), want the four keys profile, rev, counts, tasks", raw, err)
	}
	var doc struct {
		Profile struct{ ID string }          `json:"profile"`
		Rev     int64                        `json:"rev"`
		Counts  struct{ Inbox int }          `json:"counts"`
		Tasks   map[string][]json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Profile.ID != "default" || doc.Rev != 2 || doc.Counts.Inbox != 2 || len(doc.Tasks) != 5 || len(doc.Tasks["inbox"]) != 2 || doc.Tasks["done"] == nil {
		t.Fatalf("board = %+v (empty columns must be [], never null)", doc)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("the board read ran the CLI: %v", loggedArgs(t, log))
	}
	if got := a.TaskMove(1, "ready", "", 0); got != refusal {
		t.Fatalf("a move under the marker = %q, want the CLI's operator_only refusal", got)
	}
	if got := a.TaskAgentShell(); got != "CLAUDECODE" {
		t.Fatalf("TaskAgentShell = %q: the page would not show the read-only banner", got)
	}
}

// The app never asks for every Done card: a limit that is not positive is
// the board's 50 (Store.Board reads 0 as all of them).
func TestTaskBoardLimitIsNeverEveryDoneCard(t *testing.T) {
	for in, want := range map[int]int{0: 50, -3: 50, 10: 10, 50: 50} {
		if got := boardLimit(in); got != want {
			t.Errorf("boardLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

// The app's children inherit its environment: the binding names the agent
// marker (or MONOAGENT_ACTOR) the CLI would see, so the page can go
// read-only before anything is refused.
func TestTaskAgentShellNamesTheMarkerTheAppInherited(t *testing.T) {
	for _, m := range orgsign.AgentContextMarkers() {
		t.Setenv(m, "")
	}
	t.Setenv("MONOAGENT_ACTOR", "")
	a := newTaskTestApp(t)
	if got := a.TaskAgentShell(); got != "" {
		t.Fatalf("no marker: %q", got)
	}
	t.Setenv("MONOAGENT_ACTOR", "bot")
	if got := a.TaskAgentShell(); got != "MONOAGENT_ACTOR" {
		t.Fatalf("MONOAGENT_ACTOR set: %q", got)
	}
	t.Setenv("CLAUDECODE", "1")
	if got := a.TaskAgentShell(); got != "CLAUDECODE" {
		t.Fatalf("CLAUDECODE set: %q", got)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go -C wails-app test -run 'TestTaskBindings|TestTaskAdd|TestTaskAgentShell|TestTaskBoard' -count=1 .`
Expected: FAIL to compile (`a.TaskBoard undefined` and the rest).

- [ ] **Step 3: Write `wails-app/app_tasks.go`**

```go
// wails-app/app_tasks.go
//
// The Tasks tab's bindings (docs/mastermind/specs/2026-10-05-task-board-design.md
// §10). Each action runs `monoagentcli --profile <active> --json task …` and
// returns its stdout verbatim: the document, or the CLI's {"error","code"} on
// a refusal, so the page can tell operator_only from claimed or not_found.
// The board is the one read done in process (TaskBoard), as the watcher
// (app_tasks_watch.go) reads the revision: the CLI refuses `task board` to an
// agent-driven caller, and an app started from an agent's shell must still
// show its board, read-only.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/tasks"
)

// taskCLITimeout bounds one task command: each is a few small queries.
const taskCLITimeout = 30 * time.Second

// boardDoneLimit is how many Done cards the board shows (spec §10).
const boardDoneLimit = 50

// maxArgNotes caps the notes the app passes on the command line, which
// Windows limits to 32,767 characters in all; longer notes are saved with
// the CLI in a terminal.
const maxArgNotes = 30000

// notesTooLong is the refusal for notes over maxArgNotes, in TaskAdd and TaskEdit.
const notesTooLong = "these notes are too long to save from the app (over 30,000 characters); save them in a terminal with monoagentcli task"

// taskBoardStatuses are the columns a card can be moved to; archiving has
// its own binding.
var taskBoardStatuses = map[string]bool{"inbox": true, "ready": true, "in_progress": true, "review": true, "done": true}

// taskRefusal is the {"error","code"} document of input refused before the
// CLI runs.
func taskRefusal(msg string) string {
	return aiError(&codedError{msg: msg, code: "invalid_input"})
}

// taskCLI runs `monoagentcli --profile <active> --json task <args…>` with
// stdin when it is not empty, and returns stdout verbatim or the {"error"}
// shape (cliResultJSON keeps the CLI's own error document and its code).
func (a *App) taskCLI(stdin string, args ...string) string {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, taskCLITimeout)
	defer cancel()
	full := append([]string{"--profile", a.getActiveProfileID(), "--json", "task"}, args...)
	cmd := exec.CommandContext(ctx, cliBin, full...)
	hideWindow(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, runErr := cmd.Output()
	return cliResultJSON(cliBin, out, runErr)
}

// TaskBoard is the active profile's whole board, read in one snapshot with
// Store.Board and returned as the document `task board --json` prints. It
// skips the CLI because `task board` refuses an agent-driven caller (the
// whole board would hand an agent every unreviewed Inbox card, spec §4.1),
// while an app started from an agent's shell must still show its board,
// read-only. The store refuses a whole-board read to every actor but the
// operator, so the read is made as the operator, always, the markers this
// app inherited included: the one who asks is the person at the window, the
// markers only say what started the app, and an actor taken from them, as
// the CLI takes it, would leave that tab with a refusal in place of its
// board. Every action below runs the CLI, whose guard refuses it there.
func (a *App) TaskBoard(doneLimit int) string {
	if a.db == nil {
		return aiError(errors.New("the database is not open yet"))
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	b, err := tasks.NewStore(a.db).Board(ctx, a.getActiveProfileID(), boardLimit(doneLimit), tasks.Actor{Kind: tasks.Human})
	if err != nil {
		return aiError(err)
	}
	out, err := json.Marshal(b)
	if err != nil {
		return aiError(err)
	}
	return string(out)
}

// boardLimit is the Done cards a board read asks for: Store.Board reads 0 as
// every Done card, which the app never wants, so a limit that is not
// positive is boardDoneLimit.
func boardLimit(n int) int {
	if n <= 0 {
		return boardDoneLimit
	}
	return n
}

// TaskShow is `task show ID`: one task with its history.
func (a *App) TaskShow(id int64) string {
	if id <= 0 {
		return taskRefusal("a task id is a positive number")
	}
	return a.taskCLI("", "show", strconv.FormatInt(id, 10))
}

// taskAddSpec is TaskAdd's argument: a title (with optional notes), or text
// whose first line the CLI makes the title, and whether the task goes
// straight to Ready (the operator's choice).
type taskAddSpec struct {
	Title string `json:"title"`
	Notes string `json:"notes"`
	Text  string `json:"text"`
	Ready bool   `json:"ready"`
}

// TaskAdd is `task add --source app`: a title as one argument after "--",
// so a title that starts with a dash stays a title, or text on standard
// input with --stdin.
func (a *App) TaskAdd(spec string) string {
	var s taskAddSpec
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		return taskRefusal("the task is not valid JSON: " + err.Error())
	}
	hasTitle, hasText := strings.TrimSpace(s.Title) != "", strings.TrimSpace(s.Text) != ""
	switch {
	case !hasTitle && !hasText:
		return taskRefusal("a task needs a title")
	case hasTitle && hasText:
		return taskRefusal("give a title or a text, not both")
	case hasText && s.Notes != "":
		return taskRefusal("give notes or text, not both")
	case len(s.Notes) > maxArgNotes:
		return taskRefusal(notesTooLong)
	}
	args := []string{"add", "--source", "app"}
	if s.Ready {
		args = append(args, "--ready")
	}
	if hasText {
		return a.taskCLI(s.Text, append(args, "--stdin")...)
	}
	if s.Notes != "" {
		args = append(args, "--notes="+s.Notes)
	}
	return a.taskCLI("", append(args, "--", s.Title)...)
}

// taskEditSpec is TaskEdit's argument: a field left out stays as it is, and
// empty notes clear them.
type taskEditSpec struct {
	Title *string `json:"title"`
	Notes *string `json:"notes"`
}

// TaskEdit is `task edit ID --title=T --notes=N`, in the = form so a value
// that starts with a dash stays a value.
func (a *App) TaskEdit(id int64, spec string) string {
	if id <= 0 {
		return taskRefusal("a task id is a positive number")
	}
	var s taskEditSpec
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		return taskRefusal("the change is not valid JSON: " + err.Error())
	}
	args := []string{"edit", strconv.FormatInt(id, 10)}
	if s.Title != nil {
		args = append(args, "--title="+*s.Title)
	}
	if s.Notes != nil {
		if len(*s.Notes) > maxArgNotes {
			return taskRefusal(notesTooLong)
		}
		args = append(args, "--notes="+*s.Notes)
	}
	if len(args) == 2 {
		return taskRefusal("nothing to change: give a title or notes")
	}
	return a.taskCLI("", args...)
}

// TaskMove is `task move ID STATUS` with a place in the column: where is ""
// (the column's default), "top", "bottom", "before" or "after", the last two
// with ref, another card of that column.
func (a *App) TaskMove(id int64, status, where string, ref int64) string {
	if id <= 0 {
		return taskRefusal("a task id is a positive number")
	}
	if !taskBoardStatuses[status] {
		return taskRefusal("unknown column " + strconv.Quote(status))
	}
	args := []string{"move", strconv.FormatInt(id, 10), status}
	switch where {
	case "":
	case "top", "bottom":
		args = append(args, "--"+where)
	case "before", "after":
		if ref <= 0 || ref == id {
			return taskRefusal(where + " needs another card of the column")
		}
		args = append(args, "--"+where, strconv.FormatInt(ref, 10))
	default:
		return taskRefusal("unknown place " + strconv.Quote(where))
	}
	return a.taskCLI("", args...)
}

// taskIDArgs renders ids as arguments; false for an empty list or an id that
// is not positive.
func taskIDArgs(ids []int64) ([]string, bool) {
	if len(ids) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, false
		}
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out, true
}

// TaskApprove is `task approve ID…`: Inbox to the bottom of Ready, or the
// top with top.
func (a *App) TaskApprove(ids []int64, top bool) string {
	args, ok := taskIDArgs(ids)
	if !ok {
		return taskRefusal("give the ids of the tasks to approve")
	}
	args = append([]string{"approve"}, args...)
	if top {
		args = append(args, "--top")
	}
	return a.taskCLI("", args...)
}

// TaskArchive is `task archive ID…`.
func (a *App) TaskArchive(ids []int64) string {
	args, ok := taskIDArgs(ids)
	if !ok {
		return taskRefusal("give the ids of the tasks to archive")
	}
	return a.taskCLI("", append([]string{"archive"}, args...)...)
}

// TaskUnarchive is `task unarchive ID…`: each back to the column it left.
func (a *App) TaskUnarchive(ids []int64) string {
	args, ok := taskIDArgs(ids)
	if !ok {
		return taskRefusal("give the ids of the tasks to restore")
	}
	return a.taskCLI("", append([]string{"unarchive"}, args...)...)
}

// TaskComment is the operator's `task comment ID -- TEXT`.
func (a *App) TaskComment(id int64, text string) string {
	if id <= 0 {
		return taskRefusal("a task id is a positive number")
	}
	if strings.TrimSpace(text) == "" {
		return taskRefusal("a comment needs text")
	}
	return a.taskCLI("", "comment", strconv.FormatInt(id, 10), "--", text)
}

// TaskAgentShell names the agent-context marker in this app's environment,
// or MONOAGENT_ACTOR, or "". The CLI the app runs inherits it and then
// refuses every operator action (spec D7, §10 Errors), so the page shows the
// board read-only and says to open MonoAgent from the Dock or Finder.
func (a *App) TaskAgentShell() string {
	if m := orgsign.AgentContextMarker(); m != "" {
		return m
	}
	if strings.TrimSpace(os.Getenv("MONOAGENT_ACTOR")) != "" {
		return "MONOAGENT_ACTOR"
	}
	return ""
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `gofmt -l wails-app/app_tasks.go wails-app/app_tasks_test.go` then `go -C wails-app vet .` then `go -C wails-app test -run 'TestTaskBindings|TestTaskAdd|TestTaskAgentShell|TestTaskBoard' -count=1 .`
Expected: `gofmt` prints nothing (if it lists a file, run `gofmt -w` on that file: hand-aligned literals and struct tags are the usual cause), vet clean, PASS (seven tests).

- [ ] **Step 5: Check that each rule is pinned**

Make each change below, run the same test command, see the named test FAIL, then undo the change:
1. `TaskAdd`: `"--", s.Title` becomes `s.Title` → `TestTaskBindingsPassTheirArgumentsExactly`.
2. `TaskEdit`: `"--title="+*s.Title` becomes two arguments `"--title", *s.Title` → the same test.
3. `TaskMove`: drop `|| ref == id` → `TestTaskBindingsRefuseBadInputWithoutRunningTheCLI`.
4. `TaskAgentShell`: drop the `MONOAGENT_ACTOR` branch and put `_ = os.Getenv` in its place (the line only keeps the `os` import used) → `TestTaskAgentShellNamesTheMarkerTheAppInherited`.
5. `boardLimit`: `n <= 0` becomes `n < 0` → `TestTaskBoardLimitIsNeverEveryDoneCard` (0 would read every Done card).
6. `TaskEdit`: drop the `maxArgNotes` check → `TestTaskBindingsRefuseBadInputWithoutRunningTheCLI`; then the same in `TaskAdd` → the same test.
7. `TaskBoard`: replace its body after the `a.db` check with `_ = tasks.NewStore` (the line only keeps the `tasks` import used) and `return a.taskCLI("", "board", "--done-limit", strconv.Itoa(boardLimit(doneLimit)))` → `TestTaskBoardIsReadInProcessEvenUnderAnInheritedMarker` (the CLI's refusal comes back instead of the board).
8. `TaskBoard`: `tasks.Actor{Kind: tasks.Human}` becomes `tasks.Actor{}` → the same test (the store refuses an actor that was never set, so the document is an error); then, separately, the actor becomes `tasks.Actor{Kind: tasks.Agent, Name: "app"}` when `a.TaskAgentShell() != ""` (the CLI's way of telling who runs a command) → the same test (an app started from an agent's shell loses its board).

- [ ] **Step 6: Commit**

```
git add wails-app/app_tasks.go wails-app/app_tasks_test.go
```
then
```
git commit -m "feat(tasks): desktop bindings for the task board, through the CLI" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

