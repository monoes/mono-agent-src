# Global Constraints (verbatim copy of the plan's sections; every task's requirements include these)

Source: docs/mastermind/plans/2026-10-06-tasks-board-phase3-gui.md, sections Global Constraints and Review Focus.

## Global Constraints

- Placement: `NAV_ITEMS` gets `{id: 'tasks', labelKey: 'tasks', icon: SquareKanban, section: 'DATA'}` right after Documents; `persistentPages` gets `tasks: <Tasks isActive={activePage === 'tasks'} />` (an id missing there silently shows the dashboard); `sidebar.nav.tasks` in `en.json` and `es.json`. The badge is Inbox plus Review (tooltip: the split), driven by the `tasks:changed` event, so it is right before the tab was ever opened.
- Event: `tasks:changed {profile_id, rev, inbox, review}`. The watcher polls the board revision in process every two seconds and is restarted on startup and profile switch and stopped on shutdown, like the document watcher.
- Data path: actions shell out to `monoagentcli task ... --json`; the whole board is one in-process `Store.Board` read, asked as the operator, in the `task board --json` document (Ruling: P1 made `task board` operator-only); Done shows the 50 most recent.
- Columns `inbox`, `ready`, `in_progress`, `review`, `done`; `archived` is hidden. A task new to a column goes to the top of Inbox, Review and Done and to the bottom of Ready and In progress.
- Interaction: drag with the app's mouse ghost drag (no native drag), a glowing drop zone, a drop indicator between cards, column auto-scroll; the move is applied at once and the CLI call follows; a refusal reverts it and says why.
- Keyboard: Tab reaches cards; Enter opens; Shift+Left and Shift+Right move to the neighbouring column; Alt+Up and Alt+Down reorder; `A` approves an Inbox card; `N` opens quick add; `/` focuses search; Escape closes the drawer; an `aria-live` region announces each move.
- Motion: cards enter with a short scale and fade; reorders animate by FLIP; a Done card gets a check pulse; a claim pulses softly. All of it is off under `prefers-reduced-motion` (the global rule plus a JS check).
- Empty states, verbatim: Inbox "Highlight text in Chrome and choose MonoAgent, Add selection as task, or select text in any app: Services, Add to MonoAgent Tasks"; Ready "Approve tasks from Inbox so agents can pick them up".
- A stale claim shows in amber. Columns scroll on their own; the board scrolls sideways under 1,100 px.
- No new dependencies, no new HTTP route, no new port. Dark theme. English and Spanish. Selects follow the select policy (`src/selectStyle.test.js`; this plan adds no `<select>`).
- Files stay under 500 lines. Commits are `feat(tasks): ...` or `docs(tasks): ...`, each ending with the trailer `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.

## Review Focus

Failure modes the spec implies and no happy-path test would catch; each has a test in the task that owns the code.

1. A pasted paragraph is one long line: it must not lose its tail. Over 120 characters it goes whole as text (the CLI keeps it in the notes); the drawer's title field takes at most 200. (Task 7b: "sends a line over 120 characters whole, as text"; Task 9: the `maxlength` check)
2. Approving text the person never saw: a card with notes, or a clamped title, must open the drawer, never reach Ready, whatever the gesture: Approve, `A`, Shift+Right or a drop on Ready. (Task 7a: "opens a card that has more to read instead of approving it"; Task 8: "sends a card with more to read to the drawer from A and from Shift+Right", "opens a card with more to read dropped on Ready instead of moving it"; Task 10c: "opens a card with notes instead of moving it to Ready, and says why")
3. Text that starts with a dash (a title `-x fix`, notes `--help me`, a comment `-looks odd`) must reach the CLI as text, never as a flag. (Task 1: `TestTaskBindingsPassTheirArgumentsExactly`)
4. A refetch must not undo an optimistic move, a failed confirming read must not drop it, and the operator's own move must not toast. (Task 5: "keeps a move until the read that includes it", "keeps a confirmed move when the confirming read fails", "toasts what an agent did, never the operator's own move")
5. The badge before the tab was ever opened, and after the window reloads on a profile switch: the first `tasks:changed` was emitted before anything listened, and until `startup` has chosen the profile the app answers for `default`. (Task 2: `TestTaskPulseReadsTheActiveBoard`; Task 11: the two Sidebar badge tests: an older revision of the same profile never overwrites a newer one, the badge asks only once `IsReady` is true, and another profile's value replaces it)

Close runners-up, also pinned: the app started from an agent's shell is read-only up front and still shows its board, although the CLI refuses `task board` there and the store refuses every actor but the operator, so the binding asks as the operator (Task 1: `TestTaskBoardIsReadInProcessEvenUnderAnInheritedMarker`; Task 10c); `/`, `N` and `A` stay quiet while the page is hidden or a field has the focus (Task 10c); untrusted notes render as markdown without images or unsafe links (Task 9); the Done pulse lasts through the confirming read (Task 10a); a drop below the cut Done column's last card stays visible (Task 8); a profile that is not there ends the board watcher with one warning in the app's log, and `TaskPulse` answers `{}` for it (Task 2).

