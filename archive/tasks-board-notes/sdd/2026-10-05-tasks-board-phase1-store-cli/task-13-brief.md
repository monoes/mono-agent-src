### Task 13: Verify the whole phase

**Files:**
- Create (outside the repository, in the session scratchpad): `smoke.sh`
- No source file changes unless a check below fails and the fix belongs to an earlier task.

**Interfaces:**
- Consumes: everything. Produces: evidence that the phase works, which the PR description quotes.

- [ ] **Step 1: Re-check the migration number (spec D32)**

Run (separate calls): `ls data/migrations | tail -3`, `git fetch origin master`, `git ls-tree --name-only origin/master data/migrations/ | tail -3`, and `ls` of `data/migrations` in the four other checkouts listed in Task 1.
Expected: `062_tasks.sql` is the only 062 anywhere. If another 062 exists, stop and tell the lead.

- [ ] **Step 2: Format, vet and build on every platform this repository ships**

CI runs on Linux only, so a macOS or Windows break would surface only after a merge, which releases. Run, one per call:

```
gofmt -l internal/tasks cmd/monoagentcli data
go vet ./internal/tasks/ ./cmd/monoagentcli/
go build ./...
go build -o /dev/null -tags nosocial ./cmd/monoagentcli
GOOS=darwin go vet ./internal/tasks/ ./cmd/monoagentcli/
GOOS=windows go vet ./internal/tasks/ ./cmd/monoagentcli/
```
Expected: `gofmt` prints nothing and every other command ends without output (do not use a bare `go build ./cmd/monoagentcli`: it overwrites the repository's untracked `monoagentcli` binary).

- [ ] **Step 3: Run the tests, the whole suite included**

A new root command, a locale sentence and a migration can break tests that walk the command tree, the locale files or the database, and CI runs `go test ./...` with `-race` on Linux. So run, one per call:

```
go test ./internal/tasks/ -race -count=1 -timeout 15m
go test ./cmd/monoagentcli/ -count=1 -timeout 20m
go test ./... -count=1 -timeout 30m
```

(No `-run` on the second: one doctor test of that package hangs when selected by a narrow pattern and passes in a full package run.) Expected: the first two PASS. The third may fail on tests that already fail on a pristine macOS tree, as of 2026-10-05: in `cmd/monoagentcli` `TestCaptureTaskFilesOnTheBoard`, `TestCoderRootIsOneSharedFolder`, `TestWorkflowCancelSignalsAndMarks` and `TestCoderConversationFolders`; in `internal/capturetask` `TestCreateAttachesEveryArtifact` and `TestCreateRecordsTheRealPathNotASymlink`; in `internal/config` `TestGenerateConfigFailsFastWhenMonomindMissing`; in `internal/monomind` `TestFindAll_ListsShadowedCopies`; plus load flakes that pass when rerun alone (`internal/connections` `TestMigrateConnectionsToVault_SkipsRowLockedByAnotherProcess`, `internal/mcp` `TestGrantWaitTimeoutNote` and `TestManyUpdateCallsAtOnceAllLand`, `internal/dynorg` `TestIsolatedWritersRunInParallelInTheirOwnWorktrees`, `cmd/monoagentcli` `TestAgentTestGoDeadline`, and now and then a `wails-app` test). Any other failure, above all one in `internal/tasks`, `internal/i18n`, `internal/storage`, `data` or a test of this phase, is yours: fix it in the task that owns the code. If in doubt, run the failing test on an export of `origin/master` (`git archive origin/master | tar -x -C <a fresh directory under the scratchpad>`) to see whether it fails there too. If a test of this phase fails because the environment is an agent's (a marker you did not clear), the test is wrong: `newTaskTestDB` must clear it.

- [ ] **Step 4: Mutation checks**

For each row: apply the change with the Edit tool, run the test named, see it FAIL, undo the change with the Edit tool, see it PASS again. A mutation that does not fail its test means a rule has no proof: add or sharpen the test (in the task that owns the code) before going on.

| # | File and change | Test that must fail |
|---|---|---|
| 1 | `store.go`, `Add`: in `if in.Ready && actor.Kind != Human {` (the first check of Add) use `if false {` | `go test ./internal/tasks/ -run TestAddReadyIsForTheOperatorOnly` |
| 2 | `claims.go`, `claimTx`: replace `until = later(cur.Claim.Until, until)` by `until = until` | `-run TestSameNameClaimRenewsButNeverShortens` |
| 3 | `claims.go`, `pickNext`: the Ready query's `ORDER BY position, id` becomes `ORDER BY id DESC` | `-run TestNextPeeksTheTopReadyTask` |
| 4 | `store.go`, `sourceKindFor`, the `Agent` case: the condition becomes `if true {` | `-run TestSourceKindRules` |
| 5 | `claims.go`, `Comment`: `if n >= MaxEventsPerTask {` becomes `if false {` | `-run TestEventCapRefusesCommentsOnly` |
| 6 | `ops.go`, `placeIn`: in the final `switch` the case `pos[idx]-pos[idx-1] >= 2:` becomes `case true:` | `-run 'TestMoveRenumbersAColumnWithNoRoom|TestAnyOrderOfMovesKeepsATotalOrder'` |
| 7 | `clean.go`, `hidden`: the first `case r >= 0xE0000 && r <= 0xE007F:` becomes `case false:` | `-run TestCleanText` |
| 8 | `cmd/monoagentcli/task.go`, `callerFor`: `marker := orgsign.AgentContextMarker()` becomes `marker := ""` | `go test ./cmd/monoagentcli/ -run TestOperatorCommandsRefuseAnAgentContext` |
| 9 | `data/migrations/062_tasks.sql`: remove ` REFERENCES profiles(id) ON DELETE CASCADE` from the `tasks` table | `go test ./internal/tasks/ -run 'TestATaskNeedsAnExistingProfile|TestDeletingAProfileDeletesItsBoard'` |
| 10 | `store.go`, `tx`: `"BEGIN IMMEDIATE"` becomes `"BEGIN"` | `go test ./internal/tasks/ -run 'TestClaimsAcrossProcesses|TestConcurrentClaims' -count=5` (may be flaky: report what you saw) |

- [ ] **Step 5: Smoke-run the built CLI under a throwaway home**

Never run a built `monoagentcli` against the real `HOME` (every run may install skills into `~/.claude/skills`). Write `smoke.sh` into the session scratchpad with the Write tool:

```bash
#!/bin/bash
# Builds the CLI (with the real Go caches) and drives the task board under a throwaway HOME and an empty environment.
set -e
SCRATCH="$(cd "$(dirname "$0")" && pwd)"
TMP="$SCRATCH/smoke-$$"
mkdir -p "$TMP/home"
go build -o "$TMP/monoagentcli" ./cmd/monoagentcli
# A clean environment: no agent-context marker of any kind (the shell that runs this may be an agent's).
E=(env -i "HOME=$TMP/home" "PATH=$PATH")
M=("${E[@]}" "$TMP/monoagentcli" --db-path "$TMP/smoke.db")
echo "--- add, approve, board"
"${M[@]}" task add "smoke test the board" --notes "from the smoke script"
"${M[@]}" task approve 1
"${M[@]}" task board
echo "--- agent: next, claim, comment, finish"
"${M[@]}" task next --as smoke-1
"${M[@]}" task next --claim --as smoke-1
"${M[@]}" task comment 1 "working on it" --as smoke-1
"${M[@]}" task finish 1 --as smoke-1 --result "done in the smoke script"
echo "--- an agent context cannot approve (exit 3 expected)"
"${M[@]}" task add "second" >/dev/null
"${E[@]}" CLAUDECODE=1 "$TMP/monoagentcli" --db-path "$TMP/smoke.db" --json task approve 2 || echo "refused, exit $?"
echo "--- digest is silent with nothing ready"
"${M[@]}" task digest
echo "--- leftovers are in $TMP"
rm -f "$TMP/monoagentcli"
```
Run it with `bash <path to smoke.sh>` from the repository root (a script file passes the worktree guard). Expected: the task is added to the Inbox, approved, listed under READY, claimed by `smoke-1` with the `--profile default` follow-up commands printed, finished into REVIEW; the `CLAUDECODE=1` approval prints a JSON error with `"code":"operator_only"` and `refused, exit 3`; `digest` prints nothing. No file appears under the real `~/.claude`.

- [ ] **Step 6: Hand over**

Do not push, open a PR or merge: the lead does that after the independent reviews. Report: the list of commits (`git log --oneline origin/master..HEAD`), the test results of Step 3, which mutations of Step 4 failed their tests (all ten should), the smoke output of Step 5, and anything that behaved differently from this plan or the spec.

---

## Self-review (done by the plan's author)

- **Spec coverage.** §4 model: Tasks 1 to 4 (schema, statuses, task JSON, events, limits, cleaning, order). §4.5 the profile: Tasks 1 (foreign key), 3 (`Add` refuses an unknown profile), 4 (every read is scoped), 8 and 9 (`--profile` by id and by name, unknown profile exit 3). §5.1 who may do what: Tasks 3, 5, 6 (store) and 8, 10, 11 (CLI). §5.2 claims and leases: Task 6, proven under contention in Task 7. §5.3 idempotent capture: Tasks 3 and 7. §5.4 revision: Tasks 3, 4 and 7 (`Watch`). §6 the package: Tasks 2 to 7. §7 the CLI: Tasks 8 to 11 and 12 (`ref`). §13 security: the gate and the guard (Tasks 5, 8, 10, 11), limits (Task 3), cleaning (Task 2), documented in Task 12 (which also adds the sentence about the board to the root help in both locales, §9.2). The operator guard is tested under every marker of `orgsign.AgentContextMarkers()` (Task 10), as §14 says. Not in this phase, by design: MCP (P2), the app (P3), the extension (P4), the macOS menu (P5), `summary --section tasks` (P2), the skill (P2).
- **Spec deviations recorded here.** `task next` and `claim` use `--lease` as a Go duration (`30m`); the spec says "30 minutes". `Store.Board` takes `doneLimit` and the CLI default is 50, as the spec says. `Unarchive` restores the archived-from column (spec §7 only says `unarchive`). A released task goes to the bottom of Ready (the spec leaves the place open). Amend the spec when the build differs, as the earlier specs were.
- **Placeholders.** None: every code step holds its code. The only value left to the executor is the number of tests in a `PASS` line.
- **Type consistency.** The names used across tasks: `Store`, `Actor{Kind, Name}` with `Human`, `Agent`, `Capture`; `AddInput`, `Filter`, `Edit`, `Placement`, `Outcome`; `Add`, `Get`, `List`, `Board`, `Rev`, `Counts`, `Edit`, `Move`, `Approve`, `Archive`, `ArchiveStatus`, `Unarchive`, `Next`, `Claim`, `Comment`, `Finish`, `Release`, `Watch`; the CLI helpers `callerFor`, `taskErr`, `withTasks`, `flagAs`, `parseTaskID`, `parseTaskIDs`, `columnLabel`, `taskCut`, `heldNote`, `writeOneTask`, `writeTasks`; the test helpers `bg`, `human`, `bot`, `newTestStore`, `addProfile`, `mustAdd`, `countWhere`, `seedTasks`, `seedRow`, `insertRow`, `rowTime`, `column`, `sameIDs`, `newTaskTestDB`, `runTask`, `mustTaskJSON`, `failedTaskJSON`, `id`.
- **Review Focus.** Each of the five has a named test in the task that owns the code (see the list at the top).

