# Phase 2, Task 1 report: who the task tools act as (spec D18)

Status: DONE

Commit: `da632d6f feat(tasks): the MCP server names its caller after the client, agent:<client>#<4 hex>`
(on branch `worktree-agent-a8727e544ca24ff4c`, started from TIP 3c7bc612 by `git merge --ff-only`; the worktree was based on 5a009e8e and fast-forwarded cleanly).

## What I implemented

Exactly the brief, with no deviation:

- `internal/mcp/task_actor.go` (new, 87 lines): `maxClientLabel = 53`, `var newActorSuffix`, `clientLabel`, `(*Server).recordClient`, `(*Server).taskActor`, copied verbatim from the brief's Step 3.
- `internal/mcp/server.go`: the three edits of Step 4, as three separate Edit calls: the `Server` struct gained `clientMu sync.Mutex`, `clientName`, `actorName`, `actorSuffix string` (after `rtMu`, with the brief's comment); `NewServer` returns `&Server{opts: opts, actorSuffix: newActorSuffix()}`; the `initialize` case of `handleLine` calls `s.recordClient(req.Params)` before it answers. The initialize answer itself is unchanged.
- `internal/mcp/task_actor_test.go` (new, 128 lines): the brief's Step 1 file verbatim: `TestClientLabel`, the helpers `withSuffixes` and `initializeAs`, `TestTheTaskActorIsNamedAfterTheClientInInitialize`, `TestTwoSessionsOfOneClientAreTwoClaimants`, `TestEveryClientNameMakesAClaimableActor`.

Compile slips: none. The brief's code compiled as written; `gofmt -l internal/mcp` listed nothing (the map literal's alignment in the test was already right), and `go vet ./internal/mcp/` is clean. The `taskActor` method is called by nothing but the tests, as intended (Task 4 uses it).

Checks that the brief names and I ran:

- Raw non-ASCII check on the test file, as the rules require: `grep -nP '[^\x00-\x7F]' internal/mcp/task_actor_test.go` printed nothing and exited 1 (no match; `-P` works with this grep, shown by a positive control: the same command on a scratch file holding a raw "e acute" printed the line and exited 0). I cross-checked with `perl -ne 'print "$.:$_" if /[^\x00-\x7F]/'`: nothing printed. The three escapes stay escapes (lines 30, 31 and 111 hold `\U000065e5\U0000672c` and `\U0000202eevil` as text).

## TDD evidence

RED, before `task_actor.go` and the `server.go` edits existed:

```
$ go test ./internal/mcp/ -run 'TestClientLabel|TestTheTaskActor|TestTwoSessionsOfOneClient|TestEveryClientName' -count=1
# github.com/monoes/mono-agent/internal/mcp [github.com/monoes/mono-agent/internal/mcp.test]
internal/mcp/task_actor_test.go:32:33: undefined: maxClientLabel
internal/mcp/task_actor_test.go:34:13: undefined: clientLabel
internal/mcp/task_actor_test.go:43:9: undefined: newActorSuffix
internal/mcp/task_actor_test.go:45:2: undefined: newActorSuffix
internal/mcp/task_actor_test.go:50:21: undefined: newActorSuffix
internal/mcp/task_actor_test.go:66:12: s.taskActor undefined (type *Server has no field or method taskActor)
internal/mcp/task_actor_test.go:73:18: early.taskActor undefined (type *Server has no field or method taskActor)
internal/mcp/task_actor_test.go:77:18: early.taskActor undefined (type *Server has no field or method taskActor)
internal/mcp/task_actor_test.go:84:17: bare.taskActor undefined (type *Server has no field or method taskActor)
internal/mcp/task_actor_test.go:90:10: undefined: newActorSuffix
internal/mcp/task_actor_test.go:90:10: too many errors
FAIL	github.com/monoes/mono-agent/internal/mcp [build failed]
```

A compile failure is the expected RED here (the symbols did not exist yet), as the brief's Step 2 says.

GREEN, after the implementation (the brief's Step 5 commands, run unfiltered, with `2>&1` added):

```
$ gofmt -l internal/mcp                      (nothing, exit 0)
$ go vet ./internal/mcp/                     (nothing, exit 0)
$ go test ./internal/mcp/ -run 'TestClientLabel|TestTheTaskActor|TestTwoSessionsOfOneClient|TestEveryClientName|TestServer' -count=1 2>&1
ok  	github.com/monoes/mono-agent/internal/mcp	3.727s
```

That one line is the whole output: no stray log line, no warning. The per-test listing below is from an earlier run of the same command with `-v`, filtered to the `---`, PASS, FAIL and `ok` lines (2.594s):

```
--- PASS: TestClientLabel (0.00s)
--- PASS: TestTheTaskActorIsNamedAfterTheClientInInitialize (0.00s)
--- PASS: TestTwoSessionsOfOneClientAreTwoClaimants (0.00s)
--- PASS: TestEveryClientNameMakesAClaimableActor (0.08s)
... and the 24 existing TestServer* tests, all PASS (TestServerInitialize, TestServerInitializeInstructions, TestServerToolsList, ...)
PASS
ok  	github.com/monoes/mono-agent/internal/mcp	2.594s
```

28 tests (4 new + 24 `TestServer*`), all passing.

## Files changed

- `internal/mcp/task_actor.go` (new)
- `internal/mcp/task_actor_test.go` (new)
- `internal/mcp/server.go` (+9 lines in the struct, 1 line changed in `NewServer`, +1 line in `initialize`)

Nothing else was touched: `internal/mcp/tools.go` is untouched, no tool calls `taskActor`, no extra tests, no `-race` run (the brief does not ask for one).

## Self-review

- Completeness: every step of the brief is done, in its order (tests, raw-character check, gofmt, RED, implementation, GREEN, commit). The three review-focus items this task owns (two sessions of one client; a later initialize cannot rename a claimant; a real client name such as "Claude Desktop" makes a claimable actor) each have their test and each passes.
- The length arithmetic holds: "agent:" (6) + at most 53 + "#" (1) + 4 hex = 64 = `tasks.MaxNameLen`, and every character of the label is inside the store's `nameRE` (`[A-Za-z0-9._#@:-]`). The claim test uses the store's real rule, including the reserved labels in any case (`you`, `agent`, `capture`, `chrome`, `os`, `YOU`, `Agent`), and passes for all 14 client names.
- Matches spec D18 (line 38 of the spec): `agent:<client name>#<4 hex>`, the client's name from `initialize`, a random suffix per server process, the model does not choose it, `initialize`'s params are recorded.
- No other code builds an `mcp.Server` literal (only `NewServer` does), so every server has a suffix. No test of the package runs in parallel, so swapping the package-level `newActorSuffix` in `withSuffixes` is safe.
- Observations, not changed (they are the brief's design, and no real client hits them):
  1. The brief's `TestTwoSessionsOfOneClientAreTwoClaimants` names a local variable `real`, which shadows the predeclared `real`. It compiles, `go vet` is clean and the repo runs no other linter; I kept the brief's text.
  2. "The first initialize that names a client wins" is decided on a name that is not blank. A first initialize whose name has no letter or digit (for example `###`) counts as naming a client, so a later initialize with a real name is ignored and the actor reads `agent:mcp#xxxx`. A real client does not do this.
  3. The suffix has 16 bits, so two sessions of one client collide with probability 1 in 65,536 per pair, as D18 accepts.

No concerns.

## Fix round 1

The coordinator's rulings, applied: take the Important finding and Minor 1, 2 and 4 of `task-1-review.md`; Minor 3 is parked (MCP forbids a second initialize); no production code changes; test files only, in a new commit.

Commit: `e3dd5557 test(tasks): the real suffix generator is not constant, a label cut after a gap keeps no hyphen, the client name and the actor are used concurrently under -race` (one file, `internal/mcp/task_actor_test.go`, +56/-3; on top of da632d6f). `task_actor.go` and `server.go` are byte-identical to da632d6f.

Step zero: `git status --short` printed nothing and `git log --oneline -3` had da632d6f on top.

### What changed (all in `internal/mcp/task_actor_test.go`)

1. Important: `TestTwoSessionsOfOneClientAreTwoClaimants` draws 32 suffixes from the real `newActorSuffix`, checks that each is four hex digits, and fails when fewer than 2 are distinct (a false failure has odds of 65536^-31). The single sample `real := newActorSuffix()` is gone. The injected-suffix half of the test (`withSuffixes(t, "0001", "0002")`) is unchanged.
2. Minor 1: `TestClientLabel` gains the case `edge + " bbbb"` with want `edge`, where `edge := strings.Repeat("a", maxClientLabel-1)`: 52 a's, the value of the ruling's `strings.Repeat("a", 52)`, spelled with the constant so the case stays an edge case if the limit changes. The new key is shorter than the longest key of the table, so gofmt kept the alignment and no existing line of the table changed.
3. Minor 2: new `TestConcurrentInitializesAndTaskCallsShareOneActor`: 25 rounds; each round a fresh server and 16 goroutines released together by a closed channel; each goroutine calls `recordClient` and then `taskActor`; all 16 names must equal `agent:claude-code#beef`. (Each goroutine records before it asks, so the expected name is exact whatever the interleaving.) CI runs the suite with `-race` (`.github/workflows/ci.yml:223`), so the test has teeth there.
4. Minor 4: the local `real` no longer exists: the block that held it is now the loop, whose variable is `suffix`. `grep -n '\breal\b'` finds only a comment and an error message.

Two spellings differ from the ruling's words, with the same effect: `edge` is built from the constant (item 2) and `real` is removed instead of renamed (item 4).

### Commands and output (worktree)

```
$ gofmt -l internal/mcp                                         (nothing, exit 0)
$ grep -nP '[^\x00-\x7F]' internal/mcp/task_actor_test.go       (nothing, exit 1; this grep's -P works, see round 0)
$ go vet ./internal/mcp/                                        (nothing, exit 0)
$ go test ./internal/mcp/ -run 'TestClientLabel|TestTheTaskActor|TestTwoSessionsOfOneClient|TestEveryClientName|TestConcurrentInitializes|TestServer' -count=1 2>&1
ok  	github.com/monoes/mono-agent/internal/mcp	4.576s
```

That is the brief's Step 5 command with the new test added to the selection: 29 tests (the 4 of round 0, the new one, 24 `TestServer*`), all passing, one line of output.

The new test under `-race`, once, only that test, on the committed file:

```
$ go test -race ./internal/mcp/ -run 'TestConcurrentInitializesAndTaskCallsShareOneActor' -count=1 -v 2>&1
=== RUN   TestConcurrentInitializesAndTaskCallsShareOneActor
--- PASS: TestConcurrentInitializesAndTaskCallsShareOneActor (0.01s)
PASS
ok  	github.com/monoes/mono-agent/internal/mcp	2.276s
```

### Mutants (scratch export; the worktree was never mutated)

The scratch export is `.../scratchpad/task1mut`: `git archive da632d6f internal data automations go.mod go.sum AGENTS.md SECURITY.md CHANGELOG.md`. I first ran the mutants there against da632d6f's tests (the gap) and against the new test file overlaid before the commit. After the commit I re-exported with `git archive e3dd5557 internal/mcp` over it, checked with `cmp` that `task_actor.go`, `task_actor_test.go` and `server.go` there equal `git show e3dd5557:...`, and ran everything again: the outputs below are from that second pass, on the committed code. Each mutant is applied by a one-line perl edit to the scratch `task_actor.go`; after each run the file was restored from a backup and `cmp` said byte-identical. Baseline of the committed code in the scratch (same selection as above): `ok  github.com/monoes/mono-agent/internal/mcp  3.120s`.

| Mutant (scratch `task_actor.go`) | the earlier tests (da632d6f's, gap) | the new tests (committed) |
|---|---|---|
| A: `newActorSuffix` returns "0000": `return hex.EncodeToString(b[:])` becomes `return hex.EncodeToString(b[:])[:0] + "0000"` (keeps the import used) | PASS: `ok ... 1.556s` | FAIL |
| B: no trim after the cut: `if label = strings.TrimRight(label, "-"); label == "" {` becomes `if label == "" {` | PASS: `ok ... 1.255s` | FAIL |
| C: no locking: the four lines `s.clientMu.Lock()` and `defer s.clientMu.Unlock()` of `recordClient` and `taskActor` deleted | PASS under `-race` (the four earlier tests; `ok ... 6.179s`) | FAIL under `-race`, 5 of 5 runs |

A, new tests (`go -C <scratch> test ./internal/mcp/ -run 'TestClientLabel|TestTheTaskActor|TestTwoSessionsOfOneClient|TestEveryClientName|TestConcurrentInitializes' -count=1`):

```
--- FAIL: TestTwoSessionsOfOneClientAreTwoClaimants (0.00s)
    task_actor_test.go:108: 32 draws of the real suffix generator gave 1 distinct value, want at least 2: two sessions of one client would share a claimant
FAIL	github.com/monoes/mono-agent/internal/mcp	1.032s
```

B, same command:

```
--- FAIL: TestClientLabel (0.00s)
    task_actor_test.go:39: clientLabel("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bbbb") = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-", want "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
FAIL	github.com/monoes/mono-agent/internal/mcp	0.902s
```

C, `go -C <scratch> test -race ./internal/mcp/ -run 'TestConcurrentInitializes' -count=1`, five separate invocations:

```
run 1: 3 race reports; --- FAIL: TestConcurrentInitializesAndTaskCallsShareOneActor (0.00s) FAIL	github.com/monoes/mono-agent/internal/mcp	0.873s
run 2: 4 race reports; --- FAIL: TestConcurrentInitializesAndTaskCallsShareOneActor (0.00s) FAIL	... 0.813s
run 3: 3 race reports; --- FAIL: ... (0.01s) FAIL	... 0.813s
run 4: 2 race reports; --- FAIL: ... (0.01s) FAIL	... 1.186s
run 5: 3 race reports; --- FAIL: ... (0.01s) FAIL	... 0.913s
failed 5 of 5 runs
```

The race reports name the right lines. In the first pass (before a comment-only edit added one line to the test file, so its lines were one smaller then) a report read: `WARNING: DATA RACE`, read at `task_actor.go:68` (`recordClient`, `if s.clientName == ""`) against a previous write at `:69`, called from the test's goroutine at `task_actor_test.go:168`; a second one, read at `:79` (`taskActor`, `if s.actorName == ""`) against a previous write at `:80`, from `task_actor_test.go:169`. The unmutated code passes the same test under `-race` in the scratch export (`ok ... 2.217s`) and in the worktree (above), so the failure is the mutation's.

About the table's middle column: for A and B it is da632d6f's own test file (run in the scratch before the new file was overlaid, in the first pass); for C it is the four earlier tests selected from the committed file (so the new test is excluded), run in the second pass. Their `ok` times are those runs'.

### Self-review (round 1)

- Reread my diff: three hunks in the test file only, plus the import `sync`. Comments say why (the cut at the limit, why the real generator is drawn, why the concurrent test exists and what it relies on). Output of the covering run is one `ok` line. No debug prints, no stray file in the worktree (`git status --short` is empty).
- The three mutants were each shown to pass the earlier tests and to fail the new ones, so the new assertions are what close the gaps, not their neighbours.
- Process note: one of my mutant runs went through `| head -70`, which can cut a script off before its restore step; I checked at once that the scratch `task_actor.go` was byte-identical to the backup (it was), and all outputs above are from later runs that I did not truncate.
- No concerns.
