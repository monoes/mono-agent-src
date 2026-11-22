# Rules for implementers of the task board phases 2 to 5 (common to all four)

Your task brief is the requirement. This file is the process. Both bind you. Your dispatch names the phase, the commit you start from (TIP), the report file, and anything specific to your task.

## Environment (binding)

- You work in an ISOLATED git worktree that the harness created for you: it is your current directory, on a branch `worktree-agent-<id>`. The harness bases it on origin/master, so your FIRST step is: `pwd`, `git branch --show-current`, `git log --oneline -1`, then `git merge --ff-only <TIP>` (TIP is the commit named in your dispatch: it descends from origin/master, so the fast-forward works), then `git log --oneline -3`. If the merge is refused, stop and report NEEDS_CONTEXT with the output: do not force anything and write nothing.
- Work only in this worktree. Never push, never merge into another branch, never rebase, stash, or switch to another branch, never touch another checkout or worktree: other Claude sessions use this repository.
- Never rewrite history: no `git commit --amend`, `git reset`, `git rebase`, `git update-ref`. Fixes go in NEW commits. Before committing run `git status --short` and stage explicit paths only (never `git add -A`, never `git commit -a`).
- The Bash tool here refuses compound git commands, `git -C`, and heredocs. Run `git add <files>` and `git commit -m "<subject>" -m "<trailer>"` as two separate Bash calls. Write files with the Write and Edit tools. If a long shell command is refused, put it in a script file under your scratchpad directory and run it with `bash <file>`.
- A project hook blocks Bash commands whose text contains destructive SQL or `rm -r`; another blocks Write and Edit content that looks like an API key (`sk-` followed by 20 or more letters, digits, `_` or `-`). If a hook blocks something harmless, reword it; never try to get round a hook. Do not delete folders with `rm -r`; leave scratch folders where they are.
- Every commit message ends with the trailer, as the second `-m`: `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`. Commit subjects are conventional (`feat(tasks): ...`, `fix(tasks): ...`, `docs(tasks): ...`, `test(tasks): ...`).
- Your report goes to the report file named in your dispatch (a path under the session scratchpad: your worktree is not the controller's); the controller copies it.
- Tests: run the tests your brief names (targeted runs), not the repository's whole suite (the machine is shared and busy; the controller runs the suites once per phase). Never select the `cmd/monoagentcli` doctor tests with a loose `-run` such as `Doc` (one of them hangs when selected that way).
- Go: use the real Go caches (do not override HOME for go commands). Never run a built `monoagentcli` against the real HOME (every CLI run may install skills into `~/.claude/skills`): build it with `-o` into a fresh `mktemp -d` folder and run it as `env -i HOME=<folder>/home PATH=/usr/bin:/bin <folder>/monoagentcli ...`. Never a bare `go build ./cmd/monoagentcli` (it overwrites an untracked binary): use `-o`.
- Node and the browser: use `npm ci` (never `npm install`, which rewrites the lock file); frontend commands run as `npm --prefix wails-app/frontend <command>`. Never launch the desktop app, a daemon, `wails dev` or `wails build`, and never the user's Chrome; a browser test runs only when your brief says so, with the opt-in the plan names.
- Never write to the real `~/.claude`, `~/.monoagent`, `~/Library/Services` or any other folder of the user's real home: tests and smoke runs use temporary folders and a throwaway HOME.
- Mutation runs: never mutate a source file in your worktree. Export your committed code into a scratch folder (`git archive <your commit> <the directories your brief names> | tar -x -C <scratchpad>/<your task>mut`, plus `go.mod go.sum`, `automations`, `AGENTS.md SECURITY.md CHANGELOG.md` for Go packages) and run every mutant there; restore each file after its run.
- Invisible characters in code are written as eight-digit Go escapes (`\U0000202e`) or, in JS, `String.fromCodePoint(0x202e)`; the Write tool turns four-digit `\u` escapes into the raw character. After writing a file that holds such text, run `grep -nP '[^\x00-\x7F]' <file>`: only characters the file already held may show.
- The plan's code was written without a compiler or a browser. Fix a compile slip (an unused variable, a shadowed name, a missing import, gofmt alignment) in place and say so in your report. A failing assertion is different: read the spec section the test comes from before changing the test or the code, and report which of the two was wrong.
- Read the plan's header once: everything above the first "### Task" heading of your plan (Global Constraints, Environment rules, Rulings, Review Focus, file structure). Do not read the rest of the plan: your brief holds your task. Where the plan's environment rules name a worktree or a branch of its own, your dispatch's instructions win (the controller manages branches).

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

- Follow the file structure in the plan. Each file has one clear responsibility. Files stay under 500 lines.
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

If the task review finds issues you will be resumed with the findings. Fix them, re-run the tests that cover the amended code, and append a fix report to your report file: what you changed, the covering tests you ran, the command, and the output. Reviewers do not re-run tests for you: your report is the test evidence. Then reply with the same short status contract as your first report. Never resume work after a long pause without first running `git status --short` and `git log --oneline -3`.

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
