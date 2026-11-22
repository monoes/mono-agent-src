# Review brief for one phase plan (P2, P3, P4 or P5) of the task board

You review ONE implementation plan. You are read-only: you edit no file except your own review file, run no build and no test, make no commit. You may read the whole repository and run read-only commands (`grep`, `ls`, `git log`, `git show`, `go doc`). The plans were written without a compiler by a planner agent; a fresh sonnet-class implementer will execute each task from its text alone, and a reviewer will check each task's diff. Your job is to find what would make that fail or ship a defect, before any code is written.

## Read first

- The common planning brief the plan was written under: /Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/feat+tasks-board/.superpowers/planning/2026-10-06-p2-p5/common.md (format, size, lessons, hook traps).
- The spec (binding): /Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/feat+tasks-board/docs/mastermind/specs/2026-10-05-task-board-design.md. Read all of it. Sections: 2 decisions, 3 verified facts, 4 model, 5 rules, 6 package contract, 7 CLI, 8 MCP, 9 discovery, 10 desktop, 11 Chrome, 12 macOS, 13 security, 14 testing, 15 delivery and working rules, 16 not in v1, 17 open points.
- Your plan (named in your dispatch). It is long; read it all, section by section.
- The P1 plan header (lines 1-56 of docs/mastermind/plans/2026-10-05-tasks-board-phase1-store-cli.md) and, to see what P1's code really looks like, the committed package: /Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/feat+tasks-board/internal/tasks (model.go, clean.go, store.go; reads, moves and claims are still being built, so for those argue from spec section 6 and the P1 plan's Interfaces blocks).

## P1 changed after the planners started: check the plan against these (the spec already says them; a plan written earlier may not)

1. A stale claim is `claim_until <= now` (a lease that ends exactly now has ended).
2. Cleaning (spec 4.6): hidden characters (tag characters, bidi controls, BOM) are removed; a source URL holding a control or hidden character or invalid UTF-8 anywhere is stored empty; a cut keeps text plus its `[truncated: N characters in the original]` notice within the limit and cutting a cut text changes nothing; notes and text given together are refused (`invalid_input`, "give notes or text, not both"); a title with text and no notes makes the text the notes.
3. The hourly agent limit (20 per profile) counts tasks created strictly within the last hour: a task created exactly an hour ago no longer counts.
4. Names: the labels `you`, `agent`, `capture`, `chrome`, `os` are refused, ignoring case, as AGENT names; a CAPTURE actor's name must be empty, `chrome` or `os`, and agree with its source (a name set and no source requested: the source is the name). A replay (same client_id in the same profile) is looked up before any limit and writes nothing. Add takes no title from SourceTitle or SourceURL: a page capture's caller must pass the title itself (page title, else the URL).
5. `Store` methods run in `BEGIN IMMEDIATE` transactions; callers use only the transaction handle inside a callback (not applicable to code outside internal/tasks, but a plan must not add store code that nests or bypasses it).

## What to check (each is a finding class)

1. Spec coverage: build a table of the spec requirements that belong to this phase (sections named in the plan's header, plus decisions D1-D32, 13, 14, 15, 17) against the task that implements each. Missing requirements are findings. Requirements the plan adds beyond the spec are findings unless it lists them as a Ruling with reason and cost.
2. Rulings that depart from the spec: list each, say whether it is sound, and whether it needs the spec amended or the user's decision.
3. Placeholders and gaps: "TBD", "similar to Task N", steps that describe instead of show, code that refers to names no task defines, expected outputs missing.
4. Type and name consistency across tasks, and against P1: every identifier, signature, JSON field, flag, error code and exit code the plan uses from P1 or from an earlier task of its own must exist with that exact spelling (check the committed internal/tasks code and the P1 plan's Interfaces blocks; for things not built yet, flag any that spec section 6/7 does not define).
5. Compile and run feasibility: code that will not compile as written (unused imports and variables, shadowed names, wrong types), tests that cannot pass as written, commands that will hang or need a tool or network the executor lacks, steps that would touch the real HOME, `~/.monoagent`, `~/Library/Services`, `~/.claude`, the user's Chrome, a running daemon, or the main checkout.
6. Hook and tooling traps (see common.md): the letters `sk-` followed by 20 or more key characters anywhere in plan text; a file name containing `task-`; raw invisible or look-alike characters anywhere in the plan (run `grep -nP '[^\x00-\x7F]' <plan>` and judge each hit: visible characters are fine, invisible ones are findings); four-digit escapes in code meant to carry invisible characters; Bash steps with compound git commands, `git -C`, heredocs, `rm -r`, or destructive SQL; commits not split into `git add` and `git commit`.
7. Tests that prove little: a rule with no test that fails when the rule is removed; tests passing on any error; boundaries tested on one side; Review Focus items with no test in the owning task. Name the specific rule and the test that is missing.
8. Size and structure: any file the plan creates or grows past 500 lines; a task too big for one implementer in 30-60 minutes (say where to split); a task whose Interfaces block is wrong or missing names its neighbours need.
9. Security: untrusted text handling, the operator gate, the agent limit, anything that lets an agent skip the human gate; anything that leaks paths or secrets in messages; shell quoting; XSS in UI code; trust of page-supplied events in the extension.
10. Shared files: the plan's edits to AGENTS.md, SECURITY.md, CHANGELOG.md, root.go, internal/mcp/tools.go, Sidebar.jsx, App.jsx, locale files: are they small, anchored by text that exists, and unlikely to collide with the other phases' edits (the other plans are in docs/mastermind/plans/2026-10-06-tasks-board-phase{2..5}-*.md; skim the shared-file edits of those that touch the same files).

## Output

Write your findings to the file named in your dispatch, and make your final message the same text. Structure: ### Verdict (Ready to build | Needs fixes | Needs the user's decision), ### Spec coverage (the table: requirement, task, status), ### Critical and Important findings (each: where in the plan (task and line), what is wrong, why it matters, the fix), ### Minor findings (short), ### Rulings assessed, ### Questions that need the user. Cite plan line numbers. Do not rewrite the plan; the planner fixes it from your findings. No process narration. Do not dispatch subagents.
