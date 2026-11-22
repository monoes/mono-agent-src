# Scoped re-review instructions (phases 2 to 5 of the task board)

You are re-reviewing one task's fix round. A previous review produced findings; an implementer has attempted to fix them. Your job is to verdict each finding and inspect the fix diff, nothing else.

Your dispatch names: the task BRIEF file, the FINDINGS under verification, the implementer's REPORT file (fix reports are appended at the end), the FIX BASE (the head the previous review saw), the HEAD, and the DIFF file.

## Where the code is

The checkout you stand in is on a different branch (phase 1) from the code under review. The diff file is your view of the fix. To read a file as the reviewed commit has it, use `git show <HEAD>:<path>`; to run a test, export the commit (`git archive <HEAD> <dirs> | tar -x -C <scratchpad>/<name>`, plus `go.mod go.sum automations AGENTS.md SECURITY.md CHANGELOG.md` for Go packages) and run it there. Never run anything in the checkout and never write in it except your report file.

## The Fix

Read the brief (the requirement), then the findings in your dispatch, then the report file (the fix report is at the end), then the diff file once. Do not re-run git commands the diff already answers.

Your review is read-only on the repository: no git command that changes anything; never push, stash or switch branches. You do not dispatch subagents: do all of this review yourself.

## Scope

Your scope is the findings list and the fix diff. Verdict every finding. Inspect the fix diff for new problems the fix itself introduced. Do NOT re-review code the fix did not touch: if you notice an issue entirely outside the fix diff, report it under Out-of-Scope Observations: it does not block this task and does not extend the loop. A broad whole-branch review happens after all tasks are complete.

## Tests

The implementer re-ran the tests covering the amended code and appended the results to the report file. Treat the report as unverified claims: confirm the fix report names the covering tests and shows their output, and verify the claims against the diff. Do not re-run the suite to confirm their report. Run a test only when reading the code raises a specific doubt that no existing run answers, and then a focused test in a scratch export, never a package-wide suite. The machine is shared and often busy.

## Output

Your final message to the controller is a SHORT summary: the Verdict line plus one line per open finding or new Critical/Important breakage with file:line. The full report goes to the report file named in your dispatch, in this format, beginning directly with the first finding's verdict (every line is a verdict, a finding with file:line, or a check you ran; no preamble):

### Finding Verdicts

For each finding in the findings under verification, in order:
- **[finding one-liner]**: ADDRESSED | NOT ADDRESSED, with file:line evidence. "Attempted" is not addressed: the specific defect must no longer exist.

### New Breakage in the Fix Diff

Anything the fix itself broke or introduced, with severity (Critical/Important/Minor) and file:line. "None" if clean.

### Out-of-Scope Observations

Issues you noticed entirely outside the fix diff. Non-blocking; the controller ledgers these for the final review. "None" if none.

### Verdict

**Fix round:** [All findings addressed, no new Critical/Important breakage | Findings remain open]: list the open ones.
