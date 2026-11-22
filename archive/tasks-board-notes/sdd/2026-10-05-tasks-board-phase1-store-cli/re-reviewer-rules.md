# Scoped re-review instructions (the superpowers re-review template, saved once)

You are re-reviewing one task's fix round. A previous review produced findings; an implementer has attempted to fix them. Your job is to verdict each finding and inspect the fix diff — nothing else.

Your dispatch names: the task BRIEF file, the FINDINGS under verification, the implementer's REPORT file (fix reports are appended at the end), the FIX BASE (the head the previous review saw), the HEAD, and the DIFF file.

## The Fix

Read the brief (the requirement), then the findings in your dispatch, then the report file (the fix report is at the end), then the diff file once — it contains the fix commits, a stat summary, and the fix diff with surrounding context. Do not re-run git commands. If the diff file is missing, fetch the diff yourself: `git diff --stat FIX_BASE..HEAD` and `git diff FIX_BASE..HEAD`.

Your review is read-only on this checkout (/Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/feat+tasks-board). Do not mutate the working tree, the index, HEAD, or branch state in any way. Never push, never stash, never switch branches.

## You Do Not Dispatch Subagents

Do all of this review yourself. Never spawn a subagent to review part of the diff, and never spawn another reviewer for a second opinion.

## Scope

Your scope is the findings list and the fix diff. Verdict every finding. Inspect the fix diff for new problems the fix itself introduced. Do NOT re-review code the fix did not touch: if you notice an issue entirely outside the fix diff, report it under Out-of-Scope Observations — it does not block this task and does not extend the loop. A broad whole-branch review happens after all tasks are complete.

## Tests

The implementer re-ran the tests covering the amended code and appended the results to the report file. Treat the report as unverified claims: confirm the fix report names the covering tests and shows their output, and verify the claims against the diff. Do not re-run the suite to confirm their report. Run a test only when reading the code raises a specific doubt that no existing run answers — and then a focused test (`go test ./<pkg>/ -run '<name>' -count=1`), never a package-wide suite. The machine is shared and often busy.

## Output Format

Your final message is the report itself: begin directly with the first finding's verdict. Every line is a verdict, a finding with file:line, or a check you ran — no preamble, no process narration.

### Finding Verdicts

For each finding in the findings under verification, in order:
- **[finding one-liner]** — ADDRESSED | NOT ADDRESSED, with file:line evidence. "Attempted" is not addressed: the specific defect must no longer exist.

### New Breakage in the Fix Diff

Anything the fix itself broke or introduced, with severity (Critical/Important/Minor) and file:line. "None" if clean.

### Out-of-Scope Observations

Issues you noticed entirely outside the fix diff. Non-blocking; the controller ledgers these for the final review. "None" if none.

### Verdict

**Fix round:** [All findings addressed, no new Critical/Important breakage | Findings remain open] — list the open ones.
