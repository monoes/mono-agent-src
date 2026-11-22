# SDD ledger — plan: docs/mastermind/plans/2026-10-06-tasks-board-phase4-extension.md

Phase 4: the Chrome extension and the daemon's `task.add`. Branch `feat/tasks-board-extension`: a LOCAL ref that I advance with `git branch -f` after each accepted task (nothing is pushed; the user pushes). Lane B (after phase 2). Base: `feat/tasks-board` 3c7bc612 (phase 1, PR #359 open). Common rules: `.superpowers/sdd/common/`; this folder holds global-constraints.md, the briefs, reports, review packages. Lane scratch report dir: `/private/tmp/claude-501/-Users-morteza-Desktop-monoes-mono-agent/54d60522-f555-44a3-802d-b3c4ca92ce82/scratchpad/lane-p4/`.
Started: not yet (queued behind phase 2 in lane B).

Process: as in the phase 2 and 3 ledgers. Phase-specific: the node tests use a fake `chrome`; every `node --test` command sets `CHROME_PATH=/nonexistent` so the browser suites skip; the ONE exception is Task 6 Step 7, on the conditions written there (headless, throwaway profile, explicit opt-in, read the harness first, abort if it launches anything else): the user's own Chrome is never touched. The Go side runs under throwaway HOMEs and starts no daemon.
Pre-flight (the skill's plan scan): planner + reviewer (`.superpowers/planning/2026-10-06-p2-p5/review-p4.md`) + the drift check against phase 1 as built, whose findings were fixed (eadaf443, ba503047, 67402a57); I do not repeat that scan.

TIP (current head of feat/tasks-board-extension): 3c7bc612

SCOPE CHANGE 2026-10-07: the user asked to finish the work in flight and file the rest as GitHub issues: this phase is NOT built now; it is tracked as issue #360 (plan and open points are in it). Nothing to resume here unless the user asks.
