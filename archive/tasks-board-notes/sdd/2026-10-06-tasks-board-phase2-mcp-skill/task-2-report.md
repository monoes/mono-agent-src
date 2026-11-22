# Phase 2, Task 2 report: the agent's view of a task, its refusals and its arguments

Status: DONE

Commit: `aeb43fe3 feat(tasks): the MCP view of a task: _untrusted fields, refusal codes, strict arguments`
(on branch `worktree-agent-afa471b7148789c23`; the harness had based the worktree on 5a009e8e, and `git merge --ff-only e3dd5557` fast-forwarded cleanly, so the commit sits directly on TIP e3dd5557).

## What I implemented

Exactly the brief, with no deviation and no compile slip:

- `internal/mcp/task_view_test.go` (new, 237 lines): the brief's Step 1 file. It holds the helpers `docKeys` and `asDoc` and the eight tests `TestATaskViewNamesEveryTextUntrusted`, `TestAnEventViewNamesItsNoteUntrusted`, `TestAListCutsLongNotesAndSaysWhere`, `TestTaskToolErrNamesTheCodeFirst`, `TestDecodeTaskArgsRefusesWhatATaskToolDoesNotTake`, `TestATaskIDIsANumberAsAModelWritesIt`, `TestANumberArgumentMayBeAString`, `TestAStatusArgumentIsOneColumnOrSeveral`.
- `internal/mcp/task_view.go` (new, 283 lines): the brief's Step 3 file. It holds `untrustedNote`, `listNotesRunes`, `taskView`, `eventView`, `viewOf`, `viewPtr`, `listView`, `listViews`, `cutListNotes`, `eventViews`, `taskListResult`, `taskResult`, `taskGetResult`, `taskToolErr` (with `codedErr`), `invalidArgs`, `decodeTaskArgs`, `taskIDArg` (`UnmarshalJSON`, `need`), `numberArg`, `statusArg`.

Proof that both files are the brief's text and nothing else: I cut the two fenced code blocks out of `task-2-brief.md` (lines 23-259 and 272-554) and ran `diff` against the two files: no difference in either (the extracted copies were scratch files in this folder and are deleted).

Nothing else was touched: not `task_actor_test.go`, not `tools.go`, not `server.go`.

## Checks I ran

- Raw non-ASCII check, both files: `grep -nP '[^\x00-\x7F]' <file>` printed nothing and exited 1 (no match) for `task_view_test.go` and for `task_view.go`. Positive control that `-P` works with the grep on this machine: `printf 'a\xc3\xa9b\n' | grep -nP '[^\x00-\x7F]'` printed `1:aéb` and exited 0. The one escape of the test file, `\U000000e9` in `TestAListCutsLongNotesAndSaysWhere`, is still an escape (`grep -c 'U000000e9'` finds it once).
- Size: 283 and 237 lines, under the 500-line limit.
- `gofmt -l internal/mcp`: nothing (exit 0).
- `go vet ./internal/mcp/`: nothing (exit 0).

## TDD evidence

RED: the test file alone, before `task_view.go` existed. Command (the brief's Step 2):

```
$ go test ./internal/mcp/ -run 'TestATaskView|TestAnEventView|TestAListCuts|TestTaskToolErr|TestDecodeTaskArgs|TestATaskID|TestANumberArgument|TestAStatusArgument' -count=1
# github.com/monoes/mono-agent/internal/mcp [github.com/monoes/mono-agent/internal/mcp.test]
internal/mcp/task_view_test.go:46:16: undefined: viewOf
internal/mcp/task_view_test.go:74:16: undefined: eventViews
internal/mcp/task_view_test.go:81:51: undefined: eventViews
internal/mcp/task_view_test.go:81:80: undefined: listViews
internal/mcp/task_view_test.go:89:40: undefined: listNotesRunes
internal/mcp/task_view_test.go:90:12: undefined: cutListNotes
internal/mcp/task_view_test.go:91:59: undefined: listNotesRunes
internal/mcp/task_view_test.go:93:9: undefined: cutListNotes
internal/mcp/task_view_test.go:98:10: undefined: listViews
internal/mcp/task_view_test.go:101:10: undefined: listView
internal/mcp/task_view_test.go:101:10: too many errors
FAIL	github.com/monoes/mono-agent/internal/mcp [build failed]
FAIL
```

The go command stops at ten errors, so I ran it again with `-gcflags=-e` to list every error, and counted the `undefined` symbols: `cutListNotes` (2), `decodeTaskArgs` (4), `eventViews` (2), `listNotesRunes` (2), `listView` (1), `listViews` (2), `numberArg` (3), `statusArg` (2), `taskIDArg` (4), `taskToolErr` (4), `viewOf` (2). That is every symbol the brief names, and the failure is the expected one (a compile failure, as the brief's Step 2 says: nothing existed yet).

GREEN, after `task_view.go` (the brief's Step 4 commands; `-v` added so the eight tests show):

```
$ gofmt -l internal/mcp                      (nothing, exit 0)
$ go vet ./internal/mcp/                     (nothing, exit 0)
$ go test ./internal/mcp/ -run 'TestATaskView|TestAnEventView|TestAListCuts|TestTaskToolErr|TestDecodeTaskArgs|TestATaskID|TestANumberArgument|TestAStatusArgument' -count=1 -v
=== RUN   TestATaskViewNamesEveryTextUntrusted
--- PASS: TestATaskViewNamesEveryTextUntrusted (0.00s)
=== RUN   TestAnEventViewNamesItsNoteUntrusted
--- PASS: TestAnEventViewNamesItsNoteUntrusted (0.00s)
=== RUN   TestAListCutsLongNotesAndSaysWhere
--- PASS: TestAListCutsLongNotesAndSaysWhere (0.00s)
=== RUN   TestTaskToolErrNamesTheCodeFirst
--- PASS: TestTaskToolErrNamesTheCodeFirst (0.00s)
=== RUN   TestDecodeTaskArgsRefusesWhatATaskToolDoesNotTake
--- PASS: TestDecodeTaskArgsRefusesWhatATaskToolDoesNotTake (0.00s)
=== RUN   TestATaskIDIsANumberAsAModelWritesIt
--- PASS: TestATaskIDIsANumberAsAModelWritesIt (0.00s)
=== RUN   TestANumberArgumentMayBeAString
--- PASS: TestANumberArgumentMayBeAString (0.00s)
=== RUN   TestAStatusArgumentIsOneColumnOrSeveral
--- PASS: TestAStatusArgumentIsOneColumnOrSeveral (0.00s)
PASS
ok  	github.com/monoes/mono-agent/internal/mcp	0.944s
```

The output holds nothing else: no log line, no warning.

Two more runs of the same package, for flakiness and for Task 1's tests, which share the package and the test helpers:

```
$ go test ./internal/mcp/ -run '<the same eight names>' -count=3 -race
ok  	github.com/monoes/mono-agent/internal/mcp	2.056s
$ go test ./internal/mcp/ -run 'TestClientLabel|TestTheTaskActor|TestTwoSessions|TestEveryClientName|TestConcurrentInitializes' -count=1
ok  	github.com/monoes/mono-agent/internal/mcp	1.003s
```

I ran only these targeted tests, not the repository's suite, as the rules say.

## Files changed

- `internal/mcp/task_view.go` (new, 283 lines)
- `internal/mcp/task_view_test.go` (new, 237 lines)

`git status --short` was clean after the commit (the worktree holds nothing untracked).

## Self-review

- Completeness: every item of the brief's "Produces" list exists with the name and shape the brief gives (constants, `taskView` with its 14 JSON names and `NotesUntrusted`, `eventView` with its 7, the six view functions, the three result documents, `taskToolErr`, `invalidArgs`, `decodeTaskArgs`, the three argument types, the two test helpers). All eight tests of the brief are in the file.
- Review focus 1 (a task's text under a plain name) and 4 (an argument the tool does not list is refused, never ignored) are the two that this task owns; `TestATaskViewNamesEveryTextUntrusted` pins the exact 14 keys of a task and `TestDecodeTaskArgsRefusesWhatATaskToolDoesNotTake` the refusal of `profile`, `as` and `ready`.
- I checked the one thing the brief's `taskToolErr` depends on in P1: the store builds `ClaimedError` in exactly one place (`internal/tasks/claims.go:197`) and never returns a bare `ErrClaimed`, so the `errors.As` case covers every claim refusal. I also grepped the store's error constructors for a task's title, notes or note text: none interpolates one, so "the store's words never hold a task's text" is true today.
- Quality: no stray file, debug print or commented-out code in the repository; my two scratch comparison files in this folder are deleted.

## Observations (none blocks; no code change made, the code is the brief's)

1. A bad `status` that goes through `decodeTaskArgs` will read `invalid_input: invalid input: unknown status "x" (use inbox, ready, ...)`. By reading the code, not by running it: `statusArg.UnmarshalJSON` returns `tasks.ParseStatus`'s error as it is (text `invalid input: unknown status ...`), `json.Unmarshal` passes it up unchanged, and `invalidArgs("%v", err)` puts the code in front of it. `taskToolErr` trims that repeat for the store's errors; `decodeTaskArgs` does not. It is readable and the prefix is right, so I left it. No test of this task covers the wording (the `{"title": 5}` case only checks the prefix, and the bad `status` values are tested through `json.Unmarshal` directly). Task 3's test of `task_list` with a bad status may want to pin the wording it wants; a one-line `strings.TrimPrefix(err.Error(), tasks.ErrInvalid.Error()+": ")` before `invalidArgs` would remove the repeat.
2. `viewPtr`, `taskListResult`, `taskResult`, `taskGetResult` and `untrustedNote` are not exercised by this task's tests: the brief says nothing calls them until Task 3, and Tasks 3 and 4 cover them through the tools. I added no test of my own for them, because a test name of mine could collide with one of the later tasks' test files, which I did not read.
3. `cutListNotes` writes the number in its notice as the text `1000`, separately from the constant `listNotesRunes`; `TestAListCutsLongNotesAndSaysWhere` pins the text (`[cut at 1000 characters`), so a change of the constant alone fails that test rather than going unnoticed.

---

# Fix round 1 (the review of aeb43fe3, the coordinator's rulings 1 to 5)

Status: DONE

Start: `git status --short` printed nothing and `git log --oneline -3` ended at aeb43fe3 (worktree `agent-afa471b7148789c23`).

Commits (new commits on my branch, nothing amended; the controller moves them):

- `de2213b4 test(tasks): the task view tests pin the fields inside claim and last_event and the values they left free` (test only: rulings 1 and 2)
- `3fb24912 fix(tasks): a refused task argument names what is wrong in a model's words, an unknown one names all of them and what the tool takes, a whole number may be a float` (production and its tests: rulings 3 and 4)

Only `internal/mcp/task_view.go` (283 to 372 lines) and `internal/mcp/task_view_test.go` (237 to 477 lines) changed. Rulings 5 (Minor 4, the idempotence of `taskToolErr`, and Minor 5, the split of the file) are parked as told: `taskToolErr` is untouched and nothing moved to another file. `task_actor_test.go`, `tools.go`, `server.go` and the plan are untouched.

## Ruling 1 and 2 (test only, commit de2213b4)

- `TestATaskViewNamesEveryTextUntrusted` now also pins the key set of `claim` (`by, stale, until`) and of `last_event` (`actor, at, kind`), with a comment saying why: P1's `Claim` and `LastEvent` go out whole under plain names, so a text field added to either fails here until someone decides what to call it. Four different times (created, changed, the last event's, the lease's) so that a view that mixes two up is seen; `position` is asserted; the claim has `Stale: true` so that the flag is told from its zero value; `last_event` has its own actor, kind and time.
- `TestAnEventViewNamesItsNoteUntrusted` now takes two events, a comment and a claim with `from_status` ready, `to_status` in_progress, distinct ids and times, and asserts all seven fields of both.
- New small test `TestAViewThroughAPointerIsTheViewOrNothing` (so that the review's s07, `viewPtr` without its nil guard, dies here and not only in Task 3's `task_next` test): `viewPtr(nil)` is nil and `viewPtr(&task)` is `viewOf(task)`. Its name is not in the plan, I checked.

Mutants, run the same way on the old committed test (before) and on the new one (after); the same 15 mutants, `<id> :: what it does`:

| mutant | before (the tests of aeb43fe3) | after (de2213b4 and 3fb24912) |
| --- | --- | --- |
| s01 `viewOf` drops Position | SURVIVED | KILLED by TestATaskViewNamesEveryTextUntrusted |
| s02 drops LastEvent | SURVIVED | KILLED by TestATaskViewNamesEveryTextUntrusted |
| s03 swaps CreatedAt and UpdatedAt | SURVIVED | KILLED by TestATaskViewNamesEveryTextUntrusted |
| s04 drops UpdatedAt | SURVIVED | KILLED by TestATaskViewNamesEveryTextUntrusted |
| s05 `eventViews` swaps FromStatus and ToStatus | SURVIVED | KILLED by TestAnEventViewNamesItsNoteUntrusted |
| s06 `eventViews` drops ID and At | SURVIVED | KILLED by TestAnEventViewNamesItsNoteUntrusted |
| s07 `viewPtr` returns a view of the zero task for nil | SURVIVED | KILLED by TestAViewThroughAPointerIsTheViewOrNothing |
| s07b `viewPtr` has no nil guard at all | SURVIVED | KILLED (panic) in the same test |
| i01 a free-text `note` field added to `tasks.Claim` AND `tasks.LastEvent` (in the scratch copy of model.go) | SURVIVED | KILLED by TestATaskViewNamesEveryTextUntrusted |
| i02 the same in `Claim` only | SURVIVED | KILLED, same test |
| i03 the same in `LastEvent` only | SURVIVED | KILLED, same test |
| i04 a `Claim` field renamed (until to expires) | KILLED | KILLED |
| c01 `title_untrusted` becomes `title` | KILLED | KILLED |
| c02 the event's `note_untrusted` becomes `note` | KILLED | KILLED |
| c06 `cutListNotes`: `<=` becomes `<` | KILLED | KILLED |

Logs: `mut_round_a_before.log` (old tests), `mut_final_a.log` (the committed files), in this folder.

## Ruling 3 and 4 (production, commit 3fb24912)

`decodeTaskArgs` now (in `task_view.go`): checks first that dst is a non-nil pointer to a struct (a plain error `decodeTaskArgs: dst is %T, want a pointer to a struct`, not `invalid_input`, and also when the arguments are empty); builds the table of the tool's own argument names from the json tags up to the first comma, skipping the names `""` and `"-"`; takes the names the model sent in sorted order; if any is not in the table it refuses with all of them, quoted and sorted, and the tool's own names, sorted; otherwise it decodes each argument into its own field (so a refusal can name it) and returns the first refusal in name order. New helpers: `refuseUnknown`, `refuseValue`, `kindWords`, `floatForm`, `wholeNumber`.

What a model reads now (`before` is by the RED run below, `after` by the GREEN run):

| the call | before | after |
| --- | --- | --- |
| `{"limit": "x"}` | `invalid_input: expected a whole number, such as 20` | `invalid_input: limit: expected a whole number, such as 20` |
| `{"id": "x"}` | `invalid_input: id is a task's number, such as 12` | `invalid_input: id: expected a task's number, such as 12` |
| `{"status": 5}` | `invalid_input: status is a column name or a list of them` | `invalid_input: status: expected a column name or a list of them` |
| `{"text": 5}` | `invalid_input: json: cannot unmarshal number into Go struct field args.text of type string` | `invalid_input: text: expected a string` |
| `{"next": "yes"}` | `invalid_input: json: cannot unmarshal string into Go struct field args.next of type bool` | `invalid_input: next: expected true or false` |
| `{"status": "someday"}` | `invalid_input: invalid input: unknown status "someday" (use inbox, ready, ...)` | `invalid_input: unknown status "someday" (use inbox, ready, ...)`, the text `taskToolErr` gives the same store error |
| `{"profile": "work"}` | `invalid_input: "profile" is not an argument of this tool: the task tools take only what they list (this server serves one profile and names its caller itself)` | `invalid_input: "profile" is not an argument of this tool: it takes only id, title (this server serves one profile and names its caller itself)` |
| `{"ready": true, "as": "you", "profile": "w"}` | one of the three, whichever the map gave first | `invalid_input: "as", "profile", "ready" are not arguments of this tool: it takes only ...` |
| `{"ID": 3}` | `"ID" is not an argument of this tool` | the same, with `it takes only id, title` in it (lower case, so the model sees the spelling); I did not add a special sentence for the case |
| `{"": 1}`, `{"-": 1}` | accepted and ignored | refused as unknown names |
| a tool that takes none, any argument | the old sentence | `... is not an argument of this tool: it takes no arguments (this server serves one profile ...)` |
| `20.0`, `1e1`, `"12.0"`, `"#12.0"` | refused | accepted as 20, 10, 12, 12 |
| `20.5` | refused | refused |

(d), how a whole number is read, and the amended reading of the plan's Ruling 4 (the plan file is not edited):

> Ruling 4, amended reading. `id` takes `12`, `"12"` and `"#12"`, and the same written as a float that holds a whole number (`12.0`, `1.2e1`, `"12.0"`, `"#12.0"`). `limit` and `lease_minutes` take a whole number, as a number or as a numeric string, plain or written as a float (`20`, `"20"`, `20.0`, `1e1`, `"20.0"`). A fraction (`20.5`, `1e-1`) is refused, as is anything that is not a plain decimal number (`1_0`, `0x1p4`, `4/2`, `Inf`, `NaN`, `.5`, `5.`). The reading is exact, with no float in it (`9007199254740993.0` is 9007199254740993), and bounded: at most 20 digits in the integer part, 20 in the fraction and 3 in the exponent (so a text of absurd length costs nothing, and zero-padded numbers of more than 20 digits, which `ParseInt` took, are refused), and a value that does not fit an int64 is refused. Everything else of Ruling 4 stands: `status` as before, `limit` 0 or less is 50 and over 200 is 200, `lease_minutes` 0 or less is 30 minutes and over 1440 is 1440, bounded before it is multiplied (Tasks 3 and 4). Every refusal of a value names its argument.

Why exact (`math/big`, the standard library) and not `strconv.ParseFloat`: I first probed the float way (`<scratchpad>/probe`): it needs a grammar guard anyway (`ParseFloat` alone takes `1_2`, `0x1p4`, `Inf`, `NaN`, `.5`), it silently turns `9007199254740993.0` into 9007199254740992 (so it needs a 2^53 bound) and `1e-400` into 0 (a fraction taken for a whole number). `big.Rat` after the same grammar guard has neither fault and is as short. The regexp bounds are what keeps `big.Rat` from being fed a hostile text (`0e999999` would otherwise make it compute 5^999999).

Where the new code is not what the brief gave, and why (each is in the coordinator's rulings): `taskIDArg` and `statusArg` lost their own name from their error text (`id is a task's number...` is now `expected a task's number...`), because `decodeTaskArgs` puts the argument's name in front of it and the name would show twice; `numberArg` and `taskIDArg` no longer call `strconv.ParseInt` (they call `wholeNumber`), and `decodeTaskArgs` no longer decodes the whole object at once but one argument at a time.

Impact on the later tasks, which I did not read as tasks: I grepped the plan file for the call sites of `decodeTaskArgs`, the phrases I reworded, `invalid_input` and the test names (to avoid a name collision), and read two short passages around hits (about 40 lines at the end of Task 6's `TestTaskToolsAndCommandsRefuseWithTheSameCode` and 14 lines of Task 3's `task_list` test). The later tests pin only the prefix `invalid_input: ` (task_list with a bad status, task_next with `claim: true`, task_add, task_claim `invalid_input: give id`), `invalid_input: id is required` (`need()`, unchanged) and codes; none pins the old sentences. The call `decodeTaskArgs(args, &struct{}{})` of `task_next` is a pointer to a struct and works (a tool that takes none says `it takes no arguments`). None of my eight new test names (the six of the production commit, `TestANullArgumentIsNoArgument` and `TestAViewThroughAPointerIsTheViewOrNothing`) is in this plan or in any other plan under `docs/mastermind/plans` (`grep -c`: 0 in every file), and each is defined once in the repository.

## Tests added (all in `task_view_test.go`; each has a mutant below)

- `TestARefusedArgumentIsNamedInTheModelsWords` (b): every kind (taskIDArg, numberArg, statusArg, string, bool, int, a list) by exact text, and the same bad value named every time when three are bad (40 tries, since a map's order is random).
- `TestAStatusRefusalReadsTheSameThroughEveryPath` (a): the text equals `taskToolErr(store error)`, for a string, a comma list and a list.
- `TestEveryUnknownArgumentIsNamedWithThoseTheToolTakes` (c): one, several in byte order (`"Title"` before `"as"`), a wrong-case name, a tool that takes none; 40 tries each.
- `TestAWholeNumberMayBeWrittenAsAFloat` (d): accepted forms for a number and an id, exact values (`9007199254740993.0`, the int64 limits), 24 refused texts for both types, and the ids that are not positive.
- `TestOnlyTheTagsOfADstAreArguments` and `TestADstThatIsNotAPointerToAStructIsRefused` (Minor 3).
- `TestANullArgumentIsNoArgument`: not asked; the refactor of the decoding to one field at a time had to keep `null` (models often send it for an argument they do not use) working for every kind, so I pinned it. It kills no mutant of its own.

## TDD evidence

RED: the new tests (commit 3fb24912's test file) against the production code of de2213b4:

```
$ go test ./internal/mcp/ -run 'TestARefusedArgument|TestAStatusRefusal|TestEveryUnknownArgument|TestAWholeNumberMay|TestOnlyTheTagsOfADst|TestADstThatIsNot' -count=1
--- FAIL: TestARefusedArgumentIsNamedInTheModelsWords (0.00s)
    task_view_test.go:316: {"next": "yes"}: invalid_input: json: cannot unmarshal string into Go struct field args.next of type bool, want "invalid_input: next: expected true or false"
    task_view_test.go:316: {"limit": "x"}: invalid_input: expected a whole number, such as 20, want "invalid_input: limit: expected a whole number, such as 20"
    ... (12 lines of this kind; the status, id, text, count and list rows too)
--- FAIL: TestAStatusRefusalReadsTheSameThroughEveryPath (0.00s)
    task_view_test.go:342: {"status": "someday"}: invalid_input: invalid input: unknown status "someday" (use inbox, ready, in_progress, review, done or archived), want the store's words once: ...
--- FAIL: TestEveryUnknownArgumentIsNamedWithThoseTheToolTakes (0.00s)
    task_view_test.go:365: one: invalid_input: "profile" is not an argument of this tool: the task tools take only what they list (...), want "invalid_input: \"profile\" is not an argument of this tool: it takes only id, title (...)"
--- FAIL: TestAWholeNumberMayBeWrittenAsAFloat (0.00s)
    task_view_test.go:382: a number of 12.0: 0, expected a whole number, such as 20; want 12
    ... (18 lines of this kind, with the ids and the exact values) and
    task_view_test.go:404: a number of "000000000000000000012": 12, <nil>; want a refusal that asks for a whole number
--- FAIL: TestOnlyTheTagsOfADstAreArguments (0.00s)
    task_view_test.go:436: {"":"x"}: <nil>, want it to start "invalid_input: \"\" is not an argument of this tool: it takes only id, note"
    task_view_test.go:436: {"-":"x"}: <nil>, want it to start "invalid_input: \"-\" is not an argument of this tool: it takes only id, note"
    ... (the other four names are refused, with the old sentence)
--- FAIL: TestADstThatIsNotAPointerToAStructIsRefused (0.00s)
panic: reflect: NumField of non-struct type int [recovered, repanicked]
FAIL	github.com/monoes/mono-agent/internal/mcp	1.112s
```

Each failure is one the rulings describe (the doubled words, Go's own text, the unnamed argument, the old sentence for the unknown arguments, floats refused, `{"":1}` and `{"-":1}` ignored, the panic on a dst that is no struct). (The RED run was made before I added the `tags` row, the `0e1000` and `0e999999` texts and the null test; those went in with the same commit and were written to be killed by a mutant each.)

GREEN, on the committed code (HEAD 3fb24912):

```
$ gofmt -l internal/mcp                          (nothing, exit 0)
$ go vet ./internal/mcp/                         (nothing, exit 0)
$ go test ./internal/mcp/ -run 'TestATaskView|TestAnEventView|TestAListCuts|TestTaskToolErr|TestDecodeTaskArgs|TestATaskID|TestANumberArgument|TestAStatusArgument' -count=1
ok  	github.com/monoes/mono-agent/internal/mcp	1.455s
```
(the brief's command, which selects the eight tests of the brief) and the same with all 16 task view tests, the five of Task 1 added, `-v`:

```
$ go test ./internal/mcp/ -run '<the 16 task view tests and Task 1's five>' -count=1 -v
--- PASS: TestClientLabel
--- PASS: TestTheTaskActorIsNamedAfterTheClientInInitialize
--- PASS: TestTwoSessionsOfOneClientAreTwoClaimants
--- PASS: TestEveryClientNameMakesAClaimableActor (11.50s, the load of the machine)
--- PASS: TestConcurrentInitializesAndTaskCallsShareOneActor
--- PASS: TestATaskViewNamesEveryTextUntrusted
--- PASS: TestAViewThroughAPointerIsTheViewOrNothing
--- PASS: TestAnEventViewNamesItsNoteUntrusted
--- PASS: TestAListCutsLongNotesAndSaysWhere
--- PASS: TestTaskToolErrNamesTheCodeFirst
--- PASS: TestDecodeTaskArgsRefusesWhatATaskToolDoesNotTake
--- PASS: TestATaskIDIsANumberAsAModelWritesIt
--- PASS: TestANumberArgumentMayBeAString
--- PASS: TestAStatusArgumentIsOneColumnOrSeveral
--- PASS: TestARefusedArgumentIsNamedInTheModelsWords
--- PASS: TestAStatusRefusalReadsTheSameThroughEveryPath
--- PASS: TestEveryUnknownArgumentIsNamedWithThoseTheToolTakes
--- PASS: TestAWholeNumberMayBeWrittenAsAFloat
--- PASS: TestOnlyTheTagsOfADstAreArguments
--- PASS: TestANullArgumentIsNoArgument
--- PASS: TestADstThatIsNotAPointerToAStructIsRefused
PASS
ok  	github.com/monoes/mono-agent/internal/mcp	13.899s

$ go test ./internal/mcp/ -run '<the 16 task view tests>' -count=3 -race
ok  	github.com/monoes/mono-agent/internal/mcp	7.897s
```

Output pristine in all of them. The non-ASCII check (`grep -nP '[^\x00-\x7F]'`) prints nothing for either file (exit 1).

## Mutants of the production part (commit 3fb24912, run on the committed files): 45 of 45 killed

Scripts and logs in this folder: `mutlib.sh`, `mut_round_b.sh`, `mut_final_b.log`. Each line is one mutant, a literal replacement of the first occurrence in `task_view.go`, one at a time, the file restored after each (the script ends by checking that it is identical to the committed file).

- (a) the store's words once: a1 `return taskToolErr(err)` becomes `invalidArgs("%v", err)` (the doubled words are back); a2 the ErrInvalid arm skipped. Killed by `TestAStatusRefusalReadsTheSameThroughEveryPath` (a2 also by `TestTaskToolErrNamesTheCodeFirst`).
- (b) named, in a model's words: b1 a type's own words lose the name; b2 the wrong-JSON-type arm skipped (Go's text shows); b3 the wrong-type refusal loses the name; b4, b5, b6, b7 the words of `kindWords` for string, bool, int and the fallback change; b8 the arguments taken in map order (not sorted); b9, b10, b11 the words of `numberArg`, `taskIDArg`, `statusArg` change. All killed by `TestARefusedArgumentIsNamedInTheModelsWords` (b8 also by the unknown-arguments test; b9 also by two more).
- (c) every unknown argument and what the tool takes: c1 only the first unknown named; c3 the tool's names not listed; c4 the tool's names in map order; c5 a name matches in any case; c6 the singular changes; c6b the plural joins with a space; c7 the reason (one profile) dropped; c8 the no-argument sentence changes; c9 the names not quoted; c10 the unknown block skipped. All killed by `TestEveryUnknownArgumentIsNamedWithThoseTheToolTakes` (c7, c9 and c10 also by the brief's `TestDecodeTaskArgsRefusesWhatATaskToolDoesNotTake`).
- (d) a whole number as a float: d1 the fraction no longer part of the form; d2 the exponent no longer part; d3 `IsInt` check removed (12.5 would read as 25); d4 the int64 check removed; d5, d5b, d5c the three bounds of the regexp removed one at a time; d6 the form not checked before `big.Rat` reads the text (`4/2`, `0x1p4`); d7 an id of 0 taken; d8 `numberArg` and d9 `taskIDArg` back to `strconv.ParseInt`; d10 a plus sign no longer taken; d11 an exponent sign no longer taken. All killed by `TestAWholeNumberMayBeWrittenAsAFloat` (d3 and d7 also by the brief's tests).
- Minor 3: m1 the name `""` taken; m2 the name `-` taken; m3 the dst check removed (killed by a panic); m4 the dst checked only when arguments are given; m5 the dst refusal blamed on the model (`invalid_input`); m6 the tag's options part of the name; m7 a pointer to anything but a struct taken (panic); o1 every argument goes to the first field; o2 empty arguments not accepted. Killed by `TestOnlyTheTagsOfADstAreArguments`, `TestADstThatIsNotAPointerToAStructIsRefused`, `TestARefusedArgument...` and the brief's decode test.

## How the mutants were run (a deviation from the rules, and why)

The rules say to export the committed directories and run mutants there. I did, once (`git archive HEAD internal data automations go.mod go.sum AGENTS.md SECURITY.md CHANGELOG.md` into `t2mut`): its baseline passed and s01 and s02 were killed in the whole `internal/mcp` package. But the test binary of that package links 87 repository packages, the load average of the machine was above 70, and a mutant took minutes, so I stopped that run (no process left behind) and ran all mutants in a smaller scratch module, `t2mini`: the real `internal/tasks` package exported from the commit (it imports the standard library only; `diff -rq` against the worktree: identical), the committed `task_view.go` and `task_view_test.go` (`git archive HEAD` of those two paths, `cmp` against the worktree: identical) and `equalStrings` copied verbatim from `api_only_test.go` (the only helper of that file the tests use). Nothing of this touched the worktree. The stale `t2mut` folder is still in this folder, unused.

## Self-review of the fix round

- Completeness: every point of rulings 1 to 4 is done and has a test and a mutant; ruling 5 is untouched.
- `internal/mcp/task_view.go` 372 lines, `task_view_test.go` 477: under 500.
- Observation 1 of the first report is gone (the doubled words); observation 2 is half answered (`viewPtr` has a test now; the three result documents and `untrustedNote` still have none here, Tasks 3 and 4 cover them); observation 3 stands.
- Not changed, and worth knowing: an unknown argument's name is echoed in full, quoted (`strconv.Quote` turns control and bidi characters into escapes), as before for one name; the largest request line the server reads is 17 MiB (`maxRequestLineBytes`), so the echo is bounded by that and not by anything of ours. `{"status": "ready, someday"}` still reads `unknown status " someday"` (the store echoes the piece as it was split, with its space); I used `"ready,someday"` in the test and left `statusArg` alone.
- A refusal of two kinds at once reports the unknown names first (the structure of the call before the values in it).
- A gap I know and left (no tool is affected): the new float rule lives in `numberArg` and `taskIDArg`. A plain `int` field of an argument struct does not get it: `{"count": 20.0}` into the fixture's `Count int` reads `count: expected a whole number`, although `20.0` is a whole number to every other argument. No task tool takes a plain int (the plan's tools use `numberArg`, `taskIDArg`, `statusArg`, string and bool), so I changed no code; a tool that ever wants a number must use `numberArg`.
- Process deviations, for the controller: the mutants ran in the reduced scratch module (above); I read about 55 lines of the plan below its header (the two passages named above) besides the greps; the rest of the plan I did not read.
