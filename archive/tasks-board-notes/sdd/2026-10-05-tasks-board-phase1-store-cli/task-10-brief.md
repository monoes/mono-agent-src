### Task 10: The operator's commands: edit, move, approve, archive, unarchive

**Files:**
- Create: `cmd/monoagentcli/task_ops.go`
- Modify: `cmd/monoagentcli/task.go` (add the constructors to `newTaskCmd`)
- Test: `cmd/monoagentcli/task_ops_test.go`

**Interfaces:**
- Consumes: Task 8's `callerFor(...).operator(what)`, `flagAs`, `withTasks`, `taskErr`, `parseTaskID(s)`, `parseTaskIDs`, `columnLabel`; Task 9's `taskCut`; `Store.Edit`, `Move`, `Approve`, `Archive`, `ArchiveStatus`, `Unarchive`, `tasks.Placement`, `tasks.ParseStatus`.
- Produces: `newTaskEditCmd`, `newTaskMoveCmd`, `newTaskApproveCmd`, `newTaskArchiveCmd`, `newTaskUnarchiveCmd`; helpers `writeOneTask(cfg, cmd, p, t, verb) error` and `writeTasks(cfg, cmd, p, ts, verb) error`. Every command here refuses an agent-driven caller before it opens the database: exit 3, code `operator_only`.
- JSON: one task is `{"profile", "task"}`, several are `{"profile", "tasks"}`, `archive --status` is `{"profile", "archived": N}`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/monoagentcli/task_ops_test.go`:

```go
package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orgsign"
)

func id(n int64) string { return strconv.FormatInt(n, 10) }

func TestOperatorCommandsRefuseAnAgentContext(t *testing.T) {
	commands := [][]string{
		{"edit", "1", "--title", "x"},
		{"move", "1", "ready"},
		{"approve", "1"},
		{"archive", "1"},
		{"archive", "--status", "done"},
		{"unarchive", "1"},
	}
	contexts := map[string]func(t *testing.T) []string{
		"a marker":         func(t *testing.T) []string { t.Setenv("CLAUDECODE", "1"); return nil },
		"--as":             func(t *testing.T) []string { return []string{"--as", "bot"} },
		"MONOAGENT_ACTOR":  func(t *testing.T) []string { t.Setenv("MONOAGENT_ACTOR", "bot"); return nil },
	}
	for name, setup := range contexts {
		t.Run(name, func(t *testing.T) {
			db := newTaskTestDB(t)
			extra := setup(t)
			for _, c := range commands {
				doc := failedTaskJSON(t, db, "default", 3, append(append([]string{}, c...), extra...)...)
				if doc["code"] != "operator_only" {
					t.Errorf("task %s: %v, want code operator_only", strings.Join(c, " "), doc)
				}
			}
		})
	}
}

// Every marker the org-signing guard knows must trip the operator guard, not
// just CLAUDECODE: an agent that is not Claude Code is an agent all the same.
func TestEveryAgentContextMarkerRefusesOperatorCommands(t *testing.T) {
	for _, marker := range orgsign.AgentContextMarkers() {
		t.Run(marker, func(t *testing.T) {
			db := newTaskTestDB(t)
			t.Setenv(marker, "1")
			doc := failedTaskJSON(t, db, "default", 3, "approve", "1")
			if doc["code"] != "operator_only" || !strings.Contains(doc["error"].(string), marker) {
				t.Errorf("%s: %v, want operator_only naming the marker", marker, doc)
			}
		})
	}
}

func TestTheOperatorWorksTheWholeBoard(t *testing.T) {
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "write the docs")
	n := id(added.Task.ID)

	var one struct {
		Task taskJSON `json:"task"`
	}
	var many struct {
		Tasks []taskJSON `json:"tasks"`
	}
	mustTaskJSON(t, db, "default", &many, "", "approve", n)
	if len(many.Tasks) != 1 || many.Tasks[0].Status != "ready" {
		t.Fatalf("approve: %+v", many.Tasks)
	}
	mustTaskJSON(t, db, "default", &one, "", "move", n, "in_progress")
	if one.Task.Status != "in_progress" || one.Task.Claim != nil {
		t.Fatalf("move to in_progress: %+v", one.Task)
	}
	mustTaskJSON(t, db, "default", &one, "", "move", "#"+n, "done", "--top")
	if one.Task.Status != "done" {
		t.Fatalf("move to done: %+v", one.Task)
	}
	mustTaskJSON(t, db, "default", &one, "", "edit", n, "--title", "write the docs well", "--notes", "include examples")
	if one.Task.Title != "write the docs well" || one.Task.Notes != "include examples" {
		t.Fatalf("edit: %+v", one.Task)
	}
	mustTaskJSON(t, db, "default", &many, "", "archive", n)
	if many.Tasks[0].Status != "archived" {
		t.Fatalf("archive: %+v", many.Tasks)
	}
	mustTaskJSON(t, db, "default", &many, "", "unarchive", n)
	if many.Tasks[0].Status != "done" {
		t.Fatalf("unarchive restores the column: %+v", many.Tasks)
	}
	text, _, err := runTask(t, db, "default", false, "", "approve", "99999")
	if err == nil || exitCode(err) != 2 || text != "" {
		t.Errorf("approving an unknown task: exit %d, %v", exitCode(err), err)
	}
}

func TestTaskMovePlacesCardsWithBeforeAfterTopAndBottom(t *testing.T) {
	db := newTaskTestDB(t)
	var a, b, c addedJSON
	mustTaskJSON(t, db, "default", &a, "", "add", "a", "--ready")
	mustTaskJSON(t, db, "default", &b, "", "add", "b", "--ready")
	mustTaskJSON(t, db, "default", &c, "", "add", "c", "--ready")
	order := func() string {
		var l listJSON
		mustTaskJSON(t, db, "default", &l, "", "list", "--status", "ready")
		var titles []string
		for _, task := range l.Tasks {
			titles = append(titles, task.Title)
		}
		return strings.Join(titles, "")
	}
	if order() != "abc" {
		t.Fatalf("ready is a queue: %s", order())
	}
	var one struct {
		Task taskJSON `json:"task"`
	}
	mustTaskJSON(t, db, "default", &one, "", "move", id(c.Task.ID), "ready", "--top")
	if order() != "cab" {
		t.Errorf("--top: %s", order())
	}
	mustTaskJSON(t, db, "default", &one, "", "move", id(c.Task.ID), "ready", "--bottom")
	mustTaskJSON(t, db, "default", &one, "", "move", id(a.Task.ID), "ready", "--after", "#"+id(b.Task.ID))
	if order() != "bac" {
		t.Errorf("--after: %s", order())
	}
	mustTaskJSON(t, db, "default", &one, "", "move", id(c.Task.ID), "ready", "--before", id(b.Task.ID))
	if order() != "cba" {
		t.Errorf("--before: %s", order())
	}
	if doc := failedTaskJSON(t, db, "default", 3, "move", id(a.Task.ID), "ready", "--top", "--bottom"); doc["code"] != "invalid_input" {
		t.Errorf("two places: %v", doc)
	}
}

func TestTaskApproveIsAllOrNothing(t *testing.T) {
	db := newTaskTestDB(t)
	var inbox, ready addedJSON
	mustTaskJSON(t, db, "default", &inbox, "", "add", "in the inbox")
	mustTaskJSON(t, db, "default", &ready, "", "add", "already ready", "--ready")
	if doc := failedTaskJSON(t, db, "default", 3, "approve", id(inbox.Task.ID), id(ready.Task.ID)); doc["code"] != "invalid_input" {
		t.Errorf("approving a task that is not in the inbox: %v", doc)
	}
	var l listJSON
	mustTaskJSON(t, db, "default", &l, "", "list", "--status", "inbox")
	if len(l.Tasks) != 1 {
		t.Errorf("the failed approval changed the inbox: %+v", l.Tasks)
	}
}

func TestTaskArchiveByStatusAndItsRefusals(t *testing.T) {
	db := newTaskTestDB(t)
	var added addedJSON
	for _, title := range []string{"d1", "d2"} {
		mustTaskJSON(t, db, "default", &added, "", "add", title)
		var one struct {
			Task taskJSON `json:"task"`
		}
		mustTaskJSON(t, db, "default", &one, "", "move", id(added.Task.ID), "done")
	}
	if doc := failedTaskJSON(t, db, "default", 3, "archive", "1", "--status", "done"); doc["code"] != "invalid_input" {
		t.Errorf("ids and --status together: %v", doc)
	}
	var res struct {
		Archived int `json:"archived"`
	}
	mustTaskJSON(t, db, "default", &res, "", "archive", "--status", "done")
	if res.Archived != 2 {
		t.Errorf("archived %d, want 2", res.Archived)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "archive"); doc["code"] != "invalid_input" {
		t.Errorf("no ids and no --status: %v", doc)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "move", id(added.Task.ID), "archived"); doc["code"] != "invalid_input" {
		t.Errorf("move to archived: %v", doc)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "move", id(added.Task.ID), "bogus"); doc["code"] != "invalid_input" {
		t.Errorf("move to an unknown column: %v", doc)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./cmd/monoagentcli/ -run 'TestOperatorCommands|TestTheOperatorWorks|TestTaskMovePlaces|TestTaskApprove|TestTaskArchive' -count=1`
Expected: FAIL (`unknown command "edit" for "task"`).

- [ ] **Step 3: Write `cmd/monoagentcli/task_ops.go`**

```go
package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// writeOneTask prints the result of a command that changed one task.
func writeOneTask(cfg *globalConfig, cmd *cobra.Command, p tasks.Profile, t tasks.Task, verb string) error {
	if cfg.JSONOutput {
		return writeJSONTo(cmd.OutOrStdout(), map[string]any{"profile": p, "task": t})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s #%d (%s): %s\n", verb, t.ID, columnLabel(t.Status), taskCut(t.Title, 70))
	return nil
}

// writeTasks prints the result of a command that changed several tasks.
func writeTasks(cfg *globalConfig, cmd *cobra.Command, p tasks.Profile, ts []tasks.Task, verb string) error {
	if cfg.JSONOutput {
		return writeJSONTo(cmd.OutOrStdout(), map[string]any{"profile": p, "tasks": ts})
	}
	for _, t := range ts {
		fmt.Fprintf(cmd.OutOrStdout(), "%s #%d (%s): %s\n", verb, t.ID, columnLabel(t.Status), taskCut(t.Title, 70))
	}
	return nil
}

func newTaskEditCmd(cfg *globalConfig) *cobra.Command {
	var title, notes string
	cmd := &cobra.Command{
		Use:   "edit ID [--title T] [--notes TEXT]",
		Short: "Change a task's title or notes (you only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := callerFor(flagAs(cmd)).operator("edit a task")
			if err != nil {
				return err
			}
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			var e tasks.Edit
			if cmd.Flags().Changed("title") {
				e.Title = &title
			}
			if cmd.Flags().Changed("notes") {
				e.Notes = &notes
			}
			return withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.Edit(ctx, p.ID, id, e, actor)
				if err != nil {
					return taskErr(err)
				}
				return writeOneTask(cfg, cmd, p, t, "Edited")
			})
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "New title")
	cmd.Flags().StringVar(&notes, "notes", "", "New notes (an empty value clears them)")
	return cmd
}

func newTaskMoveCmd(cfg *globalConfig) *cobra.Command {
	var before, after string
	var top, bottom bool
	cmd := &cobra.Command{
		Use:   "move ID STATUS [--before ID | --after ID | --top | --bottom]",
		Short: "Move a task to a column, and to a place in it (you only)",
		Long: `STATUS is inbox, ready, in_progress, review or done (archive has its own command).
Without a place the task goes to the column's default: the top of inbox, review
and done, the bottom of ready and in_progress. The top of Ready is what an AI
agent takes next. Moving a task out of in_progress ends an agent's claim.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := callerFor(flagAs(cmd)).operator("move a task")
			if err != nil {
				return err
			}
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			to, err := tasks.ParseStatus(args[1])
			if err != nil {
				return taskErr(err)
			}
			place := tasks.Placement{Top: top, Bottom: bottom}
			if before != "" {
				if place.Before, err = parseTaskID(before); err != nil {
					return err
				}
			}
			if after != "" {
				if place.After, err = parseTaskID(after); err != nil {
					return err
				}
			}
			return withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.Move(ctx, p.ID, id, to, place, actor)
				if err != nil {
					return taskErr(err)
				}
				return writeOneTask(cfg, cmd, p, t, "Moved")
			})
		},
	}
	cmd.Flags().StringVar(&before, "before", "", "Put it just above this task of the column")
	cmd.Flags().StringVar(&after, "after", "", "Put it just below this task of the column")
	cmd.Flags().BoolVar(&top, "top", false, "Put it at the top of the column")
	cmd.Flags().BoolVar(&bottom, "bottom", false, "Put it at the bottom of the column")
	return cmd
}

func newTaskApproveCmd(cfg *globalConfig) *cobra.Command {
	var top bool
	cmd := &cobra.Command{
		Use:   "approve ID... [--top]",
		Short: "Move Inbox tasks to Ready, where an AI agent may take them (you only)",
		Long: `Approving is the step where you have read a task: agents work only what is in
Ready. The task goes to the bottom of Ready (the queue), or the top with --top.
If any task is not in the Inbox, nothing is approved.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := callerFor(flagAs(cmd)).operator("approve a task")
			if err != nil {
				return err
			}
			ids, err := parseTaskIDs(args)
			if err != nil {
				return err
			}
			return withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				ts, err := store.Approve(ctx, p.ID, ids, top, actor)
				if err != nil {
					return taskErr(err)
				}
				return writeTasks(cfg, cmd, p, ts, "Approved")
			})
		},
	}
	cmd.Flags().BoolVar(&top, "top", false, "Put the tasks at the top of Ready instead of the bottom")
	return cmd
}

func newTaskArchiveCmd(cfg *globalConfig) *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "archive ID... | --status done",
		Short: "Hide tasks from the board, keeping them (you only)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := callerFor(flagAs(cmd)).operator("archive tasks")
			if err != nil {
				return err
			}
			if status != "" {
				if len(args) > 0 {
					return errInvalidInput("give task ids or --status, not both")
				}
				st, err := tasks.ParseStatus(status)
				if err != nil {
					return taskErr(err)
				}
				return withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
					n, err := store.ArchiveStatus(ctx, p.ID, st, actor)
					if err != nil {
						return taskErr(err)
					}
					if cfg.JSONOutput {
						return writeJSONTo(cmd.OutOrStdout(), map[string]any{"profile": p, "archived": n})
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Archived %d tasks from %s.\n", n, columnLabel(st))
					return nil
				})
			}
			ids, err := parseTaskIDs(args)
			if err != nil {
				return err
			}
			return withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				ts, err := store.Archive(ctx, p.ID, ids, actor)
				if err != nil {
					return taskErr(err)
				}
				return writeTasks(cfg, cmd, p, ts, "Archived")
			})
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "Archive every task of this column (inbox, ready, in_progress, review or done)")
	return cmd
}

func newTaskUnarchiveCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "unarchive ID...",
		Short: "Bring archived tasks back to the column they were archived from (you only)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := callerFor(flagAs(cmd)).operator("unarchive a task")
			if err != nil {
				return err
			}
			ids, err := parseTaskIDs(args)
			if err != nil {
				return err
			}
			return withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				ts, err := store.Unarchive(ctx, p.ID, ids, actor)
				if err != nil {
					return taskErr(err)
				}
				return writeTasks(cfg, cmd, p, ts, "Restored")
			})
		},
	}
}
```

- [ ] **Step 4: Register the commands**

In `newTaskCmd` (`task.go`) the `cmd.AddCommand(...)` call becomes:

```go
	cmd.AddCommand(
		newTaskAddCmd(cfg),
		newTaskListCmd(cfg),
		newTaskBoardCmd(cfg),
		newTaskShowCmd(cfg),
		newTaskEditCmd(cfg),
		newTaskMoveCmd(cfg),
		newTaskApproveCmd(cfg),
		newTaskArchiveCmd(cfg),
		newTaskUnarchiveCmd(cfg),
	)
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `gofmt -l cmd/monoagentcli` then `go vet ./cmd/monoagentcli/` then `go test ./cmd/monoagentcli/ -run 'TestOperatorCommands|TestTheOperatorWorks|TestTaskMovePlaces|TestTaskApprove|TestTaskArchive|TestTask|TestAnAgentCannotUse' -count=1`
Expected: nothing from `gofmt` (if it lists `task_ops_test.go`, run `gofmt -w` on it: the aligned map literal in `contexts` is the usual cause), vet clean, PASS.

- [ ] **Step 6: Commit**

```
git add cmd/monoagentcli/task_ops.go cmd/monoagentcli/task_ops_test.go cmd/monoagentcli/task.go
```
then
```
git commit -m "feat(tasks): the operator's commands: edit, move, approve, archive, unarchive" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

