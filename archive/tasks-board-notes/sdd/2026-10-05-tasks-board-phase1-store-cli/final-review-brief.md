# Final whole-branch review: task board, phase 1 (the store and the CLI)

You are the last reviewer of this branch before a human merges it (a merge to master releases the program to users). Per-task reviewers already judged each task's diff. You judge what only the whole branch shows: that the parts fit, that the rules hold across files, that nothing a person using this software would hit was missed. Be thorough; this is the review that matters most.

## What was built

A personal kanban board per profile in the monoagent app, phase 1 of 5: the storage and rules package `internal/tasks` and the CLI group `monoagentcli task` (alias `tasks`). Later phases (MCP tools, the desktop app, a Chrome extension, a macOS menu) will call the same package; they are NOT in this branch and are out of your scope (their plans live in docs/mastermind/plans/ and another helper checks them).

Checkout (read-only for you): /Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/feat+tasks-board , branch feat/tasks-board. Range: origin/master..HEAD (78+ commits; the diff against origin/master is only this work: origin/master is already merged into the branch).

Production files to read in full (about 4,500 lines):
- data/migrations/062_tasks.sql
- internal/tasks/doc.go, model.go, clean.go, store.go, read.go, ops.go, claims.go, watch.go
- cmd/monoagentcli/task.go, task_read.go, task_ops.go, task_agent.go, ref_tasks.go
- small edits in existing files: cmd/monoagentcli/root.go (one line), cmd/monoagentcli/ref.go (three lines), internal/i18n/locales/en.json and es.json (one sentence), AGENTS.md, SECURITY.md, CHANGELOG.md
- the spec (docs/mastermind/specs/2026-10-05-task-board-design.md) and the phase 1 plan (docs/mastermind/plans/2026-10-05-tasks-board-phase1-store-cli.md: read its header, Global Constraints and Review Focus; the rest is task text).
Tests (about 12,000 lines, `*_test.go` in internal/tasks and cmd/monoagentcli): read selectively, as evidence for the points below.

A review package with the production diff is at the path given in your dispatch. The ledger of the build is progress.md in this directory (decisions, deferred and parked findings, resume notes): skim it for lines starting `Ruling:` and for anything marked "Open for the final review".

## What to judge (the checklist; report each item, even if clean, in one line)

1. Spec conformance. Walk the spec sections 4 to 7 and 13 and the decisions D5 to D15, D29, D31, D32 sentence by sentence against the code. Report each sentence the code contradicts, and each behaviour the code has that the spec contradicts or does not cover (the spec was amended during the build; say which side should change). The spec is a vision document: for an input or condition it does not mention, judge by what a reasonable person using the software would expect.
2. Transactions and concurrency. Is every write inside one `BEGIN IMMEDIATE` transaction through one helper? Any read-then-write race between two processes (the CLI, the app and agents all share one SQLite file)? Busy and locked handling; rollback on every error path; the board revision bumped by every real write and not by a no-op; `Watch` (goroutine and timer leaks, a context cancelled mid-poll, the snapshot); the claim UPDATE and the lease arithmetic (rounding, the 24-hour cap, the later-of rule).
3. Authorization. Every way an agent could do an operator action: the CLI guard (markers, `--as`, a blank `--as`, `MONOAGENT_ACTOR`, the alias `tasks`, argument order, `add --ready`, `add --source os`, `comment` without `--as`, `board`), and above all that the STORE enforces the gate independently of the CLI (the store is the authority; the CLI is a thin caller). The capture actor rules (a capture's name and source agree).
4. Untrusted text. Every place task text (title, notes, comments, source fields, event notes, an agent name) reaches a terminal, a suggested command line, JSON or SQL: quoting and escaping in the commands the printers suggest, echo cuts, control and hidden characters, ANSI escapes, line separators, very long input, right-to-left marks (the owner writes Persian: those marks are kept on purpose), dynamic SQL built from strings.
5. Data integrity and migration. Schema constraints (CHECK lists, NOT NULL, the foreign key with ON DELETE CASCADE, the partial unique index) against what the code assumes; position arithmetic and renumbering; archive and unarchive; `client_id` idempotency per profile; whether migration 062 follows this repository's conventions and is safe on an existing database (look at two or three earlier migrations); the risk that any code path replacing a profile row with INSERT OR REPLACE would cascade-delete a board (the ledger lists it as open: say whether any such path exists today).
6. CLI contract. Exit codes (2 not found, 3 invalid or refused; 1 for a flag parse error) and the `--json` error document for every error class; flag edge cases (`--status ''`, ids like `#42`, `--top` together with `--before`, huge, zero and negative numbers); standard input handling (the 1 MiB cap, refusals before reading); the silence of `digest`; the accuracy of `ref tasks`, `ref commands` entries and the help texts against what the commands do; English and Spanish locale parity.
7. The tests as evidence. Any test that asserts nothing, sleeps on the wall clock, depends on the real HOME, the network, map order or a leftover environment marker; the multi-process tests (do they leave processes or files behind, can they hang?); how long the two packages take (CI runs `go test ./...` with `-race` on Linux with a time limit: say whether the tests of these packages are a risk for it).
8. Repository hygiene. Files under 500 lines (production and test), gofmt, no stray debug output, no secrets, conventional commit messages, no unrelated changes to existing files beyond the list above.

## Rules

- Read-only on the checkout: no edits, no git command that changes anything (no add, commit, stash, switch, checkout, reset, clean; never push), never run a built monoagentcli against the real HOME, never `go build ./cmd/monoagentcli` without `-o` there (it overwrites an untracked binary). If you need to run something, export the tree: `git archive HEAD internal data cmd automations AGENTS.md SECURITY.md CHANGELOG.md go.mod go.sum | tar -x -C <a new folder under /private/tmp/claude-501/-Users-morteza-Desktop-monoes-mono-agent/54d60522-f555-44a3-802d-b3c4ca92ce82/scratchpad/>` (one command per call; the shell refuses compound git commands) and run `go test ./internal/tasks/ -run '<name>' -count=1` or a throwaway program there; a package-wide suite only if a specific doubt needs it, one at a time (the machine is shared). The controller runs the full suite, the mutation checks and a smoke run separately: their results are in task-13-mutation-report.md (when it exists) and will be reported to you if you ask.
- You do not dispatch subagents.
- Every finding: file:line, what is wrong, why it matters to a person using the software, how to fix. Severity: Critical (data loss, security hole, broken core behaviour), Important (the branch should not merge as is), Minor.
- Before the verdict, a "Declined to judge" list: every behaviour you considered and set aside as outside the plan or spec, one line each with the reason.
- Format: the code-reviewer template (Strengths, Issues by severity, Recommendations, Assessment with "Ready to merge? Yes | No | With fixes").
- Write the full report to final-review.md in this directory and return only: the verdict, the Critical / Important / Minor counts and one line per Critical or Important finding with file:line.
