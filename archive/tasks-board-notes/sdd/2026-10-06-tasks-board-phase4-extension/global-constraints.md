# Global Constraints (verbatim copy of the plan's sections; every task's requirements include these)

Source: docs/mastermind/plans/2026-10-06-tasks-board-phase4-extension.md, sections Global Constraints and Review Focus.

## Global Constraints

- Method `task.add` on the extension request channel (`ws://127.0.0.1:9222/monoagent`), not an HTTP route. No new port, no new HTTP route, no new extension permission (D22, D23, D28).
- Params: `client_id`, `text`, `url`, `title`, `kind` (`selection`, `page`, `note`), and `profile`, which is required: an empty id, or one that is not a profile in the database, is refused (`invalid_input`). Reply `{id, created}` (11.2).
- Every capture lands in Inbox, in the profile the request names, as the actor `Capture` named `chrome`, source kind `chrome` (D6, D7, 5.1).
- Limits (4.6): title 200 characters; notes 64 KiB; URL 2,048 bytes; page title 200; app name 100; client id 64 of `[A-Za-z0-9_-]`; 2,000 non-archived tasks per profile. Text is capped at 64 KiB in the page and again in Go (11.4).
- Outbox (11.3): entries `{client_id, text, url, title, kind, profile, at}` in `chrome.storage.local`; at most 200 entries and 1 MiB; an entry leaves only when the host replies `created` or `duplicate`; a refusal that would repeat is dropped and reported; `offline`, `busy`, `timeout`, `unavailable` and a missing `task.add` keep it; it flushes on every connect and on a minute alarm while it is not empty, and shows a count on the badge while it holds anything. A task is never created without being queued first, and it keeps the profile it was queued under.
- The URL and the page title come from `sender.tab`, never from the message; the URL passes the extension's existing sanitizer before it leaves the browser and Go re-checks it, and a title that is only that address is not sent (Chrome is believed to give a page with no title its address, query string included: see the Rulings list); payloads are never logged; the click handlers ignore untrusted events (11.4).
- No profile known: nothing is added and the control says "Choose a profile first" (4.5, 11.1).
- Feedback (11.1): "Added to Inbox in Work", or "Saved: will sync when MonoAgent is running". No `notifications` permission.
- Manifest version 1.5.0 goes to 1.6.0 (11.5).
- Files stay under 500 lines; new code goes in new files where the existing file is long (`background.js` 1,352 lines, `sidepanel.js` 1,203, `request.go` 488, `highlight_page.js` 479, P1's `ref_tasks_test.go` 497).
- Shared documents get append-only edits (15.3): one row in P1's AGENTS.md surfaces table, one new SECURITY.md bullet before P1's `- **No HTTP route and no new port.** The task board does not listen on the network.` (left as it is), one CHANGELOG bullet.
- Commits are `feat(tasks): ...` or `docs(tasks): ...`, each ending with the trailer `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.

## Review Focus

Failure modes the spec implies and no happy-path test would catch; each has a test in the task that owns the code, and each test fails when its rule is removed.

1. A page address carrying user-info (`https://user:secret@host/`) or a session token must never be stored: not in the task's URL, not in a page task's title (which falls back to the address, and which Chrome is believed to fill with the address for a page that has none), not in `chrome.storage`. (Task 2 `TestTaskSinkPageTaskIsItsTitleOrItsAddress` and `TestTaskSinkPageWithAHiddenTitleIsSavedUnderItsAddress`; Task 4a "offline, the task waits ..." checks the stored outbox, "a page with no title of its own ..." and "pageTitle drops ..." the title; Task 5 "Add page as task sends the page ..." and "Add page as task on a page with no title of its own ...".)
2. Text hidden with `display:none` and hidden characters (bidi controls, Unicode tag characters, a terminal escape, invalid UTF-8) must not reach a task. Text hidden by colour, size or position is still selected; the operator gate is the defence for that, and the security text says so. (Task 2 `TestTaskSinkCleansWhatAPageSent`; Task 5 "Add selection as task reads the selection as the reader sees it"; Task 6 browser test "adds the selection as a task, as the reader sees it".)
3. Two adds a moment apart, two flushes at once, a resend after a lost reply, or a burst of 200 queued tasks: none lost, none sent twice under a new id, never more than one request in flight (the bridge answers a ninth with `busy`), and a storage read that fails wipes nothing. (Task 3 "adds and removals a moment apart all land", "a storage read that fails wipes nothing"; Task 4a "two adds a moment apart both reach MonoAgent, and both say so", "two flushes at once send each task once", "a burst of 200 ...", "busy and internal stop the flush ...".)
4. A daemon started from an agent's shell inherits `CLAUDECODE`: a Chrome capture must stay a capture (source `chrome`, Inbox, no hourly agent limit). (Task 2 `TestTaskSinkIsAChromeCaptureWhateverTheEnvironment`.)
5. A task queued for a profile that is deleted before it syncs is refused, dropped from the outbox and reported with its text, never filed into another profile. (Task 2 `TestTaskSinkRefusesAProfileThatIsGone`; Task 4a "a refusal that would repeat drops the task and reports it", "a queued task keeps the profile it was queued under".)
6. A page whose title is made only of hidden or control characters (a bidi control and BEL, a tag character) is not blank to a white-space test, yet the board cleans it to nothing and refuses it: the page must not be lost for that. It is filed under its address, once however often the request is repeated; with no address it is refused and reported, as is a selection of nothing visible. (Task 2 `TestTaskSinkPageWithAHiddenTitleIsSavedUnderItsAddress`, `TestTaskSinkPageWithAHiddenTitleAndNoAddressIsRefused`, `TestTaskSinkSelectionOfHiddenTextIsRefusedNotNamedByItsPage`.)

