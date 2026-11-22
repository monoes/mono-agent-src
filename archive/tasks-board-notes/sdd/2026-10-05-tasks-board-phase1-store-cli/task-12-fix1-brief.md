# Task 12, fix round 1

You are the Task 12 implementer, resumed. The task reviewer's report is `task-12-review.md` in this directory (read it: every finding has file:line, the code truth and a suggested fix). The current tree is HEAD 08feab19 (it contains the Task 11 fix merge 152b91f4). The controller's rulings on the findings follow. Take ALL of them unless an item says otherwise.

## Rules for this round

- New commits only. Never amend, rebase, reset or otherwise rewrite history.
- Stage and commit with explicit pathspecs in ONE form that cannot pick up anything staged by someone else: `git commit -m "..." -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>" -- <path> <path>` (after `git add <path>` for any NEW file). Other sessions share this checkout; never `git add -A` or `git commit -a`.
- Mutation checks only in a scratch export (`git archive HEAD internal data cmd go.mod go.sum | tar -x -C <new folder under the scratchpad>`), never in the tree.
- Keep every file under 500 lines. `cmd/monoagentcli/ref_tasks_test.go` is 477 lines now: the rewritten gate test goes into a NEW file `cmd/monoagentcli/ref_tasks_gate_test.go` and the old one is removed from `ref_tasks_test.go`.
- The six `refTasksText` headings stay verbatim, each alone on a line at column 0, once, in the same order (later phases edit at those anchors).
- Do not touch the spec file (the controller edits it). Do not dispatch subagents.
- Text must stay true to the CODE (the reviewer checked against 152b91f4). If you find that a statement below is not what the code does, say so in your report and write what the code does.

## The items

**I1 (Important 1): `digest` and the gate text; the gate test.**
- `ref_tasks.go` (WHO MAY DO WHAT, around :193-202) and `AGENTS.md` (around :1220-1221): name `digest` among the commands an agent may run (it has no gate: it runs the same in any context; a session-start hook calls it). Replace "Anything else is the operator's" by an explicit list: `board`, `edit`, `move`, `approve`, `archive`, `unarchive` and `add --ready` are the operator's and answer an agent with exit 3 and the code `operator_only`.
- Rewrite `TestRefTasksSaysWhichCommandsTheGateRefusesAnAgent` in the new file `ref_tasks_gate_test.go`: (a) walk the real command tree (`newTaskCmd(...).Commands()`, constructed the way the other tests in the package do) and classify every subcommand through ONE explicit table (operator-only, agent-ok; `add` is agent-ok and `add --ready` is its own operator-only row); the test FAILS for a subcommand missing from the table and for a table row that names no subcommand, with a message that says which table to edit; (b) keep the behavioural half: run each command as an agent through the real CLI and check that it answers `operator_only` exactly for the operator-only rows; (c) textual half, robust to later phases: in the WHO MAY DO WHAT section find the sentence(s) that contain the code `operator_only`; the set of command names in them (word-bounded; `add --ready` through the word `--ready`) must equal the operator-only set exactly; and every agent-ok command must be named somewhere in the section outside those sentences. Drop the "lead phrase" coupling ("The operator may", "An AI agent may") and the first-period parse: a later phase that appends sentences or rewords other sentences of the section must not need to touch this test.
- Prove it by mutation in a scratch export: (i) deleting `digest` from the section text fails the test; (ii) naming `board` in the agent's list fails it; (iii) adding a fake subcommand with no row fails it; (iv) making `digest` operator-only in code fails the behavioural half.

**I2 (Important 2): comment and the lease.** `ref_tasks.go` (around :110 and :250-251) and `AGENTS.md` (around :1235-1236): "every comment renews the lease" is true only for the default lease. A comment sets the end of the claim to the later of its current value and 30 minutes from the comment: it never shortens a lease and does not add the `--lease` that was asked for. Write that (for example: a comment extends the lease to 30 minutes from the comment if that is later, and never shortens it; with a long `--lease`, comment before its last 30 minutes, or run `claim ID --lease` again). Sharpen the limits test (around `ref_tasks_test.go:261`) so it fails when the text claims that every comment renews the lease, by asserting the sentence about the 30 minutes of a comment and "never shortens".

**M2 (the refusal text).** `cmd/monoagentcli/task.go:192` (`operatorOnlyError`): drop " or in the app": there is no board in the app until phase 3, so the text must say only "run it in your own terminal". The tests pin only "your own terminal"; run them to see.

**M3 (archive Use string).** `cmd/monoagentcli/task_ops.go:181`: `Use: "archive ID... | --status STATUS"` (any of the five board columns; `archived` is refused). Check the command's own help text and the `cliDocs` entry agree.

**M4 ("open tasks").** `ref_tasks.go:230`, `AGENTS.md:1238`, `SECURITY.md:465`: the store's own message says "open tasks" (internal/tasks/store.go:346), so keep that term but make it unambiguous: "2,000 open tasks (every task that is not archived, Done ones included: archive some to make room)". Keep the test pattern (`([\d,]+) open tasks`) working.

**M5.** `ref_tasks.go:41`: "a list that is cut says how many more there are" gets "(in text)": `list --json` returns the cut list with no count.

**M6 (AGENTS.md rows).** Add a `ref tasks` row to the "Start here" table (AGENTS.md around :53-64) in the style of its other rows. Do NOT add `ref org` (not ours). Add `MONOAGENT_ACTOR` to the "Runtime environment variables" table (AGENTS.md around :2261 on), in that table's style: the agent's name for the task board's agent commands when `--as` is not given; setting it makes the caller an agent.

**M7.** Run the `refStates` helper (`ref_tasks_test.go` around :228-243) over the AGENTS.md "Task board" section too, so its copies of the numbers cannot drift, if that is a small change; if it is not, skip it and say so.

**M8 (`digest --json` on a failure).** The stray-argument branch and the `withTasks`-error branch of `digest` write one line to standard error and return nil (`task_agent.go:291-294`, `:322-325`): nothing on standard output, exit 0. So: `ref_tasks.go:151` ("with --json it always prints ...") says "on success"; add beside `:260-261` that a digest that fails prints one line on standard error, nothing on standard output, and still exits 0; the sentences at `:279` ("Every command ... prints one document") and `:288` ("An error is {"error","code"} on standard output") except `digest`. The command's own help (`task_agent.go:283-285`, "With --json it always prints its document") is wrong the same way: change that one help sentence to say "on success" (a one-line wording fix in a file outside this task's diff; run the Task 8-11 selector after).

**M9 (hints).** `ref_tasks.go:265-266`: write what is true: every suggested command carries `--profile ID` and `--as NAME` when the caller has a name; an agent identified only by an environment marker gets no `--as` in `list` and `show` hints; where the reader must choose a name the hint writes `<your-name>` (check `task_read.go:71-80` and the next/claim printers).

**Nit.** `AGENTS.md:1241-1243`: the ragged wrap. Leave the Spanish sentence alone.

## Verify, commit, report

Run: `gofmt -l cmd/monoagentcli`, `go vet ./cmd/monoagentcli/`, and `go test ./cmd/monoagentcli/ -run '^(TestEveryTaskCommand|TestRefTasks|TestRootHelp|TestTask|TestAnAgent|TestOperator|TestTheOperator|TestEveryAgentContextMarker|TestFinishAndRelease|TestAgentCommandsNeed|TestNextTells|TestDigest)' -count=1 -timeout 15m` (all pass). Commits, with conventional messages: one `docs(tasks): ...` for the wording (ref_tasks.go, AGENTS.md, SECURITY.md, task.go, task_ops.go, task_agent.go help) and one `test(tasks): ...` for the gate test and the sharpened limit/AGENTS tests. Append a "Fix round 1" section to `task-12-report.md` (what each item became, the mutants and their results, anything you did not take and why). Return only: status, commit SHAs, one-line test summary, concerns.
