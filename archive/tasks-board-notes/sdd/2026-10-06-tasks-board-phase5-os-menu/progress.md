# SDD ledger — plan: docs/mastermind/plans/2026-10-06-tasks-board-phase5-os-menu.md

Phase 5: the macOS Services menu (`task os install|status|uninstall`). Branch `feat/tasks-board-os`: a LOCAL ref that I advance with `git branch -f` after each accepted task (nothing is pushed; the user pushes). Lane A (after phase 3). Base: `feat/tasks-board` 3c7bc612 (phase 1, PR #359 open). Common rules: `.superpowers/sdd/common/`; this folder holds global-constraints.md, the briefs, reports, review packages. Lane scratch report dir: `/private/tmp/claude-501/-Users-morteza-Desktop-monoes-mono-agent/54d60522-f555-44a3-802d-b3c4ca92ce82/scratchpad/lane-p5/`.
Started: not yet (queued behind phase 3 in lane A).

Process: as in the phase 2 and 3 ledgers. Phase-specific (the plan's environment rules): never write to the real `~/Library/Services`, never run `task os install` or `uninstall` against the real HOME, never run `pbs -update`; `plutil -lint`, `plutil -extract` and `pbs -read_bundle` only read and run only on files under a test's temporary folder or the scratchpad; `/usr/bin/automator` runs only in the opt-in test of Task 5, against stubs. The user verifies the real Services menu themselves.
Pre-flight (the skill's plan scan): planner + reviewer (`.superpowers/planning/2026-10-06-p2-p5/review-p5.md`) + the drift check against phase 1 as built, whose findings were fixed (73481511, c5ecf935); I do not repeat that scan.

TIP (current head of feat/tasks-board-os): 3c7bc612

SCOPE CHANGE 2026-10-07: the user asked to finish the work in flight and file the rest as GitHub issues: this phase is NOT built now; it is tracked as issue #361 (plan and open points are in it). Nothing to resume here unless the user asks.
