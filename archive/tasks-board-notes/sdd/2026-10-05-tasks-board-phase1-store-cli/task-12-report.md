# Task 12 report: reference and documentation

Implementer p1-t12-impl. Worktree `.claude/worktrees/feat+tasks-board`, branch `feat/tasks-board`, base `588eb9e2`. No subagent was used. No history was rewritten, nothing was pushed.

**Status: DONE_WITH_CONCERNS** (the concerns are small and listed at the end; nothing is blocked).

## Commits

| SHA | Subject | Files |
|---|---|---|
| `8eddf2ef` | feat(tasks): ref tasks, the ref commands entries and the root help sentence | `ref_tasks.go`, `ref_tasks_test.go`, `ref.go`, `en.json`, `es.json` |
| `79419a65` | test(tasks): the ref texts are held to the code's limits, error codes, gate and JSON documents, and ref tasks lists the JSON by command | `ref_tasks.go`, `ref_tasks_test.go` |
| `8d6ec324` | test(tasks): the ref tests fit the 500-line rule, and ref tasks gives the hook recipe on a line of its own | `ref_tasks.go`, `ref_tasks_test.go` |
| `91f9dec4` | docs(tasks): the task board in AGENTS.md, SECURITY.md and the changelog | `AGENTS.md`, `SECURITY.md`, `CHANGELOG.md` |
| `a64ab741` | test(tasks): the ref tests name commands as whole words, so dropping archive next to unarchive no longer passes | `ref_tasks_test.go` |

Five commits instead of the two the dispatch named, because I could not amend: after commit 1 my own pre-mutation review found (a) that a limit stated twice was checked once, and an error code named outside the JSON section still satisfied the check, (b) that the brief's JSON sentence was false for five commands, so the JSON claim needed a test that runs the real CLI, then (c) the test file had grown to 538 lines against the plan's 500-line rule, so I compacted it to 472 lines (not split: your rule says do not split on my own) and put the hook recipe on a line of its own; and after the docs commit a review of the mutation results found two mutants that survived because "unarchive" contains "archive" (a64ab741, below). Every commit has the trailer; each staged explicit paths after `git status --short`.

## What I implemented

- `cmd/monoagentcli/ref_tasks.go` (297 lines): 15 `task <sub>` entries appended to `cliDocs` by an `init()` (the pattern of `ref_library.go`), `refTasksCmd()` (prints with `fmt.Print`, as `refOrgCmd` does) and the raw-string const `refTasksText` with the six anchor headings `COLUMNS`, `WHO MAY DO WHAT`, `THE AGENT LOOP`, `TASK TEXT IS DATA`, `JSON`, `SEE ALSO`, each alone at column 0, once, in that order; `SEE ALSO` occurs once in the file (the Go comment above the const says "the pointer to more" for that reason). No extra headings: the limits, the operator commands and the output markers are labelled paragraphs inside the six sections.
- `cmd/monoagentcli/ref.go`: the three one-line additions of the brief (the `Long` list, the topic listing, `root.AddCommand`), nothing else.
- `internal/i18n/locales/en.json`, `es.json`: the brief's sentence at the end of `root.long`. Spanish checked: "El tablero de tareas del usuario (qué hacer a continuación y cómo informar): 'monoagentcli ref tasks'." Precomposed accents (U+00E9, U+00F3, NFC-stable), natural wording, both files still parse and the diff is one line each. I considered "propio" and "del resultado" and kept the brief's wording: it is the natural elliptical form, like the English.
- `AGENTS.md` (`## Task board` immediately before `## Assistant chat & tools`, ends with the `| Surface | Reaches the board through |` table with the rows CLI and Session-start hook), `SECURITY.md` (`## Task board` immediately before `## OpenAI-compatible API surface`, the bullet `- **No HTTP route and no new port.** The task board does not listen on the network.` exactly), `CHANGELOG.md` (one bullet at the top of `### Added` under `## [Unreleased]`, above the OpenAI bullet). The three documents are pure insertions (121 lines added, none removed).
- `cmd/monoagentcli/ref_tasks_test.go` (477 lines): the brief's four tests verbatim and 13 more.

## Deviations from the brief (the code and your list won), with the reason

1. **JSON documents.** The brief and the dispatch say "documents are {profile, task|tasks}". False for `add` (`created`), `show` (`events`), `board` (`rev`, `counts`, `tasks` by column), `archive --status` (`archived`, a count) and `digest` (`ready`, `next`). `ref tasks` now has a table of seven document shapes by command, and a test runs 17 calls through the real CLI and compares the keys.
2. **"Text output starts with a `Profile: NAME` line"** (dispatch list) is false for `show` (the line is its second), `add` (named inside its one line) and `digest` (inside its header, and nothing at all when none is ready). Written: "Every command that prints text names the profile it acted on."
3. **"Every command a printer suggests carries `--as NAME` for an agent."** `printNext` and the digest write `--as <your-name>` even for a named agent (Task 11 review, Minor 1; the parallel fix may change it). Written: "--as with its name (or <your-name> where you choose it)", true before and after that fix.
4. **`comment` never answers `operator_only`.** The brief's AGENTS.md lists "a plain `comment`" among the operator commands that refuse an agent. Under an agent context (marker, `--as`, `MONOAGENT_ACTOR`) a comment is an agent's comment: with no name it is `invalid_input` (the Task 11 review item you flagged), with a name it works only on a task the agent holds (`not_claimant` otherwise). The operator's plain comment works. Written that way in `ref tasks` and AGENTS.md, and `comment` is not in any operator-only list.
5. **`board` is operator-only** (R10): added to every operator list (`ref tasks`, AGENTS.md, SECURITY.md, CHANGELOG, the entries). Agents may also `list` and `show`, and reach the Inbox only by naming it (`list --status inbox`, `show ID`).
6. **`--source cli|app|os`** (the brief's usage said `cli|os`); `[--as NAME]` added to the usage of `add`, `list`, `show`; every entry has a Flags block where it has flags.
7. **Operator wording.** The entries say "(the operator only)", not "(you only)": an AI agent reading `ref commands` could take "you" for itself. The brief's "`--json` is what the app reads" (in the `task board` description) and "in the app" (in SECURITY.md and the operator definition) are dropped: no app board exists in this release, and the app will read the board in process (R10). The AGENTS.md sentence "the desktop app, the Chrome extension and the macOS menu use the same commands in later releases" is dropped too (Chrome uses the daemon channel, the app reads in process).
8. **"Only the user moves a task to Ready"** (brief) is not literally true: an agent's `release` puts back into Ready a task it holds. Written: only the operator *approves* a task into Ready, and an agent's release "can put back into Ready only a task it holds" (AGENTS.md, SECURITY.md).
9. **SECURITY.md cleaning sentence.** Invalid UTF-8 is *replaced* (U+FFFD), not removed. "Bidi controls" overclaims: the code removes overrides, embeddings and isolates (U+202A-202E, U+2066-2069) and keeps the marks U+200E, U+200F, U+061C, now listed among what is not removed. "Links without user-info are kept" reads as if such links were dropped: it now says http and https links are kept with their user-info removed, and a link with a control or hidden character is dropped, not rewritten.
10. **The operator commands are described, and the limits and output markers stated**, as your list asks, inside the six sections (add's `--source`, `--stdin` 1 MiB, notes+stdin refusal, `--client-id`; approve all or none with `--top` order; move's same-column no-op and positive ids; archive/unarchive; leaving the archive can answer `limit`; the 2,000/20/500/2,000 limits and the cuts; the notes block, the history caution, `... N more`, the suggested commands).
11. **Banner.** The brief's title line was wider than its box; I aligned it (62 columns).
12. **Test mechanics.** The reverse flag check reads `LocalFlags().FlagUsages()` instead of `cmd.Flags().VisitAll`: naming `*pflag.Flag` would need a direct import of pflag, which go.mod lists as `// indirect` (a direct import would leave go.mod untidy; go.mod/go.sum are untouched).
13. **Compile slips.** None in the brief's Go code. The test code of mine had two: an inline sentence that wrapped across a line break (found by the first GREEN run, see below) and a `SetArgs(nil)` that would make cobra read the test binary's own flags (pflag happens to ignore `-test.*`, so it passed; I pass a non-nil empty slice).

## Tests (17 in `ref_tasks_test.go`)

The brief's four, verbatim: `TestEveryTaskCommandHasAReferenceEntry`, `TestRefTasksIsAListedTopic`, `TestRootHelpPointsAgentsAtTheBoard`, `TestRefTasksNamesTheGateTheLoopAndTheProfile`. Yours (1)-(3) and more, so the docs cannot drift again:

| Test | What it holds to the code |
|---|---|
| `TestRefTasksEntriesDescribeTheirOwnCommand` | every `task X` entry names a real command, has a description and an example, its Usage calls `task X`, each example is a call of `task X` |
| `TestRefTasksEntriesUseOnlyFlagsTheCommandHas` | every `--flag` in Usage, Flags and Examples of an entry exists on that real subcommand or is inherited (`--profile`, `--json`, `--as`, `--db-path`, ... found through `InheritedFlags` of the command inside `newRootCmd()`) |
| `TestRefTasksEntriesNameEveryFlagOfTheirCommand` | every flag a real `task` subcommand declares appears in its entry's Usage or Flags |
| `TestRefTasksSuggestsOnlyCommandsTheCLIHas` | every `monoagentcli ... task X ...` in any `cliDocs` entry (Short, Usage, Flags, Examples) and in `refTasksText` resolves with `newRootCmd().Find` to a real subcommand `X`, and its flags exist on `X`; at least 6 calls are read from the topic (a regexp that read nothing would pass) |
| `TestRefTasksKeepsItsAnchorHeadings` | the six headings are each a whole line at column 0 (no trailing space), once, in order; `SEE ALSO` occurs once; both in the const and in the source file `ref_tasks.go` (the file the later phases edit) |
| `TestRefTasksStatesTheLimitsAndDefaultsTheCodeHas` | every statement of a limit in the topic and the entries equals the code's constant: 2,000 open tasks, 20 an hour, title 200, notes 64 KiB, comment 8 KiB, 500 and 2,000 events, 30 minutes, 24 hours, 1 MiB; a partly changed number or a missing one fails; the defaults the flags declare (`--done-limit`, `--source`, `--limit` 500/2,000, `--lease`) |
| `TestRefTasksNamesEveryErrorCodeTheCLIAnswers` | the JSON section names every code `taskErr` can produce (all seven sentinels) and `exitCodeFor` agrees with "not_found (exit 2)" and the rest exit 3 |
| `TestRefTasksStatesTheNameRulesOfTheStore` | 1 to 64 characters, `._#@:-`, the five reserved labels in any case, checked against `tasks.CheckAgentName` |
| `TestRefTasksSaysWhichCommandsTheGateRefusesAnAgent` | runs each command as an agent (`--as bot`) through the real CLI: `board edit move approve archive unarchive add --ready` answer `operator_only` and are named (as whole words: "unarchive" does not name "archive") in "The operator may ..." and not in "An AI agent may ...", while `list show next add claim comment finish release` are never `operator_only` and are named in the agent's sentence |
| `TestRefTasksJSONSectionShowsTheDocumentsTheCommandsPrint` | 17 calls in a realistic order (add, approve, next, next --claim, comment, show, list, board, finish, move, claim, release, edit, archive, unarchive, archive --status, digest) print the keys the JSON table of the text gives, and each row of the table names the command as a whole word; and `next` with nothing ready prints `task: null` |
| `TestRefTasksGivesNoWayRoundTheGate` | says "ask the person"; never contains "unset" or "bypass" |
| `TestRefTasksIsPrintedAndListed` | `ref tasks` prints exactly `refTasksText`; bare `ref` lists the topic |
| `TestRootHelpPointsAgentsAtTheBoardInEveryLocale` | `root.long` in `en.json` and `es.json` points at `monoagentcli ref tasks` |

### TDD evidence

RED, before `ref_tasks.go` existed: `go test ./cmd/monoagentcli/ -run 'TestEveryTaskCommand|TestRefTasks|TestRootHelp' -count=1` printed `cmd/monoagentcli/ref_tasks_test.go:57:24: undefined: refTasksText ... too many errors` and `FAIL ... [build failed]`: the failure the brief predicts for Step 2.

First GREEN, after the code: 14 of 15 PASS; `TestRefTasksStatesTheNameRulesOfTheStore` FAILED with ```ref tasks` does not say "the labels you, agent, capture, chrome and os are reserved"``: my text had wrapped that sentence across a line break. A text fix (re-wrapped), no change to the test.

Final GREEN, on the tree of `a64ab741` (the Task 8-11 selector and `go build ./...` ran one commit earlier, on `91f9dec4`; `a64ab741` changed only 9 lines of my test file):

- `go test ./cmd/monoagentcli/ -run '^(TestEveryTaskCommand|TestRefTasks|TestRootHelp|TestLegacySocialCommandsVisibleSocialBuild|TestUnknownSubcommandIsAnError|TestBareGroupCommandStillPrintsHelp|TestRejectUnknownSubcommandsLeavesRunnableCommandsAlone|TestWantsJSONErrorSkipsGlobalFlagValues|TestUnknownSubcommandListsAvailable|TestRefDocsCoverEveryRegisteredNodeType|TestRefDocsFlagDeprecatedAINodes)' -count=1 -v`: 25 PASS (my 17 plus the command-tree, help and ref tests), 0 FAIL.
- Build tags: `TestLegacySocialCommandsHiddenDefaultBuild` and `...KeptVisibleDefaultBuild` live behind `//go:build nosocial` (and `...VisibleSocialBuild` behind `!nosocial`), so a `-run` in the default build never runs the first two. Run with `go test -tags nosocial ./cmd/monoagentcli/ -run '^(TestLegacySocialCommandsHiddenDefaultBuild|TestLegacySocialCommandsKeptVisibleDefaultBuild|TestEveryTaskCommand|TestRefTasks|TestRootHelp)'`: 19 PASS (my 17 and those two), 0 FAIL. My test file and the helpers it uses (`captureStdout` in `people_status_test.go`, `runTask` and `newTaskTestDB` in `task_test.go`) carry no build tag; `go vet` is clean in both builds.
- `go test ./internal/i18n/ -count=1`: ok.
- `go test ./cmd/monoagentcli/ -run '^(TestTask|TestAnAgent|TestOperator|TestTheOperator|TestEveryAgentContextMarker|TestFinishAndRelease|TestAgentCommandsNeed|TestNextTells|TestDigest)' -count=1`: ok (44.6 s, on `91f9dec4`).
- `gofmt -l cmd internal`: nothing; `go build ./...`: clean (on `91f9dec4`).
- Hygiene: `ref_tasks.go` 297 lines, `ref_tasks_test.go` 477; the only non-ASCII characters in `ref_tasks.go` are the box-drawing characters of the banner and one em dash in its title; `ref_tasks_test.go` and the three documents' added lines are ASCII only; no tab or trailing space inside the raw string; no `sk-` run; go.mod and go.sum untouched; no stray file (`git status --short` shows only `.superpowers/`).

### Mutation runs

Everything in scratch exports (`git archive <sha> cmd internal data go.mod go.sum automations orgtemplates testdata`, the last three because `cmd/monoagentcli` tests import `automations`), never in the worktree; the mutated file is restored in memory before the next mutant and the control is re-run at the end. Script: `.../scratchpad/t12mut/mutate.py`; logs `battery.log` (commit `79419a65`) and `battery_final.log` (final commit `91f9dec4`).

**Battery 1** (tests of `79419a65`, 28 mutants): 28 killed, 0 survived; the control was green before, after every restore and at the end; 11 mutants were also restored and re-run in the same session (marked "restored: PASS" below).

**Battery 2** (tests of `91f9dec4`, 31 mutants = battery 1 plus `name-length`, `stdin-limit-partial`, `claudecode-removed`): 24 killed in the run. 7 runs reported BUILD BROKEN (`code-dropped-json` and its restored re-run, then `see-also-twice` to `print-nothing`); the harness kept no output then, the machine was at a load average above 20, and in those minutes I had copied a throwaway view test into that export and moved it out again, so I cannot name the cause. I re-ran exactly those 7 on the pristine export (the checksums of its five touched files equal the committed blobs): all 7 killed with the messages below, and `code-dropped-json` passes after its restore. The control was green after every restore.

**Two survivors, found by looking for what the substring checks cannot see** (`operator-sentence-archive`: `archive` dropped from "The operator may ..." while `unarchive` stays; `json-row-archive`: the same in the `{"profile","tasks"}` row): both PASSED against the tests of `79419a65`, because "unarchive" contains "archive". Fixed in `a64ab741` (a word-bounded `refNames` in the gate test and the JSON test, 9 lines). Re-run on the export of `a64ab741`: both killed (`the operator's sentence of `ref tasks` does not name "archive"`, `the JSON section of `ref tasks` has no line that gives {"profile","tasks"} for "archive"`), and `operator-sentence`, `agent-sentence`, `json-row` still killed; control green.

Every mutant of battery 2 (and the two above), the test that killed it and its first message:

| Mutant (what was broken in the scratch copy) | Killed by | First message |
|---|---|---|
| `entry-missing`: the `task digest` entry removed (restored: PASS) | `TestEveryTaskCommandHasAReferenceEntry` | `ref commands` has no entry for `task digest` |
| `usage-typo`: `--claimed-by` written `--claimed_by` in a usage (restored: PASS) | `...EntriesUseOnlyFlagsTheCommandHas`, `...SuggestsOnlyCommandsTheCLIHas` | shows `task list` with --claimed, which it does not have |
| `anchor-duplicated`: a second `COLUMNS` line (restored: PASS) | `...KeepsItsAnchorHeadings` | more than one line that is just "COLUMNS" |
| `anchor-renamed`: `THE AGENT LOOP` becomes `THE LOOP` (restored: PASS) | `...KeepsItsAnchorHeadings` | no line that is just "THE AGENT LOOP" at the margin |
| `help-en-removed`: the sentence removed from `en.json` (restored: PASS) | `TestRootHelpPointsAgentsAtTheBoard`, `...InEveryLocale` | the root help does not mention the task board |
| `help-es-removed`: removed from `es.json` (restored: PASS) | `...InEveryLocale` | the es root help does not point agents at `monoagentcli ref tasks` |
| `ref-unregistered`: `refTasksCmd()` not added in `newRefCmd` (restored: PASS) | `...IsAListedTopic`, `...IsPrintedAndListed` | `ref tasks` is not registered |
| `invented-subcommand`: an example calls `task complete` (restored: PASS) | `...EntriesDescribeTheirOwnCommand`, `...SuggestsOnlyCommandsTheCLIHas` | not a call of that command |
| `limit-hour-partial`: one of two statements says 25 an hour (restored: PASS) | `...StatesTheLimitsAndDefaultsTheCodeHas` | says "25 tasks an hour", which is 20 in the code |
| `limit-open-tasks`: 1,000 open tasks | same | says "1,000 open tasks", which is 2,000 in the code |
| `code-dropped-json`: `not_claimant` dropped from the JSON section list (restored: PASS) | `...NamesEveryErrorCodeTheCLIAnswers` | the JSON section does not name the error code "not_claimant" |
| `see-also-twice`: SEE ALSO also in a description | `...KeepsItsAnchorHeadings` | ref_tasks.go has SEE ALSO 2 times, want once |
| `heading-trailing-space`: `COLUMNS ` | same | no line that is just "COLUMNS" at the margin |
| `headings-swapped`: `JSON` and `TASK TEXT IS DATA` swap | same | has "JSON" out of order |
| `flag-renamed-both`: `--client-id` becomes `--clientid` in every text | `...NameEveryFlag...`, `...UseOnlyFlags...`, `...SuggestsOnly...` | shows `task add` with --clientid, which it does not have |
| `default-changed`: board's default written 5 | `...StatesTheLimitsAndDefaultsTheCodeHas` | the entry of `task board` does not say "default 50" |
| `print-nothing`: `ref tasks` prints nothing | `...IsPrintedAndListed` | prints 0 bytes that are not its text |
| `listing-line-removed`: `ref` does not list `tasks` | same | `ref` does not list the tasks topic |
| `loop-flag-invented`: the loop shows `release --reason` (restored: PASS) | `...SuggestsOnlyCommandsTheCLIHas` | suggests `task release` with --reason, which it does not have |
| `ask-the-person-removed`: "ask someone" | `...GivesNoWayRoundTheGate` | does not tell an agent that is refused to ask the person |
| `bypass-hinted`: "look for a bypass" | same | mentions "bypass": it must not hint at a way round the gate |
| `reserved-label-drift`: `browser` for `chrome` | `...StatesTheNameRulesOfTheStore` | does not say "the labels you, agent, capture, chrome and os are reserved" |
| `lease-partial`: one statement says 20 minutes | `...StatesTheLimitsAndDefaultsTheCodeHas` | says "20 minutes" for a lease, which is 30 in the code |
| `operator-sentence`: `board` out of the operator's sentence | `...SaysWhichCommandsTheGateRefusesAnAgent` | the operator's sentence does not name "board" |
| `json-row`: the `{"profile","tasks"}` row says `{"profile","task"}` | `...JSONSectionShowsTheDocumentsTheCommandsPrint` | no line that gives {"profile","tasks"} for "approve" |
| `example-wrong-command`: an example of `task move` is `task edit` | `...EntriesDescribeTheirOwnCommand` | the example is not a call of that command |
| `usage-wrong-command`: the usage of `task unarchive` calls `task archive` | same | the usage of `task unarchive` ... does not call that command |
| `agent-sentence`: `archive` added to "An AI agent may ..." | `...SaysWhichCommandsTheGateRefusesAnAgent` | names "archive", which an agent may not run |
| `name-length`: 32 characters | `...StatesTheNameRulesOfTheStore` | says "1 to 32 characters of letters", which is 64 in the code |
| `stdin-limit-partial`: one statement says 2 MiB | `...StatesTheLimitsAndDefaultsTheCodeHas` | says "at most 2 MiB", which is 1 in the code |
| `claudecode-removed`: `CLAUDECODE` no longer named | `...NamesTheGateTheLoopAndTheProfile` (the brief's test) | does not mention "CLAUDECODE" |
| `operator-sentence-archive`, `json-row-archive` (above) | gate test, JSON test (after `a64ab741`) | does not name "archive" |

## How each class of statement was verified against the code

| Statement class (where) | How verified |
|---|---|
| Subcommand names, usage forms, flags, examples, loop lines (entries, `ref tasks`) | read every `Use` and flag in `task.go`, `task_read.go`, `task_ops.go`, `task_agent.go`; the four entry/flag/command tests resolve every one through the real command tree |
| Defaults: `--limit` 500 up to 2,000, `--done-limit` 50, `--source` cli, `--lease` 30m up to 24h | flag `DefValue`s and `tasks.DefaultListLimit/MaxListLimit/DefaultLease/MaxLease` in the limits test |
| Limits: 2,000 open tasks (an unarchived task counts), 20 agent tasks an hour, 500 events stop comments, 2,000 stop claims, title 200, notes 64 KiB, comment 8 KiB, stdin 1 MiB, name 64 | `tasks.*` constants in the limits and names tests; semantics read in `store.go` (`checkAddLimits`, `openTasksSQL`), `ops.go` (`moveTx` checks the limit when a task leaves the archive), `claims.go` (`commentTx` at 500, `claimRefusal` and `pickNext` at 2,000), `clean.go` (cuts, not refusals), `task.go` (`maxStdinBytes`) |
| Who counts as an agent (marker, `--as`, `MONOAGENT_ACTOR`, a blank `--as` is nameless and never the operator) | read `callerFor`, `taskCaller.operator/agent` (`task.go:151-207`), `orgsign.AgentContextMarkers()`; existing tests `TestOperatorCommandsRefuseAnAgentContext`, `TestEveryAgentContextMarkerRefusesOperatorCommands` |
| Who may do what; `board` operator-only; agents `list`/`show` | the gate test runs the commands as an agent; read `store.List` (`defaultStatuses`), `Store.Get` (no actor), `Store.Board` and `newTaskBoardCmd` |
| `comment` under an agent context (named: holder only, unnamed: `invalid_input`; renews the lease) | read `newTaskCommentCmd`, `commentTx`, `heldBy`; the gate test (a comment as an agent is not `operator_only`) |
| Names (1-64 of `[A-Za-z0-9._#@:-]`, five reserved labels in any case, a label not a credential) | `tasks.CheckAgentName` in the names test; `nameRE` and `reservedNames` in `store.go` and `clean.go` |
| Lease semantics (30 min default, 24 h cap, comments renew and never shorten, a run-out claim may be taken over and stays the holder's until then, `claim` renews or takes over) | read `clampLease`, `leaseEnd`, `claimOf`, `claimTx`, `heldBy`, `commentTx` in `claims.go` |
| Move/approve/archive/unarchive (same-column no-op, positive ids, `--top` order, all or none, claim ends outside in_progress, unarchive to the origin column, in_progress returns as ready) | read `Move`, `placeIn`, `moveTx`, `Approve`, `each`, `Archive`, `ArchiveStatus`, `Unarchive` in `ops.go`, and the `--before/--after` parsing in `newTaskMoveCmd` |
| Columns and what an agent sees (inbox only by naming, `next` never returns it, default lists) | read `pickNext`, `List`, `Get`; existing tests `TestTaskListHidesTheInboxFromEveryKindOfAgent`, `TestTaskShowByAnAgentReachesTheInbox` |
| JSON document shapes | the JSON test (17 real calls) |
| Error codes and exit codes (2 not found, 3 the rest, exit 1 for other failures with `{"error"}` alone, an unknown flag or a bad number/duration is the parser's exit 1 with no JSON) | the codes test (`taskErr`, `exitCodeFor`); read `main.go` (`reportCommandError`, `wantsJSONError` is for `org`/`status` only), `automation.go` (`withJSONErrors`, `jsonErrorCode`), `exitcodes.go`; cobra reports a flag error before `RunE` |
| Output markers: notes indented between the untrusted notice and `(end of the notes)`, the history caution, `... N more (...)`, suggested commands with `--profile ID` and `--as`, the profile named in text | read `printNotes`, `historyNotice`, `printTaskTable`, `printBoard`, `printTask`, `taskCommand`, `printNext`, `continueHelp`, the digest; the hostile-notes and text tests of Tasks 9 and 11 pin them (not re-tested here) |
| `digest`: nothing in text when none is ready, `--json` always a document, exit 0 for run-time trouble, an unknown flag is exit 1 | read `newTaskDigestCmd`; the Task 11 review's last cannot-verify item and Minor 5 (and the JSON test's `digest` step) |
| SECURITY.md: tables and cascade, cleaning, no listener | `data/migrations/062_tasks.sql` (three tables, `ON DELETE CASCADE`), `TestDeletingAProfileDeletesItsBoard` and the DSN `foreign_keys(ON)` (`internal/storage/database.go:45`); `clean.go` (`cleanText`, `hidden`, `cleanURL`); `internal/tasks` imports no `net` package and only the four CLI files import it (grep) |
| `capture task` files org issues; the app switches the active profile; the spec path | `capture_task.go`; `wails-app/app_profiles.go` `SwitchProfile` and `initDB` in `root.go` (`--profile` takes an id or a name); `docs/mastermind/specs/2026-10-05-task-board-design.md` exists and is tracked |
| Root help sentence | `TestRootHelpPointsAgentsAtTheBoard` (+ every locale) |

## Self-review

Read all four diffs again. Complete against the brief and the dispatch; nothing beyond them except the extra tests. The one judgement call I would like a reviewer to look at is test (D): `TestRefTasksSaysWhichCommandsTheGateRefusesAnAgent` reads two sentences of WHO MAY DO WHAT ("The operator may ..." and "An AI agent may ...") and runs the real CLI; P4/P5, who edit that section, must keep those sentences or update the test (its failure message says so).

## Concerns

1. **Help-text gap, not patched (not my files):** `task archive`'s own `Use` string reads `archive ID... | --status done` (`task_ops.go:181`) but `--status` takes any of the five columns; `ref commands` says `--status STATUS` and lists the five.
2. **Task 11 fix in flight:** the docs' `--as` wording is true before and after it (deviation 3); a new flag on an agent command would fail the reverse flag test until its entry names it (as intended).
3. **Forward-looking words in the code:** `operatorOnlyError` says "run it in your own terminal or in the app" (`task.go:192`); `ref tasks` and the documents say "a terminal of their own" until an app board exists.
4. **AGENTS.md "Start here"** lists the `ref` topics and has neither `ref tasks` nor the older `ref org`; I touched nothing else, as the brief says. Worth one line when the app phase edits AGENTS.md.
5. **Five commits, not two** (explained above).
6. `digest`'s own `Long` still says "It always exits 0" (Task 11 review, Minor 5); `ref tasks` carries the exception (an unknown flag is exit 1).

## Fix round 1

Resumed on HEAD `08feab19` (it holds the Task 11 fix merge `152b91f4`). New commits only, each with explicit pathspecs (`git commit ... -- <paths>`): another session had `docs/mastermind/plans/2026-10-05-tasks-board-phase1-store-cli.md` modified in this checkout, and I neither staged nor committed it.

| SHA | Subject | Files |
|---|---|---|
| `390e1cbb` | docs(tasks): ref tasks and AGENTS.md name digest in the gate, say what a comment does to a lease, and describe the limits and hints as the code does | `ref_tasks.go`, `AGENTS.md`, `SECURITY.md`, `CHANGELOG.md`, `task.go`, `task_ops.go`, `task_agent.go`, `task_agent_text_test.go` |
| `e3d66b2f` | test(tasks): the gate test walks the task command tree through one table, runs each command as an agent and reads only the operator_only sentence; the limits test covers AGENTS.md and SECURITY.md and says what a comment does to a lease | `ref_tasks_gate_test.go` (new), `ref_tasks_test.go` |
| `82dcdf9e` | test(tasks): a sentence that lists what agents may run must not name an operator command, and ref tasks must say what a digest that fails prints | `ref_tasks_gate_test.go` |

The wording commit comes first and keeps every test green; the test commits follow it (the sharpened tests need the new wording). The second test commit exists because the mutation runs of `e3d66b2f` showed that "board added to the agent's list" survived (below).

### What each item became

| Item | What I did |
|---|---|
| **I1** | `ref_tasks.go` WHO MAY DO WHAT: the agent's sentence now ends "...and run digest, which has no gate: it runs the same in any context, so a session-start hook can call it"; "Anything else is the operator's" became "Only board, edit, move, approve, archive, unarchive and add --ready are the operator's: they answer an agent with exit 3 and the code operator_only. board shows the Inbox, so an agent uses "task list"." AGENTS.md names `digest` in the agent's list with the same reason. The gate test is rewritten in the new `ref_tasks_gate_test.go` (the old one is removed from `ref_tasks_test.go`): one table `refTaskGate` (16 rows: every subcommand once, `add` agent-ok, `add --ready` its own operator row); (a) walks `newTaskCmd(&globalConfig{}).Commands()` and fails for a subcommand with no row and for a row with no subcommand, saying which table to edit; (b) runs every row as an agent through the real CLI and requires `operator_only` exactly where the row says; (c) reads only the sentences of WHO MAY DO WHAT that contain `operator_only`: the command names in them (word-bounded, `add --ready` through the word `--ready`) must be exactly the operator rows, and every agent-ok command must be named in the other sentences. No lead phrase and no first-period parse remain. |
| **I2** | The lease sentence (ref_tasks.go THE AGENT LOOP, AGENTS.md) now says: a comment extends the claim to 30 minutes from the comment if that is later, never shortens it and does not add the `--lease` that was asked for; with a long `--lease`, comment before its last 30 minutes or run `claim ID --lease` again (which extends the claim to that lease counted from then, if that is later: `claimTx` takes the later of the two). The same claim was in three more places and is fixed: the loop's line comment (`# progress; extends the lease`, ref_tasks.go and AGENTS.md), the `--lease` flag of the `task next` entry ("your comments renew it") and the Short of the `task comment` entry ("renews its lease"). **I also fixed the changelog bullet** ("renewed by its comments"), which is outside your path list: it made the same claim. The limits test now requires both texts that explain the lease (the AGENT LOOP section and the AGENTS.md section) to contain "extends it to 30 minutes from the comment" (the number from `tasks.DefaultLease`) and "never shortens it", and fails on any sentence of any doc text (topic, entries, AGENTS.md, SECURITY.md) that has a word starting "renew" and the word "comment". |
| **M2** | `task.go:192`: " or in the app" dropped; the refusal ends "run it in your own terminal". Pinned phrases are only "your own terminal": the selector passes. |
| **M3** | `task_ops.go:181`: `Use: "archive ID... | --status STATUS"`. The command's own help (Short; the `--status` flag: "Archive every task of this column (inbox, ready, in_progress, review or done)") and the `cliDocs` entry (Usage `--status STATUS`, the same five columns) agree. |
| **M4** | "2,000 open tasks (every task that is not archived, Done ones included: archive some to make room; one that comes back from the archive counts again)" in ref_tasks.go, AGENTS.md and SECURITY.md. The pattern `([\d,]+) open tasks` still matches in all three. |
| **M5** | `--limit` flag of the `task list` entry: "in text, a list that is cut says how many more there are"; the same "In text," opens the sentence on "... N more" in THE AGENT LOOP. |
| **M6** | AGENTS.md: a `ref tasks` row in the "Start here" table (after `ref api`, in the style of its rows; not `ref org`) and a `MONOAGENT_ACTOR` row in "Runtime environment variables" (the agent's name for the agent commands when `--as` is not given, `--as` wins, setting it makes the caller an agent, default unset). A small test (`TestRefTasksIsInAGENTSMDsTopicTableAndMONOAGENTACTORInItsVariableTable`) keeps both rows. |
| **M7** | `refStates` now reads the Task board section of AGENTS.md and of SECURITY.md besides the topic and the entries, each text on one line (a hard wrap cannot hide a phrase). To make the two documents' numbers checkable I aligned their wording with the topic's ("20 tasks an hour", "500 events takes no more comments, and one with 2,000 events no more claims") and widened three patterns (`titles? (?:is\|are )?\(?(\d+) characters`, `notes (?:are )?\(?(\d+) KiB`, `(?:note\|comments) \(?(\d+) KiB`). The CHANGELOG is not included (its bullet has no heading to cut at). |
| **M8** | `task digest` entry (Flags): "with --json it prints ... on success ... a digest that fails prints one line on standard error and exits 0"; THE AGENT LOOP: "a digest that fails prints one line on standard error and nothing on standard output"; JSON section: "Every command ... prints one document, except a digest that fails: that prints one line on standard error and nothing on standard output, and exits 0" and "An error (never a digest's) is {"error","code"} on standard output". The command's own help (`task_agent.go`): "With --json it prints its document on success, even when nothing is ready". That sentence was pinned by a Task 11 test (`task_agent_text_test.go:408`, "With --json it always prints its document"): I changed the pinned phrase (and added the old one to the absent list) in the wording commit, so that commit stays green. A new test (`TestRefTasksSaysWhatADigestThatFailsPrints`) runs a digest on a profile that does not exist (one line on standard error, nothing on standard output, `err == nil`, with --json) and requires both sections to say so. |
| **M9** | "Every command the output suggests carries --profile ID, and --as NAME when the caller has a name (an agent that only an environment variable identifies has none, so its list and show hints have no --as; a name a shell would not read as one word is written <name>). Where the reader has to choose a name, as in the hints to take a task, the hint writes --as <your-name>." Checked against `taskCommand` (task_read.go:71-80), `takeCommand` and the printers (task_read.go:199, :267; task_agent.go:50-88, :319). |
| **Nit** | AGENTS.md: the ragged wrap is gone (the paragraph is now three: the lease, the limits, the text and the JSON). The Spanish sentence is untouched. |

### Proof by mutation (scratch exports of the commits; never in the tree)

RED first: the final tests run against the old wording (the files of `08feab19` copied over the export of `82dcdf9e`) fail where the reviewer's findings are: the `operator_only` sentence names none of the operator commands and WHO MAY DO WHAT never names `digest`; no section says that a failing digest prints one line on standard error; the lease sentence is missing in both texts and five sentences say that a comment renews it (the loop, the loop's line comment, the AGENTS.md code block, the `task next` entry, the `task comment` entry); AGENTS.md has neither new row.

The four you named, on `e3d66b2f`/`82dcdf9e`:

| Mutant | Result |
|---|---|
| (i) `digest` deleted from WHO MAY DO WHAT | FAIL `TestRefTasksSaysWhichCommandsTheGateRefusesAnAgent`: WHO MAY DO WHAT never names `digest` outside the sentence that says operator_only; restored: PASS |
| (ii) `board` named in the agent's list: (a) moved there from the operator_only sentence | FAIL: the sentence that says operator_only does not name `board`; restored: PASS |
| (ii) (b) added to the agent's list while it stays in the operator_only sentence | PASSED on `e3d66b2f` (the brief's rule (c) cannot see it: `board` is rightly named in the operator_only sentence). Closed in `82dcdf9e` by one more rule: a sentence of WHO MAY DO WHAT that names three or more commands an agent may run and has no word "operator" must not name an operator command. Now FAIL: a sentence of WHO MAY DO WHAT lists what an agent may run and names `board`. It is a heuristic (a later phase that mixes the two kinds of command in one sentence without the word "operator" meets its message, which says what to do) |
| (iii) a subcommand added to `newTaskCmd` with no row | FAIL: `task frobnicate` has no row in refTaskGate (and `TestEveryTaskCommandHasAReferenceEntry`); restored: PASS |
| (iv) `digest` made operator-only in code | FAIL (behavioural half): `task digest` run by an agent answers the code "operator_only", but refTaskGate says operator_only is false for it |

The whole final battery ran on the export of `82dcdf9e` (44 mutants: the four above with their variants, and the rest below; script `.../scratchpad/t12fix1/mutate.py`, log `battery2.log`). The control was green before, after every restore and at the end, and the help test (`TestTaskAgentCommandsExplainTheirOptionsInTheirHelp`) was run as the control of the one mutant that needs it. **41 killed, 3 survived.** "GateTest" is `TestRefTasksSaysWhichCommandsTheGateRefusesAnAgent`, "LimitsTest" is `TestRefTasksStatesTheLimitsAndDefaultsTheCodeHas`; "restored: PASS" marks the mutants I restored and re-ran the tests on in the same session.

| Mutant | Result | Killed by | First message |
|---|---|---|---|
| `xx-gate-sentence-names-an-agent-command`: list joins the operator_only sentence | killed | GateTest | the sentence of `ref tasks` that says operator_only names `list`, which an agent may run |
| `xxi-gate-sentence-loses-the-code`: the operator_only code leaves WHO MAY DO WHAT | killed | GateTest | WHO MAY DO WHAT has no sentence with the code operator_only: it must name the commands the gate refuses |
| `xxii-add-ready-dropped-from-the-gate-sentence`: add --ready leaves the operator_only sentence | killed | GateTest | the sentence of `ref tasks` that says operator_only does not name `add --ready` |
| `xxiii-table-row-without-a-command`: the table gets a row for a command that is not there | killed | GateTest | refTaskGate (ref_tasks_gate_test.go) has a row for `task frobnicate`, which is no command: remove or rename it |
| `xxiv-table-row-missing`: the table loses the row of unarchive | killed | GateTest | `task unarchive` has no row in refTaskGate (ref_tasks_gate_test.go): add one that says whether an agent may run it, and say the same in WHO MAY DO WHAT (ref_tasks.go) and |
| `xxv-table-says-digest-is-the-operators`: the table makes digest operator-only | killed | GateTest | `task digest` run by an agent answers the code "", but refTaskGate (ref_tasks_gate_test.go) says operator_only is true for it |
| `xxvi-agents-list-names-approve`: approve joins the agent's list (and stays in the operator_only sentence) | killed | GateTest | a sentence of WHO MAY DO WHAT lists what an agent may run and names `approve`, which is the operator's: "An AI agent may list tasks (the ready, in_progress and review one |
| `xxvii-loop-loses-the-failing-digest`: THE AGENT LOOP loses the sentence on a digest that fails | killed | TestRefTasksSaysWhatADigestThatFailsPrints | the THE AGENT LOOP section of `ref tasks` does not say "a digest that fails" |
| `v-loop-says-every-comment-renews`: THE AGENT LOOP says every comment renews the lease | killed (restored: PASS) | LimitsTest | THE AGENT LOOP of `ref tasks` does not say "extends it to 30 minutes from the comment" |
| `vi-agents-lease-reverted`: AGENTS.md says the lease is renewed by comments | killed (restored: PASS) | LimitsTest | AGENTS.md does not say "extends it to 30 minutes from the comment" |
| `x-entry-next-lease-renews`: the entry of task next says comments renew the lease | killed | LimitsTest | a text says that a comment renews the lease (it only extends it to 30 minutes from the comment): "--claim Take the task for yourself in one step (needs --as NAME) --lease |
| `xi-entry-comment-short-renews`: the entry of task comment says it renews the lease | killed | LimitsTest | a text says that a comment renews the lease (it only extends it to 30 minutes from the comment): "task comment: Add a note to a task: the operator's own, or an AI agent's |
| `vii-agents-hour-drift`: AGENTS.md says 25 tasks an hour | killed (restored: PASS) | LimitsTest | a text says "25 tasks an hour" for the tasks an agent may add an hour, which is 20 in the code |
| `viii-security-open-drift`: SECURITY.md says 3,000 open tasks | killed | LimitsTest | a text says "3,000 open tasks" for a profile's open tasks, which is 2,000 in the code |
| `ix-security-claims-drift`: SECURITY.md says 3,000 events stop a claim | killed | LimitsTest | a text says "3,000 events no more claims" for the events that stop claims, which is 2,000 in the code |
| `xii-agents-ref-tasks-row-removed`: AGENTS.md loses the ref tasks row | killed | TestRefTasksIsInAGENTSMDsTopicTableAndMONOAGENTACTORInItsVariableTable | AGENTS.md has no table row that starts \| `ref tasks` \| |
| `xiii-agents-actor-row-removed`: AGENTS.md loses the MONOAGENT_ACTOR row | killed | TestRefTasksIsInAGENTSMDsTopicTableAndMONOAGENTACTORInItsVariableTable | AGENTS.md has no table row that starts \| `MONOAGENT_ACTOR` \| |
| `xvi-digest-help-always`: the digest help says it always prints its document | killed | TestTaskAgentCommandsExplainTheirOptionsInTheirHelp | task digest --help: the help (<nil>) does not say "With --json it prints its document on success, even when nothing is ready": |
| `xvii-json-section-no-digest-exception`: the JSON section loses the failing digest | killed | TestRefTasksSaysWhatADigestThatFailsPrints | the JSON section of `ref tasks` does not say "a digest that fails" |
| `xiv-app-in-refusal`: the refusal says 'or in the app' again | PASS |  |  |
| `xv-archive-use-done`: the archive Use string says --status done again | PASS |  |  |
| `xviii-agents-digest-not-named`: AGENTS.md no longer names digest among the agent's commands | PASS |  |  |
| `entry-missing`: the `task digest` entry is removed from cliDocs | killed | TestEveryTaskCommandHasAReferenceEntry | `ref commands` has no entry for `task digest` |
| `usage-typo`: --claimed-by written --claimed_by in a usage | killed | TestRefTasksEntriesUseOnlyFlagsTheCommandHas, TestRefTasksSuggestsOnlyCommandsTheCLIHas | `ref commands` shows `task list` with --claimed, which it does not have |
| `anchor-duplicated`: the COLUMNS heading appears twice | killed | TestRefTasksKeepsItsAnchorHeadings | ref_tasks.go has more than one line that is just "COLUMNS" |
| `ref-unregistered`: ref tasks is not registered in newRefCmd | killed | TestRefTasksIsAListedTopic, TestRefTasksIsPrintedAndListed | `ref tasks` is not registered |
| `help-es-removed`: the root help sentence is removed from es.json | killed | TestRootHelpPointsAgentsAtTheBoardInEveryLocale | the es root help does not point agents at `monoagentcli ref tasks` |
| `invented-subcommand`: an example calls task complete instead of task finish | killed | TestRefTasksEntriesDescribeTheirOwnCommand, TestRefTasksSuggestsOnlyCommandsTheCLIHas | the example "monoagentcli --profile work task complete 12 --as claude-7f3a --result \"opened PR 41\"" of `task finish` is not a call of that command |
| `limit-hour-partial`: one of two statements of the hourly limit says 25 | killed | LimitsTest | a text says "25 tasks an hour" for the tasks an agent may add an hour, which is 20 in the code |
| `lease-partial`: one statement of the default lease says 20 minutes | killed | LimitsTest | a text says "20 minutes" for a lease, which is 30 in the code |
| `code-dropped-json`: not_claimant dropped from the JSON section list | killed | TestRefTasksNamesEveryErrorCodeTheCLIAnswers | the JSON section of `ref tasks` does not name the error code "not_claimant" |
| `json-row`: the JSON row for list/approve/archive says {profile, task} | killed | TestRefTasksJSONSectionShowsTheDocumentsTheCommandsPrint | the JSON section of `ref tasks` has no line that gives {"profile","tasks"} for "approve" |
| `json-row-archive`: archive dropped from the {profile, tasks} row (unarchive stays) | killed | TestRefTasksJSONSectionShowsTheDocumentsTheCommandsPrint | the JSON section of `ref tasks` has no line that gives {"profile","tasks"} for "archive" |
| `flag-renamed-both`: --client-id renamed everywhere in the texts | killed | TestRefTasksEntriesNameEveryFlagOfTheirCommand, TestRefTasksEntriesUseOnlyFlagsTheCommandHas, TestRefTasksSuggestsOnlyCommandsTheCLIHas | `ref commands` shows `task add` with --clientid, which it does not have |
| `print-nothing`: ref tasks prints nothing | killed | TestRefTasksIsPrintedAndListed | `ref tasks` prints 0 bytes that are not its text (9882 bytes) |
| `bypass-hinted`: the text hints at a bypass | killed | TestRefTasksGivesNoWayRoundTheGate | `ref tasks` mentions "bypass": it must not hint at a way round the gate |
| `reserved-label-drift`: the reserved labels list names browser, not chrome | killed | TestRefTasksStatesTheNameRulesOfTheStore | the WHO MAY DO WHAT section of `ref tasks` does not say "the labels you, agent, capture, chrome and os are reserved" |
| `name-length`: an agent's name length says 32 characters | killed | TestRefTasksStatesTheNameRulesOfTheStore | a text says "1 to 32 characters of letters" for the length of an agent's name, which is 64 in the code |
| `stdin-limit-partial`: one statement of the --stdin limit says 2 MiB | killed | LimitsTest | a text says "at most 2 MiB" for what --stdin reads, which is 1 in the code |

The three survivors are expected, and I list them rather than hide them:

- `xiv-app-in-refusal`: "or in the app" put back into the refusal. No test pins it (M2 says so: the tests pin only "your own terminal").
- `xv-archive-use-done`: `--status done` put back in the archive `Use` string. No test pins it (M3).
- `xviii-agents-digest-not-named`: `digest` taken out of AGENTS.md's list of what an agent runs. The gate test reads only `ref tasks`; AGENTS.md is held to its numbers and its lease sentence. (`digest` stays named in the same section's code block and surfaces table, so a "named somewhere" check would not catch it either; a pin needs a sentence-level rule like the one for `ref tasks`, and AGENTS.md's one sentence that carries `operator_only` also carries the JSON/error list and names `digest` and `board`, so that rule does not fit it as written.)

And one I ran separately on the same export (`xxviii-operator-abilities-sentence-drops-board`): dropping `board` from "The operator may add ..., board, and comment on any task" also survives (see below).

### Verification (tree of `82dcdf9e`)

- `gofmt -l cmd/monoagentcli`: nothing. `go vet ./cmd/monoagentcli/` and `go vet -tags nosocial ./cmd/monoagentcli/`: clean. `go build ./...`: clean.
- `go test ./cmd/monoagentcli/ -run '^(TestEveryTaskCommand|TestRefTasks|TestRootHelp|TestTask|TestAnAgent|TestOperator|TestTheOperator|TestEveryAgentContextMarker|TestFinishAndRelease|TestAgentCommandsNeed|TestNextTells|TestDigest)' -count=1 -timeout 15m`: ok (30.0 s). The reference tests alone, `-count=3`: ok (no flake).
- `go test -tags nosocial ./cmd/monoagentcli/ -run '^(TestLegacySocialCommandsHiddenDefaultBuild|TestLegacySocialCommandsKeptVisibleDefaultBuild|TestEveryTaskCommand|TestRefTasks|TestRootHelp|TestTaskAgentCommandsExplainTheirOptionsInTheirHelp)'`: 22 PASS, 0 FAIL. `go test ./internal/i18n/`: ok.
- Tests in `ref_tasks_test.go` and `ref_tasks_gate_test.go`: 19 (the 17 of before, the gate test rewritten, plus `TestRefTasksSaysWhatADigestThatFailsPrints` and the AGENTS.md rows test).
- Sizes: `ref_tasks.go` 310, `ref_tasks_test.go` 495, `ref_tasks_gate_test.go` 138 lines. Added lines: ASCII only in the tests; in the documents the only non-ASCII characters are two em dashes in the two new AGENTS.md table rows, as in the neighbouring rows; no invisible character. go.mod and go.sum untouched. The six `refTasksText` headings are unchanged (the anchors test passes).

### What I did not take, and notes

- **The code's own help still says "renewed by your comments"** (`task_agent.go:101` in `next`'s Long, `:138` the `--lease` flag help, `:180` the Short of `comment`: "renews its lease"; the last one is pinned by `task_agent_text_test.go:407`). It is the same imprecision as I2, in `--help` instead of `ref`, and outside the brief's list (the brief allows one help sentence, the digest's). Left as it is: say if you want the three lines aligned.
- The brief's rule (c) cannot pin the sentence that lists the operator's abilities ("The operator may add ..., board, and comment on any task") any more, because the lead phrases are gone by design: the mutant that drops `board` from that sentence now survives. The sentence that matters for agents (the one with `operator_only`) is pinned.
- AGENTS.md's own gate sentences are not parsed (only its numbers and the lease sentence are held): its sentence that carries `operator_only` also carries the JSON/error list and names `digest` and `board`, so rule (c) would not apply to it as written.
- I did not touch the spec (spec :240 still quotes "or in the app"; yours).
- `ref_tasks_test.go` is at 495 lines: any further test goes into another file.

## Fix round 2

Resumed on HEAD `54e6f323` (docs-only commits after `82dcdf9e`; no Go file had changed). New commits only, each with explicit pathspecs (`git commit ... -- <paths>`): other sessions had plan files modified in this checkout (`docs/mastermind/plans/2026-10-06-tasks-board-phase4-extension.md`, later the phase 5 one) and I neither staged nor committed them. Another session committed `ba503047` (the phase 4 plan) on the branch after my two commits; it touches none of my files.

| SHA | Subject | Files |
|---|---|---|
| `1aa0ea17` | docs(tasks): the lease advice says what a comment does on a long lease, and the commands' own help says the same | `ref_tasks.go`, `AGENTS.md`, `task_agent.go`, `task_agent_text_test.go` |
| `4244f8dc` | test(tasks): pin that the lease advice says a comment changes nothing until fewer than 30 minutes remain | `ref_tasks_test.go`, `ref_tasks_gate_test.go` |

The wording commit is green on its own (the tests of `82dcdf9e` accept the new wording; the pins of the old help words in `task_agent_text_test.go` change in it); the test commit adds the new pins on top.

### What each item became

| Item | What I did |
|---|---|
| **F1** | THE AGENT LOOP of `ref tasks` and the AGENTS.md section now say: a comment "extends it to 30 minutes from the comment, if that is later, and never shortens it (it does not add the --lease you asked for), so with a long --lease a comment changes nothing until fewer than 30 minutes of it remain: comment then, or run claim ID --as NAME --lease DURATION again, which extends the claim to that lease counted from then, if that is later." (the re-reviewer's wording, fitted to each text; the AGENTS.md one has the code spans). Checked against the code again: `commentTx` (claims.go:364-369: for the claimant's in-progress row `claim_until = max(claim_until, now + DefaultLease)`), `claimTx` (claims.go:229-237: the same name takes `max(claim_until, end)`), `claims_lease_test.go:198-208` (a 2-hour lease commented on at +10 minutes still ends at +2 hours). The command is written with `--as NAME` and a duration, as `claim` needs a name and `--lease` needs a value. |
| **F1, the pin** | `TestRefTasksStatesTheLimitsAndDefaultsTheCodeHas` now requires "fewer than 30 minutes" (the number from `tasks.DefaultLease`, like the other phrases) in THE AGENT LOOP and in the AGENTS.md section, besides "extends it to 30 minutes from the comment" and "never shortens it". **One step beyond the brief:** the sentence scan that already rejected "renew" together with "comment" now also rejects "before its last" together with "comment" (in the topic, the entries, AGENTS.md and SECURITY.md). The positive pin alone lets the old sentence come back beside the new one with every required phrase still present; mutants `i-c` and `ii-c` below are killed only by this scan. Its message now says what a comment does to a lease. |
| **F2** | `task_agent.go`: the Long of `next` ("A claim lasts 30 minutes by default (--lease, at most 24h); a comment of yours extends it to 30 minutes from the comment, if that is later, and never shortens it."), the `--lease` flag help of `next` ("How long you hold it (default 30m, at most 24h); a comment extends it to 30 minutes from the comment, if that is later") and the Short of `comment` ("... (extends its lease to 30 minutes from the comment, if that is later)", the words the `ref commands` entry of `task comment` has had since round 1). `task_agent_text_test.go`, same commit: `next --help` must say the Long's sentence and must not say "renewed by your comments"; `comment --help` must say its Short's words and must not say "renews its lease" (the pin that was at :407). The help of `claim` ("renew your hold on one", `--lease` "How long you hold it") is left: claiming again with the same name does extend the claim, so "renew your hold" is what `claim` does. |
| **M9 nit** | `ref_tasks.go`: "(an agent that only an agent-context variable such as CLAUDECODE identifies has none, so its list and show hints have no --as; a name a shell would not read as one word is written <name>)"; the paragraph is re-wrapped, no line is longer than before. A `MONOAGENT_ACTOR` agent has a name, and the hints carry it (task.go:167, task_read.go:303). |
| **Heuristic message** | `ref_tasks_gate_test.go:116` now ends "...which is the operator's (<the sentence>): split the sentence, or say 'operator' in it if it is about the operator" (mutant `xxvi` shows it). |

### Proof by mutation (a scratch export of `4244f8dc`; never in the tree)

Export: `git archive -o head.tar HEAD internal data cmd automations orgtemplates testdata AGENTS.md SECURITY.md CHANGELOG.md go.mod go.sum`, then `tar -x -C <new folder> -f head.tar`; script `.../scratchpad/t12r2/mutate.py`, log `battery.log`. Each mutant is applied to the export, the tests run, and the file is put back. The control (the reference tests and the help test) was green before, after every restore and at the end. **11 killed, 1 survived.** "LimitsTest" is `TestRefTasksStatesTheLimitsAndDefaultsTheCodeHas`, "GateTest" is `TestRefTasksSaysWhichCommandsTheGateRefusesAnAgent`, "HelpTest" is `TestTaskAgentCommandsExplainTheirOptionsInTheirHelp`.

| Mutant | Result | Killed by | First message |
|---|---|---|---|
| `i-loop-old-advice` (your i): the old advice back in `ref_tasks.go` | killed | LimitsTest | THE AGENT LOOP of `ref tasks` does not say "fewer than 30 minutes" (and the scan rejects "... comment before its last 30 minutes ...") |
| `ii-agents-old-advice` (your ii): the old advice back in AGENTS.md | killed | LimitsTest | AGENTS.md does not say "fewer than 30 minutes" (and the scan) |
| `iii-lease-help-renewed` (your iii): "renewed by your comments" back in the `--lease` help of `next` | killed | HelpTest | task next --help: the help still says "renewed by your comments" |
| `i-b-loop-fewer-than-dropped`: the new advice with "until 30 minutes of it remain" | killed | LimitsTest | THE AGENT LOOP of `ref tasks` does not say "fewer than 30 minutes" |
| `i-c-loop-old-advice-added`: the new advice and a sentence "Comment before its last 30 minutes." | killed | LimitsTest (the scan only) | a text gives a wrong account of what a comment does to a lease ...: "Comment before its last 30 minutes" |
| `ii-b-agents-fewer-than-dropped`: the same as `i-b` in AGENTS.md | killed | LimitsTest | AGENTS.md does not say "fewer than 30 minutes" |
| `ii-c-agents-old-advice-added`: the same as `i-c` in AGENTS.md | killed | LimitsTest (the scan only) | a text gives a wrong account of what a comment does to a lease ...: "Comment before its last 30 minutes" |
| `i-d-loop-comment-renews`: "A comment renews it." added to THE AGENT LOOP | killed | LimitsTest | a text gives a wrong account of what a comment does to a lease ...: "A comment renews it" |
| `iii-b-next-long-renewed`: the Long of `next` says "is renewed by your comments" again | killed | HelpTest | task next --help: the help does not say "a comment of yours extends it to 30 minutes from the comment, if that is later, and never shortens it", and still says "renewed by your comments" |
| `iii-c-comment-short-renews`: the Short of `comment` says "(renews its lease)" again | killed | HelpTest | task comment --help: the help does not say "extends its lease to 30 minutes from the comment, if that is later", and still says "renews its lease" |
| `xxvi-agents-list-names-approve`: `approve` joins the agent's list (the heuristic rule) | killed | GateTest | a sentence of WHO MAY DO WHAT lists what an agent may run and names `approve`, which is the operator's ("An AI agent may list tasks ... a session-start hook can call it"): split the sentence, or say 'operator' in it if it is about the operator |
| `m9-hint-env-variable`: "an agent that only an environment variable identifies" back | **survived** | | Expected: the brief asks for the wording only and no test reads that parenthesis. |

### Verification (HEAD `ba503047`; my files are those of `4244f8dc`)

- `gofmt -l cmd/monoagentcli`: nothing. `go vet ./cmd/monoagentcli/` and `go vet -tags nosocial ./cmd/monoagentcli/`: clean. `go build ./...`: clean.
- `go test ./cmd/monoagentcli/ -run '^(TestEveryTaskCommand|TestRefTasks|TestRootHelp|TestTask|TestAnAgent|TestOperator|TestTheOperator|TestEveryAgentContextMarker|TestFinishAndRelease|TestAgentCommandsNeed|TestNextTells|TestDigest)' -count=1 -timeout 15m`: ok (37.9 s; 169 tests PASS, 0 FAIL; the same selector passed on the same files before the commits, 41.8 s). The reference and help tests alone, `-count=3`: ok. `-tags nosocial` subset (the two legacy social tests, the reference tests, the help test): 22 PASS, 0 FAIL.
- Sizes: `ref_tasks.go` 312, `ref_tasks_test.go` 497, `ref_tasks_gate_test.go` 138, `task_agent.go` 329, `task_agent_text_test.go` 426 lines. The added lines are ASCII only. go.mod and go.sum untouched. The six `refTasksText` headings are unchanged (the anchors test passes).

### Notes and what I did not take

- **The scratch-export recipe** (re-review, New Breakage 2): three tests read `../../AGENTS.md` and `../../SECURITY.md`, so an export without them fails with `t.Fatal`, not with a mutant result. My exports since round 1 have had them (the recipe above; the "Mutation runs" recipe of the first report, above, does not say so). I also add `orgtemplates` and `testdata` because other tests of the package read files there (`library_roundtrip_test.go`, `org_grants_test.go`), which a wider run than the Task 12 selector needs. `task-13-mutation-brief.md:12` and the fix briefs name the shorter recipe; they are yours (the phase 1 plan's recipe was changed in `54e6f323`).
- **The M9 nit is not pinned** (survivor above), as the brief asks for the wording only.
- **Left as it is:** the loop's line comment and the AGENTS.md code-block comment "progress; extends the lease" (the re-review accepted both under I2: a comment does extend a lease that has fewer than 30 minutes left, which is every default lease after a moment); the Short and the Use of `claim` ("renew your hold on one"), which is what `claim` does.
- **The re-review's out-of-scope points stay with you:** the spec's §8 table (`task_comment` "renews the lease") and §7 ("or in the app"); the phase 4 plan appends `TestRefTasksSaysWhereChromeTasksLand` to `ref_tasks_test.go`, which is now at 497 lines (it must go into a new file); the four points of the phase 5 plan (rows for the `os` leaves, the section its text lands in, the operator gate before the platform check, and the `os` row that would pass by accident). I did not touch any plan file.
