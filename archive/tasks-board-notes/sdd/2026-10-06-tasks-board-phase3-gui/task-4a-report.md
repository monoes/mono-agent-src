# Phase 3, Task 4a report: the board model (moves, places and keys, pure)

Status: DONE

Worktree: `/Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/agent-a48c0b14f29f82b26`, branch `worktree-agent-a48c0b14f29f82b26`, started from 847971bb (fast-forwarded from 5a009e8e with `git merge --ff-only 847971bb`; the merge went through).

## Commit

- `3173256f feat(tasks): the board model: moves, placement and keys` (trailer `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`)
  - `wails-app/frontend/src/lib/taskModel.js` (160 lines, new)
  - `wails-app/frontend/src/lib/taskModel.test.js` (135 lines, new)
- No other file touched. `git status --short` is empty after the commit.

## What I implemented

Exactly the brief's two code blocks. A script (`task4a-verbatim-check.mjs` in the scratchpad) extracts the two `js` blocks from the brief and compares them with `git show HEAD:<path>`: both files are IDENTICAL to the brief (tests 6335 chars, implementation 6730 chars). Exports: `COLUMNS`, `defaultIndex`, `normalizeBoard`, `findTask`, `applyMove`, `applyRemove`, `applyOps`, `placeFor`, `isNoopDrop`, `dropIndex`, `keyMove`, `focusTarget` (the Interfaces list of the brief; `defaultIndex` is exported too, as the brief's code has it).

The module header comment names search, changes between reads and card labels, which are Task 4b's: kept verbatim, because 4b appends to this file.

## Deviations from the brief

1. Step order: I committed (Step 6) BEFORE the mutation checks (Step 5). The rules file says mutants run only on a scratch export of the committed code, never in the worktree, so there had to be a commit to export. Nothing else changed order: tests first, RED, implementation, GREEN, then commit, then mutants.
2. No compile slips and no failing assertion: nothing in the brief's code or tests was changed.
3. Mutant runs use the vitest binary with the JSON reporter inside the scratch folder (vitest 5 writes the JSON report to a file, so the runner passes `--outputFile`). The RED and GREEN runs use the brief's own command.

Left as the brief has them (not deviations, listed so a reviewer does not flag them): `T0` and `by` in the test file are unused in this task (Task 4b's appended tests use them; there is no frontend linter, `package.json` has no lint script). The only non-ASCII character in either file is the visible `§` in line 1 of the module header comment (checked with `LC_ALL=C grep -n '[^ -~<tab>]'`; macOS grep has no `-P`); no invisible character.

## Environment (the dispatch's facts)

- `df -h /Users/morteza/Desktop/monoes`: 11 GiB free (more than 3 GB).
- `npm --prefix wails-app/frontend ci`: added 205 packages (about 2 min on the busy machine). The `allow-remote` warnings are the expected noise; `npm ci` also prints "1 high severity vulnerability": ignored, since `npm audit fix` would rewrite the lock file.
- `npm --prefix wails-app/frontend run build`: built (the chunk-size warning is the app's usual one).
- `git status --short`: empty after both (node_modules and dist are ignored).
- I did not touch `src/services/tasks.behavior.test.js` or `tasks.test.js`.
- Only `src/lib/taskModel.test.js` was run (never the whole suite).

## TDD evidence

RED (before `taskModel.js` existed):

```
npm --prefix wails-app/frontend test -- src/lib/taskModel.test.js
 FAIL  src/lib/taskModel.test.js [ src/lib/taskModel.test.js ]
Error: Cannot find module './taskModel.js' imported from .../wails-app/frontend/src/lib/taskModel.test.js
 Test Files  1 failed (1)
      Tests  no tests
```

Expected: the module is what the test imports and it did not exist yet.

GREEN (after writing `taskModel.js`):

```
npm --prefix wails-app/frontend test -- src/lib/taskModel.test.js
 Test Files  1 passed (1)
      Tests  14 passed (14)
```

Output pristine: apart from the two `npm warn invalid config allow-remote` lines (the user's `~/.npmrc`), no warning, no console output. The 14 tests: normalizeBoard 1, applyMove 6, applyOps and applyRemove 1, placeFor 1, isNoopDrop 1, dropIndex 1, keyMove 2, focusTarget 1.

I also traced every assertion by hand through the code before running (the Done count arithmetic 120 to 121 and 119, the ref fallback, same-column reorder, `dropIndex` exactly at a middle, `focusTarget` skipping the empty Review column); all agreed with the run.

## Step 5: the three mutants of the brief (run on a scratch export of commit 3173256f)

Scratch folder: `/private/tmp/claude-501/-Users-morteza-Desktop-monoes-mono-agent/54d60522-f555-44a3-802d-b3c4ca92ce82/scratchpad/task4amut` (left in place; `git archive 3173256f` of the two files, `vitest.config.js` and `package.json`, with the worktree's `node_modules` linked in; the export is byte-identical to the committed blob). Scripts: `task4a-mut-setup.sh`, `task4a-mutants.mjs` (same folder as this report). One mutant at a time, the pristine file restored and compared after every run. Baseline in the scratch folder: 14 passed, 0 failed.

| Mutant | Failing tests | Assertion message |
|---|---|---|
| `placeFor`: `{ where: 'after', ref: ids[ids.length - 1] }` becomes `{ where: 'bottom' }` | exactly "placeFor > names a shown neighbour, never the bottom" (13 others pass) | expected { where: 'bottom' } to deeply equal { where: 'after', ref: 4 } |
| `dropIndex`: `y < r.top + r.height / 2` becomes `y <= r.top + r.height / 2` | exactly "dropIndex > lands before the first card whose middle is below the pointer" (13 pass) | expected +0 to be 1 |
| `applyMove`: `claim: to === 'in_progress' ? found.task.claim : null` becomes `claim: found.task.claim` | exactly "applyMove > ends the claim of a card that leaves In progress and keeps it inside" (13 pass) | expected { by: 'bot', ... } to be null |

## Extra mutants (my own, not in the brief; for test-strength information only)

Killed (each by the test that names the rule): X1 Review no longer newest first (applyMove default place); X2 Done count follows the cut column (the Done counts test and the applyOps test); X3 `before` with a missing ref goes to the top; X4 `after` lands before the ref (both: "puts it before or after a card, and falls back..."); X5 no sort by position (normalizeBoard and focusTarget); X9 `first`/`last` swapped (keyMove); X10 unknown-column guard removed (applyMove, TypeError).

SURVIVED (the brief's tests do not pin these; the code is right, a later edit could break them unnoticed):
- X7 `isNoopDrop`: the `if (from !== to) return false` guard removed. This one matters most: without the guard, dropping the only card of one column into an EMPTY other column would read as "no change" (`isNoopDrop([3], 'ready', 'review', [], 0, 3)` must be false and no brief test says so). Task 8's drag tests are the natural place to pin it.
- X8 `focusTarget`: the clamp `Math.min(found.index, other.length - 1)` removed (a card at index 3 moving to a column of two cards would throw); no test uses a shorter neighbour column.
- X11 `applyOps`: laying the operations newest first (the brief's example pair commutes, so "in order" is not pinned; Task 5's hook tests could pin it).
- X6 `byPosition`: the tie-break on the id removed (no test has equal positions; the store orders by `position, id`, so the tie-break is the same rule).

I did not add tests for these: the brief fixes the test file's content and Task 4b appends to it.

## Notes for the controller (no action in this task)

- `placeFor([7], 1)` returns `after 7`, as the brief's test says. The plan's Ruling about a drop below the last card of a CUT Done column (it takes Done's default) is Task 8's code, which knows the count against the shown cards; `placeFor` stays as given.
- Card ids are compared with `===`: every caller must pass numbers (a `data-id` string from the DOM would make `findTask` answer null silently).
- `applyMove` and `applyRemove` recompute the five column counts only; `counts.stale` stays as last read until the confirming read.

## Self-review

- Completeness: every step and test of the brief is in; the 12 exports match the Interfaces.
- Quality: files are the brief's own text; 160 and 135 lines (limit 500); no debug output, no stray files in the worktree (scratch files live under the session scratchpad).
- Discipline: two paths staged explicitly (`git add <two paths>`, then `git commit` as a separate call); nothing else touched; no history rewrite; no push.
- Tests verify behaviour: they drive the pure functions with a board fixture; each of the brief's three pinned rules was shown to fail its named test when broken.

Files: `wails-app/frontend/src/lib/taskModel.js`, `wails-app/frontend/src/lib/taskModel.test.js`.
