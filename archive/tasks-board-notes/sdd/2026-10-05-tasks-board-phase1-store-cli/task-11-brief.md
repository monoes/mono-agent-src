### Task 11: The agent's commands: next, claim, comment, finish, release, digest

**Files:**
- Create: `cmd/monoagentcli/task_agent.go`
- Modify: `cmd/monoagentcli/task.go` (add the constructors to `newTaskCmd`)
- Test: `cmd/monoagentcli/task_agent_test.go`

**Interfaces:**
- Consumes: Task 8's `callerFor`, `taskCaller.agent()`, `flagAs`, `withTasks`, `taskErr`, `parseTaskID`, `columnLabel`; Task 9's `printNotes(w, notes)` (the one way to print a task's notes: the untrusted notice, the notes indented, an end line), `taskCut`, `heldNote`, `leaseEnd`, `taskCommand(p, as, words)`, `untrustedNotice` (`printTask` now takes `(w, p, as, t, events)`: Task 11 does not call it); Task 10's `writeOneTask`; `Store.Next`, `Claim`, `Comment`, `Finish`, `Release`, `Counts`; `tasks.Outcome`.
- Produces: `newTaskNextCmd`, `newTaskClaimCmd`, `newTaskCommentCmd`, `newTaskFinishCmd`, `newTaskReleaseCmd`, `newTaskDigestCmd`; the printers `printNext`, `printClaimed`, `continueHelp`.
- Behaviour: `next` without `--claim` only looks and works for anyone; `next --claim`, `claim`, `finish`, `release` need an agent with a name (`--as`, or `MONOAGENT_ACTOR`); `comment` is an agent's when the caller is an agent (needs the name) and the operator's otherwise. Every command an agent is told to run next carries `--profile ID`, because the active profile can change under a running session. `digest` prints nothing when the profile has no Ready task and always exits 0.
- JSON: `next` and `claim` are `{"profile", "task"}` (`task` is `null` when there is nothing to do); `digest` is `{"profile", "ready", "next": {"id","title"} or null}`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/monoagentcli/task_agent_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

type nextJSON struct {
	Task *taskJSON `json:"task"`
}

func TestAnAgentWorksATaskFromStartToFinish(t *testing.T) {
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "fix the flaky test", "--ready")
	n := id(added.Task.ID)

	var peek nextJSON
	mustTaskJSON(t, db, "default", &peek, "", "next", "--as", "bot-1")
	if peek.Task == nil || peek.Task.ID != added.Task.ID || peek.Task.Status != "ready" || peek.Task.Claim != nil {
		t.Fatalf("peek: %+v", peek.Task)
	}
	var claimed nextJSON
	mustTaskJSON(t, db, "default", &claimed, "", "next", "--claim", "--as", "bot-1")
	if claimed.Task == nil || claimed.Task.Status != "in_progress" || claimed.Task.Claim == nil || claimed.Task.Claim.By != "bot-1" {
		t.Fatalf("claim: %+v", claimed.Task)
	}
	doc := failedTaskJSON(t, db, "default", 3, "claim", n, "--as", "bot-2")
	if doc["code"] != "claimed" || doc["claimed_by"] != "bot-1" || doc["claimed_until"] == nil {
		t.Errorf("a task another agent holds: %v", doc)
	}

	var one struct {
		Task taskJSON `json:"task"`
	}
	mustTaskJSON(t, db, "default", &one, "", "comment", n, "reproduced it", "locally", "--as", "bot-1")
	if one.Task.ID != added.Task.ID {
		t.Errorf("comment: %+v", one.Task)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "comment", n, "mine now", "--as", "bot-2"); doc["code"] != "not_claimant" {
		t.Errorf("a comment by another agent: %v", doc)
	}
	mustTaskJSON(t, db, "default", &one, "", "finish", n, "--as", "bot-1", "--result", "fixed in PR 41")
	if one.Task.Status != "review" || one.Task.Claim != nil {
		t.Errorf("finish: %+v", one.Task)
	}
	var none nextJSON
	mustTaskJSON(t, db, "default", &none, "", "next", "--claim", "--as", "bot-1")
	if none.Task != nil {
		t.Errorf("nothing is left to claim: %+v", none.Task)
	}
	out, _, err := runTask(t, db, "default", true, "", "next", "--as", "bot-1")
	if err != nil || !strings.Contains(out, `"task": null`) {
		t.Errorf("next with nothing to do must say so with a null task: %q, %v", out, err)
	}
}

func TestFinishAndReleaseHandTheTaskBack(t *testing.T) {
	db := newTaskTestDB(t)
	var a, b addedJSON
	mustTaskJSON(t, db, "default", &a, "", "add", "needs an answer", "--ready")
	mustTaskJSON(t, db, "default", &b, "", "add", "needs the vpn", "--ready")
	var claimed nextJSON
	mustTaskJSON(t, db, "default", &claimed, "", "claim", id(a.Task.ID), "--as", "bot")
	mustTaskJSON(t, db, "default", &claimed, "", "claim", id(b.Task.ID), "--as", "bot")

	if doc := failedTaskJSON(t, db, "default", 3, "finish", id(a.Task.ID), "--as", "bot"); doc["code"] != "invalid_input" {
		t.Errorf("finish with neither a result nor a question: %v", doc)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "finish", id(a.Task.ID), "--as", "bot", "--result", "r", "--question", "q"); doc["code"] != "invalid_input" {
		t.Errorf("finish with both: %v", doc)
	}
	var one struct {
		Task taskJSON `json:"task"`
	}
	mustTaskJSON(t, db, "default", &one, "", "finish", id(a.Task.ID), "--as", "bot", "--question", "which database?")
	if one.Task.Status != "review" {
		t.Errorf("a question goes to review: %+v", one.Task)
	}
	var shown struct {
		Events []struct {
			Kind string `json:"kind"`
			Note string `json:"note"`
		} `json:"events"`
	}
	mustTaskJSON(t, db, "default", &shown, "", "show", id(a.Task.ID))
	if last := shown.Events[len(shown.Events)-1]; last.Kind != "question" || last.Note != "which database?" {
		t.Errorf("last event: %+v", last)
	}
	mustTaskJSON(t, db, "default", &one, "", "release", id(b.Task.ID), "--as", "bot", "--note", "needs the VPN")
	if one.Task.Status != "ready" || one.Task.Claim != nil {
		t.Errorf("release: %+v", one.Task)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "release", id(b.Task.ID), "--as", "bot"); doc["code"] != "not_claimant" {
		t.Errorf("releasing a task one does not hold: %v", doc)
	}
}

func TestAgentCommandsNeedAName(t *testing.T) {
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "go", "--ready")
	n := id(added.Task.ID)
	for _, args := range [][]string{
		{"next", "--claim"},
		{"claim", n},
		{"finish", n, "--result", "r"},
		{"release", n},
	} {
		doc := failedTaskJSON(t, db, "default", 3, args...)
		if doc["code"] != "invalid_input" || !strings.Contains(doc["error"].(string), "--as") {
			t.Errorf("task %s without a name: %v", strings.Join(args, " "), doc)
		}
	}
	t.Setenv("CLAUDECODE", "1") // an agent context with no name is no better
	doc := failedTaskJSON(t, db, "default", 3, "claim", n)
	if !strings.Contains(doc["error"].(string), "CLAUDECODE") {
		t.Errorf("the refusal should say why it thinks an agent is running: %v", doc)
	}
	t.Setenv("MONOAGENT_ACTOR", "bot") // the name from the environment is enough
	var claimed nextJSON
	mustTaskJSON(t, db, "default", &claimed, "", "claim", n)
	if claimed.Task == nil || claimed.Task.Claim == nil || claimed.Task.Claim.By != "bot" {
		t.Errorf("claim under MONOAGENT_ACTOR: %+v", claimed.Task)
	}
}

func TestTheOperatorsCommentIsNotAnAgentsComment(t *testing.T) {
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "something")
	var one struct {
		Task taskJSON `json:"task"`
	}
	mustTaskJSON(t, db, "default", &one, "", "comment", id(added.Task.ID), "I will look at this tomorrow")
	var shown struct {
		Events []struct {
			Actor string `json:"actor"`
			Kind  string `json:"kind"`
		} `json:"events"`
	}
	mustTaskJSON(t, db, "default", &shown, "", "show", id(added.Task.ID))
	if last := shown.Events[len(shown.Events)-1]; last.Kind != "comment" || last.Actor != "you" {
		t.Errorf("last event: %+v", last)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "comment", id(added.Task.ID), "from an agent", "--as", "bot"); doc["code"] != "not_claimant" {
		t.Errorf("an agent cannot comment on a task it does not hold: %v", doc)
	}
}

func TestNextTellsAnAgentWhatToDoAndThatTheTextIsData(t *testing.T) {
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "Fix the flaky test\nthe CI job is red on main", "add", "--stdin", "--source", "os")
	var moved struct {
		Tasks []taskJSON `json:"tasks"`
	}
	mustTaskJSON(t, db, "default", &moved, "", "approve", id(added.Task.ID))

	peek, _, err := runTask(t, db, "default", false, "", "next", "--as", "bot")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Next task: #", "Fix the flaky test", "untrusted", "--profile default", "next --claim --as"} {
		if !strings.Contains(peek, want) {
			t.Errorf("next lacks %q:\n%s", want, peek)
		}
	}
	claimed, _, err := runTask(t, db, "default", false, "", "next", "--claim", "--as", "bot")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Claimed #", "untrusted", "comment " + id(added.Task.ID) + " --as bot", "finish " + id(added.Task.ID) + " --as bot --result", "--question", "release", "--profile default"} {
		if !strings.Contains(claimed, want) {
			t.Errorf("next --claim lacks %q:\n%s", want, claimed)
		}
	}
}

func TestDigestIsSilentWithoutReadyTasksAndNeverFails(t *testing.T) {
	db := newTaskTestDB(t)
	out, errOut, err := runTask(t, db, "default", false, "", "digest")
	if err != nil || out != "" || errOut != "" {
		t.Fatalf("an empty board: stdout %q stderr %q err %v", out, errOut, err)
	}
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "waiting", "--ready")
	out, _, err = runTask(t, db, "default", false, "", "digest")
	if err != nil || !strings.Contains(out, "1 ready") || !strings.Contains(out, "Next: #") || !strings.Contains(out, "--profile default task next --claim") {
		t.Errorf("digest: %q, %v", out, err)
	}
	var doc struct {
		Ready int `json:"ready"`
		Next  *struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"next"`
	}
	mustTaskJSON(t, db, "default", &doc, "", "digest")
	if doc.Ready != 1 || doc.Next == nil || doc.Next.Title != "waiting" {
		t.Errorf("digest as JSON: %+v", doc)
	}
	if _, _, err := runTask(t, db, "no-such-profile", false, "", "digest"); err != nil {
		t.Errorf("digest must exit 0 whatever happens, so a hook can call it: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./cmd/monoagentcli/ -run 'TestAnAgentWorks|TestFinishAndRelease|TestAgentCommandsNeed|TestTheOperatorsComment|TestNextTells|TestDigest' -count=1`
Expected: FAIL (`unknown command "next" for "task"`).

- [ ] **Step 3: Write `cmd/monoagentcli/task_agent.go`**

```go
package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// continueHelp tells an agent how to carry on with a task it holds. Every
// command names the profile: the active profile can change under a session.
func continueHelp(w io.Writer, p tasks.Profile, t tasks.Task, name string) {
	cli := "monoagentcli --profile " + p.ID + " task"
	fmt.Fprintf(w, "\nWork it, then hand it back. Use the same name (%s) for every call:\n", name)
	fmt.Fprintf(w, "  report progress   %s comment %d --as %s \"what you did\"\n", cli, t.ID, name)
	fmt.Fprintf(w, "  done              %s finish %d --as %s --result \"what you did\"\n", cli, t.ID, name)
	fmt.Fprintf(w, "  need an answer    %s finish %d --as %s --question \"what you need to know\"\n", cli, t.ID, name)
	fmt.Fprintf(w, "  give it back      %s release %d --as %s --note \"why\"\n", cli, t.ID, name)
}

// printClaimed prints a task an agent has just claimed.
func printClaimed(w io.Writer, p tasks.Profile, t tasks.Task, name string) {
	fmt.Fprintf(w, "Claimed #%d (%s): %s\n", t.ID, heldNote(t), t.Title)
	if t.Source.URL != "" {
		fmt.Fprintf(w, "Link: %s\n", t.Source.URL)
	}
	printNotes(w, t.Notes) // Task 9's helper: the notice, the notes indented, an end line
	continueHelp(w, p, t, name)
}

// printNext prints the task an agent would take, and how to take it.
func printNext(w io.Writer, p tasks.Profile, t tasks.Task) {
	fmt.Fprintf(w, "Next task: #%d %s   [%s, from %s]\n", t.ID, t.Title, t.Status, t.Source.Kind)
	if t.Source.URL != "" {
		fmt.Fprintf(w, "Link: %s\n", t.Source.URL)
	}
	printNotes(w, t.Notes) // Task 9's helper: the notice, the notes indented, an end line
	cli := "monoagentcli --profile " + p.ID + " task"
	fmt.Fprintf(w, "\nTake it:\n  %s next --claim --as <your-name>\n  %s claim %d --as <your-name>\n", cli, cli, t.ID)
}

func newTaskNextCmd(cfg *globalConfig) *cobra.Command {
	var claim bool
	var lease time.Duration
	cmd := &cobra.Command{
		Use:   "next [--claim --as NAME [--lease 30m]]",
		Short: "Show the task an AI agent should work next, or claim it",
		Long: `The task is the top of Ready (an approved task), else a claim whose lease has run
out. Without --claim it only looks: two agents that look may see the same task.
With --claim and --as NAME it takes the task for you in one step, so two agents
never get the same one. A claim lasts 30 minutes by default (--lease, at most
24h) and is renewed by your comments. Nothing ready: the task is null.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			caller := callerFor(flagAs(cmd))
			actor := caller.actor
			if claim {
				var err error
				if actor, err = caller.agent(); err != nil {
					return err
				}
			}
			return withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.Next(ctx, p.ID, actor, claim, lease)
				if err != nil {
					return taskErr(err)
				}
				if cfg.JSONOutput {
					return writeJSONTo(cmd.OutOrStdout(), map[string]any{"profile": p, "task": t})
				}
				w := cmd.OutOrStdout()
				fmt.Fprintf(w, "Profile: %s\n", p.Name)
				switch {
				case t == nil:
					fmt.Fprintln(w, "Nothing is ready.")
				case claim:
					printClaimed(w, p, *t, actor.Name)
				default:
					printNext(w, p, *t)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&claim, "claim", false, "Take the task for yourself (needs --as NAME)")
	cmd.Flags().DurationVar(&lease, "lease", 0, "How long you hold it, renewed by your comments (default 30m, at most 24h)")
	return cmd
}

func newTaskClaimCmd(cfg *globalConfig) *cobra.Command {
	var lease time.Duration
	cmd := &cobra.Command{
		Use:   "claim ID --as NAME [--lease 30m]",
		Short: "Take a ready task (an AI agent), or renew your hold on one",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := callerFor(flagAs(cmd)).agent()
			if err != nil {
				return err
			}
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			return withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.Claim(ctx, p.ID, id, actor, lease)
				if err != nil {
					return taskErr(err)
				}
				if cfg.JSONOutput {
					return writeJSONTo(cmd.OutOrStdout(), map[string]any{"profile": p, "task": t})
				}
				printClaimed(cmd.OutOrStdout(), p, t, actor.Name)
				return nil
			})
		},
	}
	cmd.Flags().DurationVar(&lease, "lease", 0, "How long you hold it (default 30m, at most 24h)")
	return cmd
}

func newTaskCommentCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "comment ID TEXT... [--as NAME]",
		Short: "Add a note to a task: your own, or an AI agent's progress report (renews its lease)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			caller := callerFor(flagAs(cmd))
			actor := caller.actor
			if caller.isAgent() {
				var err error
				if actor, err = caller.agent(); err != nil {
					return err
				}
			}
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			text := strings.Join(args[1:], " ")
			return withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.Comment(ctx, p.ID, id, text, actor)
				if err != nil {
					return taskErr(err)
				}
				return writeOneTask(cfg, cmd, p, t, "Noted")
			})
		},
	}
}

func newTaskFinishCmd(cfg *globalConfig) *cobra.Command {
	var result, question string
	cmd := &cobra.Command{
		Use:   "finish ID --as NAME (--result TEXT | --question TEXT)",
		Short: "Hand a task you hold back for review, with a result or a question",
		Long: `The task goes to Review for you to read. Give exactly one of --result (what you
did) or --question (what you need to know before you can go on).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := callerFor(flagAs(cmd)).agent()
			if err != nil {
				return err
			}
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			return withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.Finish(ctx, p.ID, id, tasks.Outcome{Result: result, Question: question}, actor)
				if err != nil {
					return taskErr(err)
				}
				return writeOneTask(cfg, cmd, p, t, "Handed back")
			})
		},
	}
	cmd.Flags().StringVar(&result, "result", "", "What you did")
	cmd.Flags().StringVar(&question, "question", "", "What you need to know before you can go on")
	return cmd
}

func newTaskReleaseCmd(cfg *globalConfig) *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   "release ID --as NAME [--note TEXT]",
		Short: "Give a task you hold back to Ready, behind the others",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := callerFor(flagAs(cmd)).agent()
			if err != nil {
				return err
			}
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			return withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.Release(ctx, p.ID, id, note, actor)
				if err != nil {
					return taskErr(err)
				}
				return writeOneTask(cfg, cmd, p, t, "Released")
			})
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "Why you are giving it back")
	return cmd
}

func newTaskDigestCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "digest",
		Short: "A short line about the ready tasks, for a session-start hook (prints nothing when there are none)",
		Long: `Prints nothing when the profile has no ready task, otherwise two short lines: the
counts, the next task and the command to take it. It always exits 0, so a hook
can call it: monoagentcli --profile <id> task digest`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := withTasks(cfg, cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				c, err := store.Counts(ctx, p.ID)
				if err != nil {
					return err
				}
				var next *tasks.Task
				if c.Ready > 0 {
					if next, err = store.Next(ctx, p.ID, tasks.Actor{Kind: tasks.Agent}, false, 0); err != nil {
						return err
					}
				}
				if cfg.JSONOutput {
					var n any
					if next != nil {
						n = map[string]any{"id": next.ID, "title": next.Title}
					}
					return writeJSONTo(cmd.OutOrStdout(), map[string]any{"profile": p, "ready": c.Ready, "next": n})
				}
				if next == nil {
					return nil
				}
				w := cmd.OutOrStdout()
				fmt.Fprintf(w, "MonoAgent task board (%s): %d ready, %d in progress, %d to review. Next: #%d %s\n",
					p.Name, c.Ready, c.InProgress, c.Review, next.ID, taskCut(next.Title, 80))
				fmt.Fprintf(w, "Take it with: monoagentcli --profile %s task next --claim --as <your-name>\n", p.ID)
				return nil
			})
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "task digest: %v\n", err)
			}
			return nil
		},
	}
}
```

- [ ] **Step 4: Register the commands**

In `newTaskCmd` (`task.go`) the `cmd.AddCommand(...)` call becomes the final list:

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
		newTaskNextCmd(cfg),
		newTaskClaimCmd(cfg),
		newTaskCommentCmd(cfg),
		newTaskFinishCmd(cfg),
		newTaskReleaseCmd(cfg),
		newTaskDigestCmd(cfg),
	)
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `gofmt -l cmd/monoagentcli` then `go vet ./cmd/monoagentcli/` then `go test ./cmd/monoagentcli/ -run 'TestTask|TestAnAgent|TestFinishAndRelease|TestAgentCommands|TestTheOperator|TestNextTells|TestDigest|TestOperatorCommands' -count=1`
Expected: nothing from `gofmt`, vet clean, PASS.

- [ ] **Step 6: Commit**

```
git add cmd/monoagentcli/task_agent.go cmd/monoagentcli/task_agent_test.go cmd/monoagentcli/task.go
```
then
```
git commit -m "feat(tasks): the agent's commands: next, claim, comment, finish, release, digest" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

