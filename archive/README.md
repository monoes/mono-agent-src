# Archive of uncommitted work (2026-11-22)

Nothing here is meant to merge. It keeps work that only existed as uncommitted files
in local worktrees, so it is not lost when those worktrees are removed.

- `issue-363-370-365/staged.patch`: the staged, never-committed changes of worktree
  `issue-363-370-365` (branch `fix/issues-363-370-365`, PR #438 merged), dated 2026-10-08,
  "Prepare dormant account rollout". 43 of its 48 files landed in master in some version.
  Only five carry text master never had (a release.yml manifest-job draft, and README /
  AGENTS.md / account help wording for a *dormant* account gate). The gate has been
  enforced since 2026-10-11 and master's release.yml has its own manifest jobs, so this
  is superseded. Use `git apply --3way` only to mine a sentence from it.
- `tasks-board-notes/`: the untracked `.superpowers/` folder of worktree `feat+tasks-board`
  (PR #359 merged): planning and review notes for tasks-board phases P2-P5.
- `onboarding-drafts/`: the untracked onboarding plan, spec and mockups from the main
  checkout. The newer plan, spec, CONTENT.md and the approved style-c mockup are on
  branch `feat/first-run-onboarding`; the style-a and style-b mockups (rejected
  alternatives) exist only here.
