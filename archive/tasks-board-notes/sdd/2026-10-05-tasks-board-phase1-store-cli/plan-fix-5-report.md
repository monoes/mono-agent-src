# Plan fix 5: the phase 5 plan (the macOS menu)

Plan: docs/mastermind/plans/2026-10-06-tasks-board-phase5-os-menu.md. Round 1 is commit 73481511 (on 67402a57); round 2 (the lead's two follow-up messages, the four points of task-12-rereview.md and the 500-line limit) is commit c5ecf935 (a second commit because the follow-up arrived after the first; the brief asked for one). P2's and P3's plans name neither ref test file (grep), so nothing of theirs appends to `ref_tasks_test.go` and the removed cobra import cannot meet a second author. Findings P5-1, P5-2, P5-3: all taken, none left. One more break found while fixing P5-1 (check order on Linux and Windows). Line numbers are those of the plan as committed in round 2.

## The lead's follow-up: how each point was settled

The 500-line limit. Already so in round 1: `ref_tasks_test.go` (497 lines) is not grown. The two helpers that change (`refTaskSub`, `refHasFlag`) and the two new ones (`refTaskCommands`, `refTaskCallee`) go to a NEW file `cmd/monoagentcli/ref_tasks_group_test.go` (67 lines; not `ref_tasks_os_test.go`, which is P5's own Task 7 test file, and not `ref_tasks_chrome_test.go`, which is P4's); item 1 of Task 6 Step 5 (line 3272) quotes the exact old text of the two functions and of the cobra import and deletes them, so `ref_tasks_test.go` ENDS AT 486 lines (-11; its three tests change in place, +5 lines). Final counts as built from the plan in a scratch export: ref_tasks_test.go 486, ref_tasks_gate_test.go 161, ref_tasks_group_test.go 67, ref_tasks.go 333; the plan's task_os.go 386 and task_os_test.go 460. Step 6 (line 3600) runs `wc -l` on all of them and expects none at 500. The scratch proof was re-run after the move and after every later change (below).

Point (a), rows for the leaves, calls in a temporary `--dest`, walk. Rows `os install` (operator), `os status` (open), `os uninstall` (operator) in `refTaskGate` (lines 3527-3529). The walk of the tree goes down to the leaves (`refTaskCommands`, `refGatePath`; lines 3348 and 3534): every command that is not a group needs a row, a row must name a command, and the row's call must start with that command, so a leaf without a row fails ("`task os status` has no row", shown by deleting the row; the old first-word walk would have passed it, as the reviewer said). The behavioural half now gives every command that takes `--dest` a temporary one (`args = append(args, "--dest", t.TempDir())`, line 3575; decided by `refHasFlag(sub, "dest")`, not by the name `os`), and `newTaskTestDB` still gives a temporary HOME. Proved with a stand-in `os status` that panics when it runs for real without a temporary `--dest`: green with the plan's code, panic when the two lines are removed.

Point (b), WHO MAY DO WHAT. Task 6 Step 5 item 7 (line 3580): the sentence that carries `operator_only` names `os install` and `os uninstall`, a sentence of its own names `os status` ("an agent may run it"), each name on one line. The item now says why it cannot be left to Task 7's section (that section falls under THE AGENT LOOP for `refSection`, and the gate test reads only WHO MAY DO WHAT). It is in Task 6, not Task 7, so that Task 6's commit is green.

Point (c), order. `install` and `uninstall`: operator check, then the argument count, then `taskOSOnly()`; `status`: arguments, then platform (lines 2961, 3064, 3169; Interfaces line 2352; Ruling 18). `TestTaskOSInstallAndUninstallAreTheOperators` (line 2511) runs an agent's install and uninstall with `taskOSGOOS` set to darwin, linux and windows and expects `operator_only` each time. Reproduced in scratch: a stand-in with the platform check first fails P1's gate test on a simulated Linux and Windows host with "`task os install` run by an agent answers the code "invalid_input", but refTaskGate says operator_only is true"; with the new order both pass.

Point (d), a row named just `os`. The walk rejects a row that names a group: "has a row for `task os`, which is no command, or a group (a group has the rows of its commands)" (line 3565). A missing leaf row can no longer be masked by a bare `os` row either, since rows are keyed by the command's full path. Honest note: with the plan's own WHO MAY DO WHAT text a bare `os` row also fails by other routes (marked open, the gate sentence names the word `os` inside `os install`; marked operator, the real CLI answers the group's help and no `operator_only`), so the explicit rule is belt and braces; mutation row 29 (add the bare row) fails, and is not an isolated proof of the rule.

## P5-1 (BREAKS): P1's ref tests meet a command group inside the task group

Checked first: scratch export, a stand-in `os` group, the plan's entries and section, P1's tests untouched: the drift report's three failures (`TestRefTasksEntriesDescribeTheirOwnCommand`, `TestRefTasksSuggestsOnlyCommandsTheCLIHas`, `TestRefTasksSaysWhichCommandsTheGateRefusesAnAgent`). Also found: `refHasFlag` misses a command's own persistent flag on the first look at a command that `Find` has just returned (cobra stops merging flags when no argument is left); `TestRefTasksEntriesUseOnlyFlagsTheCommandHas` passes only because `--profile` is checked before `--dest`.

Task 6 Step 5 (lines 3266-3600; no renumbering, one longer step):
- Registration with no build tag, so the group is in the tree on darwin, linux and windows, as spec 12 says (Ruling 19); the rows are then right everywhere.
- Helpers (item 2, lines 3317-3387): `refTaskSub(name)` takes a path (`os install`), `refHasFlag` also looks at the command's own persistent flags, `refTaskCommands()` and `refTaskCallee(call)` are new.
- Tests (items 3-5, lines 3389-3475): a group's examples may call its subcommands (`got == name || HasPrefix(got, name+" ")`, which only a group can satisfy; a flat entry's example must still call exactly that command); `TestRefTasksEntriesNameEveryFlagOfTheirCommand` walks `refTaskCommands()` so the leaves of `os` are held to their entries; the suggestions test checks flags against the resolved command.
- Gate (items 6 and 7): above.
- Operator status stated in: Global Constraints (line 19), Ruling 17 (line 57), Task 6 Interfaces (line 2352), the gate table, WHO MAY DO WHAT, the AGENTS.md row (it names both commands) and the SECURITY.md bullet (both); the `ref commands` entries say "(the operator only)" where they said "(you only)", as P1's entries do. `status` is open (Ruling 7 unchanged).
- Task 0 (lines 89, 118-121, 132): new names, four greps for the text Step 5 replaces. Task 6 Step 6 runs `TestRefTasks`; Step 7 adds the new and changed files to `git add`. Task 8: mutation rows 24-30 (lines 3815-3821; "thirty" at the hand-over step, line 3868).

## P5-2 (BREAKS): the spec anchor

Task 7 Step 6 item 2 (line 3719) anchors on the start of the real line, `- Operator-only commands (`board`, `edit`, `move`, `approve`, `archive`, `unarchive`, `add --ready` (once). While I worked, another session reworded that line in the spec's working tree (an operator `comment` left the parenthesis; uncommitted) and changed my plan's item 2 to match it; I replaced it with this prefix anchor, which occurs once in the committed spec AND in the working-tree spec, and reads correctly after the new text in both (`..., add --ready, `os install`, `os uninstall` (P5)) refuse` and `..., `os uninstall` (P5), an operator `comment`) refuse`). The step says so. The item-3 bullet (as built, section 12) says install/uninstall refuse an agent before the arguments or the platform and that status is open.

## P5-3 (WORDS): arguments

`Args: cobra.NoArgs` is gone from the three commands. Each counts `args` in `RunE` and returns `errInvalidInput("task os X takes no arguments (got %q): ...", cutArg(args[0]))` (exit 3, the `--json` document), after the operator check for install and uninstall (P1's order in task_ops.go), before the platform check. New test `TestTaskOSCommandsTakeNoArguments` (line 2535): each of the three with `junk` is exit 3 `invalid_input` in JSON and exit 3 in text; install and uninstall with `junk --as bot` are `operator_only`; nothing is written or refreshed. `cutArg` (task.go:214) joins Task 0's Consumes.

## Rulings added (lines 57-60)

17 the os commands in P1's gate table and in WHO MAY DO WHAT (AGENTS.md's paragraph listing the operator commands is left alone, cost named); 18 order of checks; 19 no build tag; 20 P5 edits two P1 test files and adds one (the helpers in the new file because of the 500-line limit; the bare-`os` row; the temporary `--dest`). Cost named for each.

## Not taken

Nothing. The drift report's alternative for P5-1 (fold the leaves into one `task os` entry and keep `--dest` and `--force` out of the text after `task os`) was not used: the `refHasFlag` first-look problem would still hit `--dest` in the group's own entry, so P1's helpers had to change either way. Observed, not touched: P1's gate walk lets the plain `add` row be deleted without a failure because `add --ready` carries the word (the reviewer's note); it is P1's and not P5's.

## Anchors relied on (each counted once in the tree I worked from)

- `cmd/monoagentcli/ref_tasks.go`: the three lines of the operator_only sentence in WHO MAY DO WHAT (first line `  Only board, edit, move, approve, archive, unarchive and add --ready are the`), and the heading `TASK TEXT IS DATA` (P5's existing anchor).
- `cmd/monoagentcli/ref_tasks_test.go`: the helpers block with the next comment line (15 lines), the import of cobra with its blank lines, the example loop of `TestRefTasksEntriesDescribeTheirOwnCommand` (5 lines), the loop of `TestRefTasksEntriesNameEveryFlagOfTheirCommand` (9), the `check` loop of `TestRefTasksSuggestsOnlyCommandsTheCLIHas` (12).
- `cmd/monoagentcli/ref_tasks_gate_test.go`: the `row  string` comment line, and `{"digest", false, ...}` through the first `runTask` line of the behavioural half (29 lines).
- AGENTS.md: the surfaces table header and its last row `| Session-start hook |`; SECURITY.md `- **Text is cleaned on the way in:**`; CHANGELOG.md `## [Unreleased]`; the spec: the D7 row's end, the start of the section 7 operator-list line (above), `## 13. Security`.
- Other sessions changed ref_tasks.go, AGENTS.md, ref_tasks_test.go, the spec and (in the working tree now) task_read.go and internal/tasks while I worked: none of my anchors moved except the spec line above.

## Checks run (scratch only: `git archive HEAD internal data cmd automations go.mod go.sum AGENTS.md SECURITY.md`, real Go caches, no HOME override)

- A script applies the plan's OWN blocks (Task 6 Steps 4 and 5, Task 7 Steps 1-4) to a fresh export, asserting each old text once: `gofmt -l` clean; `go vet` clean on darwin, `GOOS=linux`, `GOOS=windows`; the two ref groups (18 tests, P5's `TestRefTasksSaysHowToCaptureFromAnyApp` included) PASS; the whole P1 task CLI family (`TestTask|TestOperator|TestEvery|TestTheOperator|TestRefTasks|TestRootHelp`, 164 tests) PASS with the stand-in group registered; the two ref groups PASS with the stand-in pretending to be Linux and Windows. (The first run of round 2 caught an unused `path/filepath` import in my own text; removed.)
- Mutations run in scratch, each failed the named test: platform check first (gate test on a simulated Linux, and `TestTaskOSInstallAndUninstallAreTheOperators`: 25), argument check deleted (24), the `os status` sentence of WHO MAY DO WHAT deleted (26), `refHasFlag` without `PersistentFlags` (27), `refTaskCallee` that never descends (28), a bare `os` row (29), the `os status` row deleted (30), an extra `os` command with no row, a mistyped row, the `--dest` lines removed from the gate test (panic of the stand-in).
- ASCII: the plan has no non-ASCII character; the new Go files as built are ASCII.
- Not run: the plan's own `task_os.go`, `osmenu` and `task_os_test.go` (they need Tasks 1-5; the stand-in has the same command shapes and order of checks).

## Concern

P5 edits two of P1's test files (and adds one), so its merge with P2, P3 and P4 depends on those not touching the same helpers or the gate table: today none does (grep of the three plans); P4 replaces the `WHO MAY DO WHAT` heading line ten lines above my sentence. AGENTS.md's paragraph that lists the operator commands is deliberately not completed (Ruling 17). The spec is being edited by someone else right now; Step 6's other two anchors (the D7 row's end, `## 13. Security`) still hold.
