# Task 11 report: the agent's commands (next, claim, comment, finish, release, digest)

## Step zero (the four results)

1. `pwd`: `/Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/agent-a5fb79b277f74704d` (a fresh agent worktree, not the controller's).
2. `git branch --show-current` (before anything else): `worktree-agent-a5fb79b277f74704d`.
3. The fast-forward worked, so the NEW-branch fallback (`git switch -c lane-t11 ...`) was not used: `git merge --ff-only 5e9d7c6c9ecdc58b5598b1aedc89c8302285ccf0` printed `Updating 5a009e8e..5e9d7c6c` / `Fast-forward` (77 files). The branch stays `worktree-agent-a5fb79b277f74704d`.
4. `git log --oneline -1` printed `5e9d7c6c Merge origin/master (49 commits) into the task board branch`; `ls cmd/monoagentcli/task_ops.go internal/tasks/claims.go` listed both.

Worktree: `/Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/agent-a5fb79b277f74704d`. Branch: `worktree-agent-a5fb79b277f74704d`. Commit: `7e1f52e7` (`7e1f52e760bee57e3a65a717ec1301785b2a15eb`), directly on top of `5e9d7c6c`. `git status --short` after it: empty. No file of `internal/tasks` is in the commit.

## What I implemented

`cmd/monoagentcli/task_agent.go` (336 lines): `newTaskNextCmd`, `newTaskClaimCmd`, `newTaskCommentCmd`, `newTaskFinishCmd`, `newTaskReleaseCmd`, `newTaskDigestCmd`, and the printers `printNext(w, p, t)`, `printClaimed(w, p, t, name)`, `continueHelp(w, p, t, name)` with exactly the signatures and texts of the brief. `task.go` grows by the six `AddCommand` lines and nothing else. The commands are thin callers of `Next`, `Claim`, `Comment`, `Finish`, `Release` and `Counts`; store errors go through `taskErr`; `comment`, `finish` and `release` print through Task 10's `writeOneTask`; the notes are printed by Task 9's `printNotes` and nowhere else.

New private helpers in `task_agent.go`: `agentNameRE`, `reservedAgentNames`, `agentNameError(name)`, `namedAgent(caller)`, `pasteName(name)`.

## Deviations from the brief, each with its reason

1. **No cobra `Args:` validators; the argument count is checked in `RunE`, after the caller is judged** (the order for the agent verbs is: name, arity, id, flags, then `withTasks`). Cobra's validators run before `RunE` and outside `withJSONErrors`: a wrong count would be exit 1 with no `--json` document (Task 10's deviation 1, same reason). Now a mistake is exit 3, `invalid_input`, with the document, found before the database opens. The messages repeat at most `cutArg` (64 runes) of an argument.
2. **The name is judged before the database is opened** (the dispatch's "refused before the database opens"). `caller.agent()` only catches "not an agent", "blank `--as`" and "no name"; `--as you`, `--as OS`, `--as "x;y"`, a 65-character name and `MONOAGENT_ACTOR=agent` all passed it and were refused by the store's `needName` only after `withTasks` had opened the database. `namedAgent(caller)` calls `agent()` and then `agentNameError`, a copy of the store's rule (alphabet `[A-Za-z0-9._#@:-]`, 1 to 64, and `you agent capture chrome os` reserved ignoring case). Used by `claim`, `finish`, `release`, `next --claim` and by `comment` when the caller is an agent. A test pins the two copies together (see Tests). `internal/tasks` has no exported validator and the dispatch forbids touching it.
3. **A name that starts with `#` is written as `<name>` in the four commands `continueHelp` prints.** The store accepts `#bot`; a shell reads `#` at the start of a word as a comment, so `finish 7 --as #bot --result ...` pasted would lose everything after the `#`. `pasteName` uses Task 9's `pasteableName` regexp (the rule `taskCommand` uses; I did not touch `task_read.go`). The brief's lines and their order are unchanged for every other name. The prose line "Use the same name (NAME)" still shows the real name.
4. **`finish` checks "exactly one of --result and --question" in the command, by the flags that were given** (`Changed("result") == Changed("question")`), before the database opens. The store still judges the cleaned text (a single flag with blank text is the store's refusal, tested). Without the command's check, `--result "" --question q` would pass as a question.
5. **`digest` never fails on an argument it does not take**: it writes one line to standard error and exits 0 with nothing on standard output (the brief's `cobra.NoArgs` would be exit 1 outside `RunE`). A failure at run time (database, profile) is the brief's one line on standard error and exit 0.
6. **`claim` prints the `Profile: NAME` line** in text (the brief printed none; spec 4.5, and `next --claim` and Task 10's commands print it). `next` uses `printProfileLine` (Task 10 refactor) instead of the brief's hand-written `Fprintf`: same text.
7. The local variable of the brief's `id` is named `taskID` (the test helper `id` is a package-level function; shadowing it was legal but confusing).

Compile slips: none. The brief's code and the brief's six tests compiled and passed unchanged (stage 1 below).

## The dispatch's facts and how they are handled

- (a) lease: `--lease` is a `time.Duration` flag (default 0, the store's 30 minutes; zero and negative mean the default; the store caps at 24h); the help of `next` and `claim` says "at most 24h". Tested through the CLI for `claim` and `next --claim`: none, `2h`, `90s`, `24h`, `48h`, `100000h`, `0`, `-5m`; a comment renews a 1-minute lease to about 30 minutes and never shortens a 3-hour one; a claim by the same name never shortens a lease.
- (b) `claimed` with `claimed_by` and `claimed_until` (the exact RFC 3339 of the holder's lease) exit 3; `not_ready` for an Inbox, Review, Done and archived task and for one the operator works on; `not_found` exit 2 for another profile's id and an unknown id; `limit` exit 3 at 2,000 events, with "leave it to the operator", and `next` skips that task (look and claim) and the digest does not announce it.
- (c) an agent's comment only on a task it claims (`not_claimant`); it renews the lease; the operator's comment is "you"; a marker, a blank `--as` (`""`, spaces, `--as=`) or `MONOAGENT_ACTOR` makes the comment an agent's and needs a name. Every one of the 27 markers of `orgsign.AgentContextMarkers()` is tested for `comment`: refused, naming the marker, nothing written.
- (d) `finish` takes exactly one of `--result` and `--question`, moves to Review; `release [--note]` back to Ready; both only by the claimant (another agent, an unheld task, a task already finished or released, a stale claim somebody took: `not_claimant`; the holder of a stale claim nobody took may still finish).
- (e) 8 KiB cut on a character with the truncation notice, for a comment, a result, a question and a release note; `limit` at 500 events for a comment (499 events: accepted, 500: refused), while `finish` and `release` still work.
- (f) `next` never offers the Inbox, Review, Done, archived cards, an operator-held task or a live claim; order: top of Ready by position, then the stale claim with the oldest lease.

## Tests

41 new top-level tests: the brief's six verbatim in `task_agent_test.go` (206 lines) and 35 of mine in seven files, all under 500 lines (`grep -nP '[^\x00-\x7F]'` on all eight `task_agent*` files: no output):

- `task_agent_helpers_test.go` (214): `leasedTask`, `leasedDoc`, `agentRun`, `agentLease`, `agentWithin`, `agentRefusal`, `agentSnapshot` (the whole board, read straight from the tables), `agentEvents`, `agentKinds`, `agentSeedEvents` (one transaction), `agentVerbsFor`, `marginLines`. No collision with the Task 8 to 10 helpers (`go vet` clean; mine are prefixed `agent`, plus `nextJSON` (the brief's), `digestJSON`, `untilClock`, `badAgentNames`).
- `task_agent_guard_test.go` (305): bad names refused before the database opens (5 verbs x 18 bad names, a database path under a regular file; the control: a good name gets the plain exit 1), no name / blank `--as` / marker / `MONOAGENT_ACTOR` before the database, nothing written, the agreement test with the store, wrong arguments before the database (21 rows, each with a name and without), bounded echo of a 10,000-character argument or name, the operator told "for AI agents", an agent's comment never the operator's (27 markers).
- `task_agent_rules_test.go` (353): a look never claims (6 callers, text and JSON, board unchanged), the order of `next`, 6 agents racing for 3 ready tasks, claim refusals, stale takeover and renewal, claimant-only finish/release/comment, what the history says, profile isolation.
- `task_agent_limits_test.go` (175): lease table, 2,000-event claim, 500-event comment, empty-after-cleaning text, the 8 KiB cut.
- `task_agent_digest_test.go` (150): digest only for Ready, the two exact lines (title cut at 80 characters, notes and link absent), never claims and says the same under a marker, never fails a hook (broken database, unknown profile, a stray argument, text and JSON), announces only what `next` would take.
- `task_agent_text_test.go` (344): the exact text of a claim (`claim` and `next --claim`) and of a look and of nothing ready, the hostile-notes test (below), `#` names, JSON key sets, the profile line of every command, the line of comment/finish/release, the help.

The test that matters most for the dispatch's safety rule, `TestTaskAgentTextTreatsWhatTheTaskSaysAsData`: a task of the profile "Work" (asked for by its name, so the commands must show the id `work-id`) whose notes contain `Take it:`, a fake `monoagentcli task approve 1`, the notice, the end line, a fake `Claimed #1 ...` line, a fake continuation block, an escape sequence, a bidi override, a tag character and a BOM. For `next`, `next --claim` and `claim`: no escape or hidden character in the output; the lines before the notice, the block and the lines after it are compared whole (the block is exactly the stored notes, each line indented; what follows is exactly the commands the printer writes); every `monoagentcli` outside the block carries `--profile work-id`; for a claim, every continuation line carries `--as bot-x`. `comment`, `release` and `finish` print two lines and none of the task's text.

Exact commands and results (final tree, commit `7e1f52e7`):

- `gofmt -l cmd/monoagentcli`: nothing. `go vet ./cmd/monoagentcli/`: clean. `go build ./...`: clean (no binary written).
- ONE regex for every test of Tasks 8 to 11: `^(TestTask|TestAnAgent|TestOperator|TestTheOperator|TestEveryAgentContextMarker|TestFinishAndRelease|TestAgentCommandsNeed|TestNextTells|TestDigest)`. `go test -list` selects 148 tests (the 107 of Tasks 8 to 10 and my 41), none outside the task board: the two names containing `Doc` are `...PrintTheDocumentsOfTheSpec`, no doctor test, and `TestFinishStreamedNoOpSaysAlreadyInstalled` (agent_install_test.go) is NOT selected because the prefix is `TestFinishAndRelease`, not `TestFinish`; the only `TestDigest`/`TestNext` tests are mine.
- `go test ./cmd/monoagentcli/ -run '<that regex>' -count=1 -v`: `ok ... 23.874s`; 148 top-level `--- PASS`, 0 `--- FAIL`, 130 subtests `--- PASS`, 0 `--- FAIL`.
- `go test -race` on the 41 new tests (`GORACE=atexit_sleep_ms=0`, regex in `scratchpad/lane-b/t11/env.sh`, exactly my 41): `ok ... 63.337s`. A first race run (76 s) before the last assertion was added to a digest test was also `ok`.
- Repeated 10 times (`-count=10`): the race test (`TestTaskAgentsRacing...`), the lease table and the exact-text tests: `ok`.
- Not run (not asked): `go test ./...` and the full package.

## TDD evidence

- Stage 1 (RED): the brief's six tests alone, `go test ./cmd/monoagentcli/ -run 'TestAnAgentWorks|TestFinishAndRelease|TestAgentCommandsNeed|TestTheOperatorsComment|TestNextTells|TestDigest' -count=1`: all six failed, e.g. `task_agent_test.go:19: task next --as bot-1: unknown command "next" for "task"`, `task_agent_test.go:64: task claim 1 --as bot: unknown command "claim" for "task"`, `task_agent_test.go:184: an empty board: ... err unknown command "digest" for "task"`. Expected: the commands did not exist (`scratchpad/lane-b/t11/red1.txt`).
- Stage 1 (GREEN): the brief's `task_agent.go` verbatim (apart from `taskID`) plus the six registrations: the six tests pass (`green0.txt`; the code is kept as `task_agent.go.stage1`).
- Stage 2 (RED): my other tests (34 of the 35: the 35th, the empty-after-cleaning test, was written after the first GREEN, for s02 below), run against that verbatim code (with a stub `agentNameError` returning nil so that they compile): 10 failed, each for the reason it exists: `TestTaskAgentVerbsRefuseABadNameBeforeTheDatabaseIsOpened` and `...AnAgentWithoutAUsableName...` (`task next --claim --as "you": exit 1, "initializing database: mkdir ..."; want exit 3, invalid_input and the word "reserved"`), `TestTaskAgentNameCheckAgreesWithTheStore` (the stub accepts the names the store refuses), `TestTaskAgentCommandsRefuseTheWrongArguments...` and `...RepeatOnlyAShortStretch...` (cobra's `Args`: exit 1), `TestTaskDigestNeverFailsAHook` (`unknown command "extra" for "task digest"`), `TestTaskClaimedTextIsExactly...` and `TestTaskAgentTextTreatsWhatTheTaskSaysAsData` and `TestTaskAgentCommandsNameTheProfileTheyActedOn` (`claim` printed no `Profile:` line), `TestTaskClaimedTextNeverWritesANameThatAShellWouldNotReadAsOne` (`... --as #bot` written into the commands) (`red2-all.txt`, `red2-guard.txt`).
- The other 24 of those 34 passed on the verbatim code: they characterise behaviour the store and the brief already had (lease, limits, order, claimant-only, text of the exact lines, profile isolation, hostile notes...). Their RED is the mutants below.
- GREEN: the final run above.

## Mutations (54, all killed, every file restored byte for byte)

Method: the nine files were staged; a driver (`scratchpad/lane-b/t11/runmut.py`, mutants in `mutants.txt`) applied one mutant to its file in place in this worktree, ran the new tests (`NEW`; `ALL` = the Task 8 to 11 regex for `task.go` and the store), restored the file with `git checkout -- <file>` from the index and compared its checksum. After the last mutant `shasum -a 256` of `task_agent.go`, `task.go` and `internal/tasks/claims.go` is identical to before the first one, `git diff` is empty and `git status` shows only the nine staged files; nothing mutated was ever committed. The logs are `mut-<id>.txt`, the summary `mutation-results.txt`. Five mutants are of the store (`internal/tasks/claims.go`), mutated in place and restored the same way; I did not edit it otherwise.

| # | Mutation | Killed by (count; the ones that matter) |
|---|---|---|
| m01 | `next --claim`: the name check removed | 5: AgentCommandsNeedAName, ...BadNameBeforeTheDatabase, ...WithoutAUsableNameBefore..., ...RefuseTheWrongArguments..., TheAgentVerbsAreForAgents |
| m02, m04, m05 | the same for `claim`, `finish`, `release` | 5 each, the same tests |
| m03 | the name check of an agent's `comment` removed | 3: the two before-the-database tests, CommentOfAnAgentIsNeverTheOperators |
| m06 | `namedAgent` drops `agentNameError` (only `agent()` judges) | 2: the before-the-database tests (nothing else sees it: the store refuses the name later, with the same code) |
| m07 | guard order: `claim` judges the name after the database opens | 3: the before-the-database tests |
| m08 | `next` without `--claim` claims | 10 (PeekNeverClaims, the brief's StartToFinish and NextTells...) |
| m09 | `next --claim` only looks | 9 |
| m10 / m11 | the lease flag ignored in `claim` / in `next --claim` | 2 / 1: the lease table |
| m12 | `finish`: the command's exactly-one check removed | 1: RefuseTheWrongArguments... (the store still refuses, so only the broken database shows it) |
| m13 | `finish`: only "neither" refused by the command | 1: the same (`--result "" --question q`) |
| m14 | result and question swapped | 3 |
| m15 | `release`: the note ignored | 2 |
| m16 | `comment`: only the first word sent | 1: WriteWhatTheAgentSaid |
| m17 | `comment`: an agent context with no name comments as the operator | 3: ...WithoutAUsableName..., ...WriteNothing, CommentOfAnAgentIsNeverTheOperators |
| m18 | `digest` returns its failure (exit non-zero) | 2: the brief's DigestIsSilent..., DigestNeverFailsAHook |
| m19 | `digest`: a stray argument is exit 3 | 1: DigestNeverFailsAHook |
| m20 | digest JSON key `ready` renamed | 5 |
| m21 | digest title not cut | 1: DigestIsTwoShortLines... |
| m22 | digest looks with a claim | 5 |
| m23 | `continueHelp` without `--profile` | 4 |
| m24 | `printNext` without `--profile` | 3 |
| m25 / m26 | notes printed raw in `printClaimed` / `printNext` | 3 / 3: AgentTextTreatsWhatTheTaskSaysAsData, the exact-text tests, NextTells |
| m27 | `pasteName` returns every name as it is | 1: ClaimedTextNeverWritesANameThatAShellWouldNotReadAsOne |
| m28 / m29 | the profile line removed from `claim` / `next` | 3 / 6 |
| m30 | claim JSON without `profile` | 1: PrintTheDocumentsOfTheSpec |
| m31 | `claim` returns the store's error without `taskErr` | 5 (exit 1 instead of 3, no code) |
| m32 | reserved labels compared case-sensitively | 3: includes the agreement test |
| m33 | `os` not reserved | 2: includes the agreement test |
| m34 | names of 65 characters pass | 3: includes the agreement test |
| m35 / m36 | `claim` accepts two ids / `comment` accepts no text | 1 / 1: RefuseTheWrongArguments... |
| m37 | `finish`: arguments checked before the caller | 1: the same (a name-less call must be refused for its name) |
| m38 | `printClaimed` without the link | 2 |
| m39 | `next` accepts extra arguments | 2 |
| m40 | `printNext`: the claim command has no id | 2 |
| m41 | `digest` not registered | 11 |
| m42 | `release` not registered | 19 |
| m43 | the claimed error does not name the holder | 3: StartToFinish, ClaimRefusals, TaskErrGivesEveryStoreRefusal... |
| m44 | the claimed error has an empty `claimed_until` | 2: ClaimRefusals, TaskErrGives... (the brief's `!= nil` check passes it) |
| m45 | `finish` line says "Finished" | 2 |
| m46 | a named agent's comment is stored as the operator's | 6 (the first version did not compile: `actor` unused; the re-run is the one counted) |
| m47 | the digest line carries the notes after the title | 2 (my first digest test could not have killed it: its title was longer than the cut, so notes appended after it were cut away; I saw that when planning the mutant and gave a second digest test a short title with notes and a link before running it) |
| m48 / m49 | `printNext` without the link / "Nothing to do." for nothing | 2 / 1 |
| s01 | store: a claim takes Inbox, Review, Done, archived tasks | 2: ClaimRefusals, WriteWhatTheAgentSaid |
| s02 | store: `finish` accepts both or neither | 1: CommentFinishWithTextThatIsEmptyOnceCleanedAreRefused (the command's check precedes it for flags; this is the only test of the store's side, written for it) |
| s03 | store: `next` offers Inbox tasks | 2: the order test, NothingReady |
| s04 | store: a look goes the way of a claim | 14 |
| s05 | store: any agent may finish/release/comment on a held task | 2: the brief's StartToFinish, ClaimantsAlone |

Two mutants first did not apply (m03, m30: my tab count in the mutant file); I fixed the mutant file and ran them, both killed. The kills were measured before one assertion was added to a digest test (the stronger test cannot un-kill them) and the final green run is on the committed tree.

## Self-review

- Read the whole diff: no stray file, no debug print, no commented-out code; scratch files live in the scratchpad (`lane-b/t11/`), the report here.
- `task.go`: six `AddCommand` lines only. No change to `task_read.go`, `task_ops.go` or `internal/tasks`.
- Every claim in the report was run on the final tree after the last edit (gofmt, vet, build, the list, the 148, the race run).
- Weaknesses found in my first drafts and fixed: m47 (digest test with a title longer than the cut, so notes appended after it were cut away: now a second test has a short title with notes and a link), s02 (the store's own exactly-one rule had no CLI test once the command checks flags first: `TestTaskCommentFinishWithTextThatIsEmptyOnceCleanedAreRefused`), the first `-3` argument row (cobra reads it as a flag; replaced by `3x`), the `until 12:00` text in a hostile note (the time normaliser would have rewritten it).

## Concerns (none blocks)

1. **The name rule exists twice** (the store's `checkAgentName` and the CLI's `agentNameError`); `TestTaskAgentNameCheckAgreesWithTheStore` pins them together over 77 names (the store is probed with an empty-board `Next` claim, which writes nothing). An exported `tasks.CheckAgentName` would remove the copy; it is a one-line change in the store lane, so I did not make it.
2. **`digest` "always exits 0" has one hole**: an unknown flag (`digest --bogus`) or an invalid flag value is cobra's own exit 1, as everywhere in the group. It holds for every run-time failure and for stray positional arguments. Task 12 should say it that way in the hook recipe.
3. **A look and `digest` do not judge `--as`** (`next --as you` works, `digest --as you` is ignored): they are reads, like `list` and `show`; the pre-database refusal covers the five verbs that act.
4. **`finish` counts flags, not text** (deviation 4): `--result "" --question q` is refused although the store alone would take it as a question.
5. **One timing-sensitive test**: `TestTaskAgentsRacingForTheReadyTasksGetOneEach` (6 goroutines, each opening the database and claiming) relies on SQLite's 5-second busy timeout on a loaded machine. It passed 10 repeated runs and the race run.
6. The claim output does not tell an agent to run `task show ID` to read the history, where the operator's answer to an earlier question lives (only the notes are printed). Not implemented: it is not in the brief.
7. `next` without `--claim` writes `<your-name>` in the "Take it" lines even when `--as NAME` was given (the brief's text; `printNext` has no name parameter).
8. `pasteName` repeats three lines of `taskCommand`'s switch; a refactor of `taskCommand` to use it would touch Task 9's file, so I left it.
9. The full package run (with its four known failures on a pristine macOS tree) and `go test ./...` were not run, as the dispatch says.

## Commit

One commit, directly on top of `5e9d7c6c`: `7e1f52e7` (`7e1f52e760bee57e3a65a717ec1301785b2a15eb`) `feat(tasks): the agent's commands: next, claim, comment, finish, release, digest`, with the trailer `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`. 9 files, 2,089 insertions: `cmd/monoagentcli/task.go` (+6), `task_agent.go`, `task_agent_test.go`, `task_agent_helpers_test.go`, `task_agent_guard_test.go`, `task_agent_rules_test.go`, `task_agent_limits_test.go`, `task_agent_digest_test.go`, `task_agent_text_test.go`. Staged with explicit paths in a separate call. `git status --short` after the commit: empty.

---

# Fix round 1 (review of Task 11: Approved, 6 Minor; all six taken)

## Step zero

`pwd` (`/Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/agent-a5fb79b277f74704d`) and `git branch --show-current` (`worktree-agent-a5fb79b277f74704d`) in two calls; `git merge --ff-only 588eb9e21bba4c7bb6ed075f5b9e30235c07c6c2` fast-forwarded `7e1f52e7..588eb9e2` (15 files: the store lane's `tasks.CheckAgentName` and its cleanup round); `git log --oneline -1` printed `588eb9e2 Merge the CLI lane (Task 11: ...) into the store lane`. Before touching anything I ran the 148-test regex once on the merged tree (the review's open point: nothing had run on it): `ok`, 148 top-level PASS, 0 FAIL.

New commit, directly on top of `588eb9e2`: `cb8f7e72` (`cb8f7e72a29701b87ec3bca313dfb52bfb14e6e3`) `fix(tasks): the CLI judges agent names with tasks.CheckAgentName; next names the caller in its hints; the agent commands' tests pin the guidance`, with the trailer. 4 files, +168 -109: `task_agent.go`, `task_agent_guard_test.go`, `task_agent_helpers_test.go`, `task_agent_text_test.go`. Nothing under `internal/tasks`, `task.go` unchanged. `git status --short` after the commit: empty. Branch `worktree-agent-a5fb79b277f74704d`.

## What I changed, per item

1. **`tasks.CheckAgentName` replaces the copy.** `agentNameRE`, `reservedAgentNames`, the `EqualFold` loop and the `regexp` import are gone. `agentNameError(name)` is `tasks.CheckAgentName(name)` wrapped with one guidance sentence that fits both kinds of refusal (I did not parse the store's message to choose between two): `taskErr(fmt.Errorf("%w (choose another name with --as NAME)", err))`, so it is still exit 3, `invalid_input`, and still judged by `namedAgent` before the database opens (the broken-database tests are unchanged and green). The message is now the store's words plus the way out: `invalid input: an agent may not be named "you": you, agent, capture, chrome and os are reserved (choose another name with --as NAME)` and `invalid input: an agent name is 1-64 characters of letters, digits and . _ # @ : - (choose another name with --as NAME)`. A parenthesis, not the brief's ": ", because the store's sentence ends in the alphabet's "-" and ": choose" read as "-: choose"; the old "(got NAME)" echo is gone (the store does not echo an invalid name, which also keeps the message short). `TestTaskAgentNameCheckAgreesWithTheStore` (the agreement test of the copy, with its database probe) is deleted and replaced by the small `TestTaskAgentNameIsJudgedByTheStoresRuleAndSaysHowToChooseAnother`: 84 names (66 + the 18 `badAgentNames`, both kinds, including a 100,000-character name, Cyrillic/zero-width/Kelvin look-alikes and the reserved labels in every case): the command refuses exactly when `tasks.CheckAgentName` does, and for a refusal it is exit 3, holds the store's complete message, holds "choose another name with --as NAME", is at most 300 bytes and valid UTF-8; and it fails if the table holds names of one kind only.
2. **Review Minor 1: `printNext` names the caller.** `printNext(w, p, t, as string)` (the caller's name, "" for none); `next` passes `caller.actor.Name`. A named agent's look prints `... task next --claim --as bot` and `... task claim 12 --as bot`; the operator, an agent context with no name and a blank `--as` still get `--as <your-name>`; a name a shell would not read as one word is `<name>` (taskCommand's rule). The digest keeps `<your-name>` (it does not judge `--as`; a test already checks that it says the same to everyone). Tests: `TestTaskNextTextShowsTheTaskAndHowToTakeIt` (exact text, now for the operator and for `--as bot`) and the hostile-notes test's tail for `next --as bot-x` were updated; new `TestTaskNextLookNamesTheCallerInTheCommandsItSuggests`, 15 callers: the operator; `--as`; `MONOAGENT_ACTOR`; `--as` wins over it; a marker and a name; an MCP-style name; 64 characters; a marker and no name; blank `--as` (empty, spaces); a blank `--as` that `MONOAGENT_ACTOR` fills; `x; echo hi`; `#bot`; `$(id)`; 65 characters. Each look must end with the two commands and carry exactly two `--as`.
3. **Review Minor 2: every suggested command is built by `taskCommand`.** `continueHelp` writes `taskCommand(p, name, "comment ID")` plus the quoted suffix, and likewise for the two finish lines and the release line; `printNext` and the digest use `takeCommand(p, as, words)`, the one new helper (`taskCommand` with the name, or `taskCommand` plus ` --as <your-name>` for none). `pasteName` and every hand-built `monoagentcli --profile` prefix are gone (the only `monoagentcli` left in `task_agent.go` is the recipe in the digest's help). The text is identical for every name: the exact-text tests that pin it (`TestTaskClaimedTextIsExactlyWhatTheAgentIsToldToDo`, `TestTaskClaimedTextNeverWritesANameThatAShellWouldNotReadAsOne`, the brief's `TestNextTellsAnAgent...`) pass unchanged.
4. **Review Minor 3.** (a) The two before-the-database tests now assert "--as NAME" in the message (the first: each of 5 verbs x 18 bad names, invalid and reserved; the second: every row), and the rule test above asserts the whole sentence. (b) `TestTaskNextAndClaimShowATitleWhole`: a title of exactly 200 characters (200 x e-acute) is shown whole on the first line of `next`, `claim` and `next --claim`.
5. **Review Minor 4.** The dead `marginLines` is deleted from `task_agent_helpers_test.go`.
6. **Review Minor 5, the help says what the commands do.** `next`: "Nothing ready: the text says so, and --json gives a null task."; `finish`: "The task goes to Review for the operator to read."; `digest` Short: "(in text, prints nothing when there are none)", Long: "In text, prints nothing when the profile has no ready task ... With --json it always prints its document. It always exits 0 whatever goes wrong at run time (a failure is one line on standard error), so a hook can call it". `TestTaskAgentCommandsExplainTheirOptionsInTheirHelp` pins the new sentences and that the old ones are gone (and checks the digest's Short through `task --help`).

Not done, as the review says (Minor 6, informational): the race test stays as it is.

## RED and GREEN

- RED: the updated tests against the unchanged production code (on the merged tree): 5 tests failed, each for its reason: `TestTaskAgentNameIsJudgedByTheStoresRuleAndSaysHowToChooseAnother` (`the refusal of the name "you": exit 3, "an agent may not be named ... are reserved labels: choose another name with --as NAME"; want exit 3, the store's words "invalid input: ..."`), `TestTaskNextTextShowsTheTaskAndHowToTakeIt` and `TestTaskNextLookNamesTheCallerInTheCommandsItSuggests` (11 of its 15 rows: `... --as <your-name>` where `--as bot` is wanted), `TestTaskAgentTextTreatsWhatTheTaskSaysAsData` (the look's tail), `TestTaskAgentCommandsExplainTheirOptionsInTheirHelp` (`the help ... does not say "Nothing ready: the text says so, and --json gives a null task"`, `the help still says "Nothing ready: the task is null"`). The guidance assertions of 3a and the title test passed on the old code: they pin what was already true (the old text had `--as NAME` and shows titles whole); their RED is the mutants below (f01, f03, f13, f14), as the review's scratch mutants ME and MI showed.
- GREEN: the same run after the change, `ok`.

## Checks on the committed code

- `gofmt -l cmd/monoagentcli`: nothing. `go vet ./cmd/monoagentcli/`: clean. `grep -nP '[^\x00-\x7F]'` on the eight `task_agent*` files: no output. Lines: `task_agent.go` 328, `..._test.go` 206, `_helpers_test.go` 202, `_guard_test.go` 303, `_rules_test.go` 353, `_limits_test.go` 175, `_digest_test.go` 150, `_text_test.go` 425: all under 500.
- The regex `^(TestTask|TestAnAgent|TestOperator|TestTheOperator|TestEveryAgentContextMarker|TestFinishAndRelease|TestAgentCommandsNeed|TestNextTells|TestDigest)`: `go test -list` selects **150** tests (the 148, minus the agreement test, plus the rule test, the caller-name table and the title test); `go test ./cmd/monoagentcli/ -run '<it>' -count=1 -v`: `ok ... 23.323s`, **150 top-level `--- PASS`, 0 `--- FAIL`, 145 subtests `--- PASS`, 0 `--- FAIL`** (130 before + the 15 caller rows).
- `go test -race` on the 43 tests of Task 11 (the new regex selects exactly the tests defined in the `task_agent*_test.go` files): `ok ... 63.446s`.
- Not run (not asked this round): `go build ./...` (the vet compiled the package; the commit changes no other package), `go test ./...`, the full package.

## Mutations (19, one or more per change, all killed, restored byte for byte)

Same method as before (changes staged, one mutant at a time applied in place, the 43 Task 11 tests run, file restored with `git checkout -- <file>` from the index; after the last one `shasum -a 256` of `task_agent.go` and `task.go` equals the value taken before the first, `git diff` is empty, `git status` shows only the four staged files). Logs and the table: `scratchpad/lane-b/t11/fix1/` (`mutants-fix1.txt`, `mutation-results.txt`, `mut-f*.txt`).

| # | Change | Mutation | Killed by |
|---|---|---|---|
| f01 | 1 | `agentNameError` without the way out (the store's error as it is) | 3: the rule test and both before-the-database tests (the guidance assertions of 3a) |
| f02 | 1 | `agentNameError` judges nothing | 3: the rule test and the two before-the-database tests |
| f03 | 1 | its own words instead of the store's | 3: the same |
| f04 | 1 | refuses a name the store takes (`bot`) | 26 |
| f05 | 2 | `printNext` gets "" instead of the caller's name | 3: the look tests and the hostile test |
| f06 | 2 | `takeCommand` ignores the name it is given | 3 |
| f07 | 2/3 | `takeCommand` writes the name as it is (a `;`, `$(` or leading `#` reaches the command) | 1: `TestTaskNextLookNamesTheCaller...` (only the new table sees it) |
| f08 | 2 | a caller with no name gets no `--as` at all (pasted, it would run as the operator) | 3: the look tests and the digest's exact text |
| f09 | 3 | `continueHelp`: the comment command carries no name | 4 |
| f10 | 3 | `continueHelp`: the release line is a finish | 3 |
| f11 | 3 | `continueHelp`: the finish lines name the profile by its name, not its id | 3 (the hostile test asks for the profile `Work`, id `work-id`) |
| f12 | 3 | `continueHelp`: the name written as it is (a `#bot` reaches the command) | 1: `...NeverWritesANameThatAShellWouldNotReadAsOne` |
| f13 | 4b | `printClaimed` cuts the title to 40 characters | 1: `TestTaskNextAndClaimShowATitleWhole` (the review's MI) |
| f14 | 4b | `printNext` cuts the title to 40 characters | 1: the same |
| f15 | 6 | the help of `next` says again "the task is null" | 1: the help test |
| f16 | 6 | the help of `finish` says again "for you to read" | 1: the same |
| f17 | 6 | the Short of `digest` drops "in text" | 1: the same |
| f18 | 6 | the Long of `digest` says "It always exits 0, so ..." again | 1: the same |
| f19 | 3 | the digest's command built by hand, without the profile | 3 |

Change 5 (dead code) has no behaviour to mutate: the package compiles and vets without `marginLines`.

## Concerns

None new. For Task 12 (docs), as the review lists: a marker-only `comment` is `invalid_input` (an agent's comment needs a name), not `operator_only`; `digest` is exit 1 for an unknown flag; `digest --json` prints its document even when nothing is ready; the refusal of an invalid or reserved name now reads `invalid input: <the store's words> (choose another name with --as NAME)`.
