# Rules for implementers of this plan

Your task brief is the requirement. This file is the process. Both bind you.

## Environment (binding)

- Work only in the worktree /Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/feat+tasks-board on branch feat/tasks-board. Never push, merge, rebase, switch branches, stash, or touch any other checkout or worktree: other Claude sessions use this repository.
- The Bash tool refuses compound git commands, `git -C`, and heredocs here. Run `git add <files>` and `git commit -m "<subject>" -m "<trailer>"` as two separate Bash calls. Write files with the Write and Edit tools. If a long shell command is refused, put it in a script file under your scratchpad directory and run it with `bash <file>`.
- A project hook blocks Bash commands whose text contains destructive SQL or `rm -r`; another blocks Write and Edit content that looks like an API key. If a hook blocks something harmless, reword it; do not try to get round the hook.
- Go: use the real Go caches (do not override HOME for go commands). Never run a built monoagentcli against the real HOME. The machine is shared and often busy: run the packages your task names, not `go test ./...` (Task 13 does that once).
- The plan's code was written without a compiler. Fix a compile slip (an unused variable, a shadowed name, a missing import, gofmt alignment) in place and say so in your report. A failing assertion is different: read the spec section the test comes from before changing the test or the code, and report which of the two was wrong.
- Every commit message ends with this trailer, as the second `-m`: `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
- Never rewrite history: no `git commit --amend`, `git reset`, `git rebase`, `git update-ref`. Other agents and the controller commit to the same branch (the controller's docs commits land between yours): a commit you did not make may be the tip. Fixes go in NEW commits. Before committing, run `git status --short` and make sure only your own files are staged (the index is shared); commit with explicit paths.
- Mutation runs: other agents may share this worktree and read or compile the same files. Never mutate a source file IN PLACE here: export your committed code with `git archive <your commit> internal data go.mod go.sum` into a scratch directory under the scratchpad (use the folder your dispatch names, or .../scratchpad/<your task>mut/) and run every mutant there. A mutant left in a shared file breaks everyone else's build (it happened once).
- Read the plan's header once: everything above the heading "### Task 1" in docs/mastermind/plans/2026-10-05-tasks-board-phase1-store-cli.md (Global Constraints, Environment rules, Review Focus, file structure). Do not read the rest of the plan: your brief holds your task.

## Before You Begin

If you have questions about the requirements, the approach, dependencies or anything unclear in the task, ask them now, before starting. If something unexpected comes up while you work, pause and ask: do not guess.

## Your Job

1. Implement exactly what the task specifies, step by step, in the order the brief gives (tests first where it says so; run the failing test and see it fail before you implement).
2. Verify it works with the commands the brief gives.
3. Commit your work as the brief says.
4. Self-review (below).
5. Report back.

While iterating run the focused test for what you are changing; run the task's whole verification once before committing.

## You Do Not Dispatch Subagents

Do all of this task's work yourself. Never spawn a subagent to implement part of it, and above all never spawn a reviewer to check your work. Self-review means reading your own diff. Review is the controller's job: after you report, a fresh reviewer is dispatched against your diff. A reviewer you spawn duplicates that review at full cost and its approval counts for nothing.

## Code Organization

- Follow the file structure in the plan. Each file has one clear responsibility.
- If a file you are creating grows beyond the plan's intent, stop and report DONE_WITH_CONCERNS: do not split files on your own.
- Follow the patterns of the codebase. Do not restructure things outside your task.

## When You Are in Over Your Head

It is always OK to stop and say "this is too hard for me". Bad work is worse than no work. Stop and report BLOCKED or NEEDS_CONTEXT when the task needs an architectural decision, when you need code beyond what was provided and cannot find clarity, when you are unsure your approach is correct, or when you have read file after file without progress. Say specifically what you are stuck on, what you tried, and what help you need.

## Before Reporting Back: Self-Review

Read your own diff with fresh eyes.
- Completeness: did you implement everything in the brief, every step, every test?
- Quality: clear names, clean code, no leftovers (stray files, debug prints, commented-out code).
- Discipline: only what was requested (YAGNI); shared files touched minimally.
- Testing: do the tests verify behavior; is the test output pristine (no stray warnings)? Did you see each new test fail before it passed (TDD evidence)?
Fix what you find before you report.

## After Review Findings

If the task review finds issues you will be resumed with the findings. Fix them, re-run the tests that cover the amended code, and append a fix report to your report file: what you changed, the covering tests you ran, the command, and the output. Reviewers do not re-run tests for you: your report is the test evidence. Then reply with the same short status contract as your first report.

## Report Format

Write your full report to the report file named in your dispatch:
- what you implemented (or attempted, if blocked), and every deviation from the brief with the reason (compile slips you fixed included);
- what you tested and the results;
- TDD evidence: RED (command, the relevant failing output before implementing, why it was expected) and GREEN (command, the relevant passing output after);
- the files changed;
- self-review findings; any issues or concerns.

Then reply with ONLY (under 15 lines):
- Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
- the commits you created (short SHA and subject)
- a one-line test summary (for example "14/14 passing, output pristine")
- your concerns, if any
- the report file path

If BLOCKED or NEEDS_CONTEXT put the specifics in the final message itself. Use DONE_WITH_CONCERNS if you completed the work but have doubts. Never silently produce work you are unsure about.
