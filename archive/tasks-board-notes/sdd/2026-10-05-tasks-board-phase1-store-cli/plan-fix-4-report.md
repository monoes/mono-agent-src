# Plan fix 4: report (phase 4, the Chrome extension)

Plan: `docs/mastermind/plans/2026-10-06-tasks-board-phase4-extension.md`. Findings: P4-1 only. Fixed 1, not taken 0. Commit eadaf443 (the plan file only). Code read at checkout HEAD e773a9f6 (the phase 1 store is the same as in the drift report).

## P4-1: a page task whose title is only hidden or control characters was dropped, not saved under its address

The report is right. Read in the code: `cleanTitle` is `cutRunes(oneLine(s), 200)` (internal/tasks/clean.go:85) and `deriveTitleNotes` (clean.go:90-103) answers `invalid("a task needs a title or some text")` when the title, the notes and the text all clean to nothing. `Add` calls it first after the Ready gate (store.go:375), before `s.tx` (:405), so that refusal comes before the profile check (:406) and the client-id lookup (:409-419) and touches no database. `strings.TrimSpace("‮\a")` is not empty, so the plan's sink kept the hostile title and the board refused it. Reproduced in a scratch export: the three hidden-title cases fail against the old sink with `invalid input: a task needs a title or some text`.

Fix (the lead's preferred one, no change to the store): the sink files the page under its own title and, when the board answers `ErrInvalid` for a page that has an address, asks once more with the address as the title. Two decisions:

1. I removed the sink's `strings.TrimSpace(in.Title) == ""` test. It was a second, weaker opinion on what a usable title is, and the root of the bug; with the retry it is redundant (a blank title is refused in pure Go, before any database work, then retried with the address). Now the board is the only judge, and the sink copies none of its cleaning, which could drift from it. The smaller edit would have kept the test and added the retry after it; I took the single-rule version and checked it: the existing untitled-page test (`bare`, user-info address) passes through the retry.
2. The retry's result is returned, so a hidden-title page queued for a deleted profile is reported as `unknown profile`, not as a title the board did not like. A page with no address keeps the board's first refusal. The retry is for pages only: a selection of nothing visible stays refused (without the `Kind` gate it would be saved under its page's address; a test pins that).

Why the retry cannot hide another refusal or make a second task: a page sends no text or notes, the actor and source are fixed, so a bad title is the only `ErrInvalid` a second try can cure; any other (deleted profile, malformed client id) is refused again, harmlessly. Both tries carry the same client id and the first creates nothing, so a resend of a hidden-title request finds the task made by the first retry and returns `created: false` (tested). An address's title always starts with its scheme (`u.String()` of a URL with a host), so it survives any cleaning and the second try is never refused for its title.

Where, in the new plan (4195 lines):
- Review Focus item 1 (line 45): names the new test, since the retry is a second path by which an address becomes a title, and user-info is stripped on it (case 3 of the test). New item 6 (line 50): the hidden-title failure mode and its three tests.
- New Ruling bullet (line 83), after the existing page-title ruling (line 82, which stays true).
- Task 2 "Rulings:" line (528): the page title fallback reworded to "whenever the board refuses the title (the sink asks the board once more)".
- Task 2 tests, after `TestTaskSinkPageTaskIsItsTitleOrItsAddress` (687): `TestTaskSinkPageWithAHiddenTitleIsSavedUnderItsAddress` (719; U+202E and BEL, a tag character, hidden characters between spaces with a user-info address; each also sent twice: same task, `created` false; also `Source.Title` empty, no notes), `TestTaskSinkPageTitleWithSomethingVisibleKeepsIt` (750; "Bank<U+202E> login<BEL>" stays "Bank login", not the address), `TestTaskSinkPageWithAHiddenTitleAndNoAddressIsRefused` (765; address "", "/only/a/path", "about:blank": `invalid_input`, nothing filed), `TestTaskSinkPageWithAHiddenTitleForAGoneProfileSaysSo` (781; reports `unknown profile`), `TestTaskSinkSelectionOfHiddenTextIsRefusedNotNamedByItsPage` (796). A plain normal title is asserted by the existing test, unchanged. All names start `TestTaskSink`, so Step 5's and Task 8 Step 12's `-run` patterns select them; the test file's imports did not change.
- Task 2 Consumes list (line 526): `tasks.Store` and `tasks.Task` added, which `addCaptured` and the tests name.
- Task 2 sink (Step 3): `AddCaptured` calls `addCaptured` (1014); `capturedInput` (1024) lost the TrimSpace block and its comment names `addCaptured`; new `addCaptured` (1050) holds the retry; `addressOf` (1067) is unchanged.
- Spec section 11.6 as-built bullet (line 4149): one clause added. It is text the plan inserts into the spec, not an anchor.

## Findings not taken

None.

## Anchors relied on

None new. No Edit step of the plan was changed: the AGENTS.md, SECURITY.md, CHANGELOG.md, README, `ref_tasks.go` and spec anchors of Task 8 are untouched. Counted anyway in the current tree: the spec's `## 12. The macOS menu (P5)` once, `- A command` once.

## Checks run

- Scratch export (`git archive HEAD internal data cmd automations go.mod go.sum`) in the scratchpad, `p4-export`, plus only the Task 1 declarations the sink names (types and constants, no handler) in `internal/extension` and a stand-in `package main` with `openProfileDB` in `cmd/p4probe`. The sink and the test file were extracted from the plan by line range, not copied by hand (the zerolog import dropped, and `TestEveryBridgeAnswersTaskAdd` left out: it needs Task 1's `SetTaskSink` and `extension_serve.go`). Nothing was built or run in the checkout.
- Baseline, the plan's old sink and old tests: 11 PASS. New tests against the old sink: the three hidden-title cases and the gone-profile test FAIL with the refusal above; the visible-title and no-address tests pass (they guard against over-fixing).
- New sink and new tests: `gofmt -l` prints nothing, `go vet` clean, `go test -race -run TestTaskSink`: 16 PASS.
- Mutations of the new sink, each caught: no retry (the hidden-title test fails on all three cases, and the gone-profile test fails); no `Kind` gate (`...SelectionOfHiddenText...` fails: the selection is saved under its address); the raw address as the title instead of `addressOf` (`TestTaskSinkPageTaskIsItsTitleOrItsAddress`, `...HiddenTitleIsSavedUnderItsAddress` and `...NoAddressIsRefused` fail); the first refusal returned instead of the second (the gone-profile test fails).
- The plan: non-ASCII lines 2 before and after (two em dashes that were already there), no space-indented Go line in the new blocks, `grep -nP '[^\x00-\x7F]'` on the extracted Go files prints nothing, and `git diff` shows the nine removed lines are exactly the ones named above.

## Concern (outside P4-1, not checked: there is no browser here)

The extension's JS sends `title: tab.title` (task_bridge, `add`) without any sanitizer, and only `url` passes `pageUrl`. For a page with no `<title>`, Chrome's `tab.title` is, as far as I know, the page's address without its scheme, query string included. If so, a session token the sanitizer strips from `url` would still reach a page task's title and `source_title`, which is the leak Review Focus item 1 exists to prevent. The Go side cannot tell a title from an address. Worth one check in a real Chrome before Task 5, or a rule in the extension that blanks a title that equals the address.

## Round 2: the `tab.title` concern (the lead's request)

Fixed 1 (the title leak), not taken 0. Commit ba503047 (the plan file only). Plan line numbers below are in the plan as it now stands (4294 lines).

Problem: Chrome gives a page with no `<title>` its address as `tab.title`, query string included (not verified here: no browser). Only `url` went through `pageUrl`, so a token could reach a page task's title, its page title, and `chrome.storage` (the outbox), by way of the title.

What I changed:
- `pageTitle(page)` in `task_bridge.js` (2199-2233), exported on `MonoTaskBridge` (2436; Task 4a Produces, 1524; Task 4a Rulings, 1525): `""` when the title is only the page's address, else the title as it came. Called once, in `add()` (2116), before the title is queued; `add()`'s doc (2107) and the file header say so.
- Tests: a `pageTitle` unit test next to the `pageUrl` one (1983; the lead's four cases, plus a root page, `www.` on either side, a port, upper case, padding, a title left from an earlier path, "mail.example is down", a title that starts with the host and goes on in words, no usable address, no title); the content-script path offline, then connected (1814: no `token=abc` in storage or in the request, title `""`, url with `REDACTED`); the menu's *Add page as task* (2766).
- One Ruling bullet (84: Chrome's behaviour unverified here, the rule harmless where Chrome differs, the ASCII-host limit); Global Constraints (22); Review Focus item 1 (45); one clause each in the SECURITY.md text (4217) and spec 11.6 (4247); item 8 in the list only the user's Chrome can check (4294).

Two deviations from the lead's wording, on purpose:
1. Applied once inside `add()`, not at the three places `tab.title` is passed (the lead's lines 2206, 2848, 2894 at the time). All three, and any later entry, go through `add()` with the same title and the raw address, so one call covers them, also before `box.enqueue` (nothing with the token reaches `chrome.storage`), and no call site changed. The function takes anything with `title` and `url`, so `pageTitle(tab)` works too.
2. The rule compares the host only, followed by the end, `/`, `?` or `#`, not host plus path. The literal rule misses a root page (the path is `/`, Chrome's title is the bare host), needs trailing-slash and percent-escape handling (Chrome unescapes spaces in a title, the tab's URL does not), and misses a title left from before a single-page app moved on. Cost: a title that starts with the host and a `/`, `?` or `#` is dropped though Chrome did not write it, and the task is named by that address (harmless). A title that goes on in words ("mail.example - Inbox") stays.

Checks run (scratch only; nothing built in the checkout): `chrome-extension` exported at HEAD, the plan's six JS files extracted by line range, plus Task 5's own edits to `capture_modes.js`, the manifest and the harness list. Before my edit: 29 of 29 bridge and menu tests PASS. After: 32 of 32 PASS, `node --check` clean, no non-ASCII character in the six files, `task_bridge.js` 414 lines (limit 500), no tab in the JS blocks, the plan's non-ASCII lines 2 as before. Mutations of `pageTitle`, each caught by a named test: no call in `add()` (the two end-to-end tests), no boundary after the host ("mail.example is down" gets blanked), no scheme rule, no protocol guard (a `file:` tab), the host keeping `www.`, no lower-casing, no trimming. One guard I had written, an empty-host check, survived its mutation: no test could fail it (the parser gives every http or https URL a host), so I removed it.

Anchors relied on: none. The SECURITY.md, spec and Step 13 edits change text the plan inserts, not an Edit anchor.

Wording: every sentence that stated Chrome's behaviour as fact is hedged ("is believed to"), in the Global Constraints, Review Focus, the code comments and a test name; the spec 11.6 and SECURITY.md clauses say only what the code does, since they outlive the plan.

A side effect to know: a page with no title whose address the sanitizer drops (over 2,048 bytes, or not http or https) is now refused as "This page has no title or address to add"; before, its raw address went in as the title. That is the plan's own fail-closed rule (the Rulings list: an address without the sanitizer is dropped, not kept raw).

Not covered: Chrome's real title for a page with no title (item 8 of the user's list); a host that is not plain ASCII; a token a page writes into its own `<title>`, which is the page's text like any other (the operator reads a task before approving it).

## Round 3: the `ref tasks` test goes in a new file (the lead's request)

Fixed 1 (the 500-line rule for P1's `ref_tasks_test.go`), not taken 0. Commit 67402a57 (the plan file only). Plan line numbers are in the plan as it now stands.

Fact check: `ref_tasks_test.go` is 497 lines in the tree now (the lead said 495; one more P1 commit landed), so any append breaks the rule. The plan has no Task 0 size note; its size notes are the Global Constraints list (line 26) and the `wc -l` checks of Tasks 4a and 6. The plan's only `ref tasks` test is `TestRefTasksSaysWhereChromeTasksLand`; it needs `refTasksText` and nothing else (no helper of P1's), so the new file is 15 lines.

What I changed:
- Task 8 Step 1 (4164-4187): "Create `cmd/monoagentcli/ref_tasks_chrome_test.go`", said to be a new file of package `main` beside P1's two ref test files, with its complete content (package, imports, a one-line comment, the same test, unchanged). Name chosen as P2's `ref_tasks_mcp_test.go` and P5's `ref_tasks_os_test.go`: `ref_tasks_<surface>_test.go`.
- Task 8 Files (4155-4158) and the commit's `git add` (4266): the new file, no longer `ref_tasks_test.go`. Consumes (4161): P1's tests are named as the tests the new text has to pass, and `refSection` as a helper in reach.
- File structure: the Create list (57) and the Modify list (66). Global Constraints (26): `ref_tasks_test.go` 497 added to the list of long files. One Ruling bullet (88).
- Task 0 Step 3 (124-125): its grep of P1's test inside `ref_tasks_test.go` was a check of a file the plan appended to; it is now `ls` of P1's two ref test files, with the Expected line saying the phase appends to neither.

Checks run (scratch only): `git archive HEAD internal data cmd automations go.mod go.sum AGENTS.md SECURITY.md` (the ref tests read the two documents), the new file extracted from the plan by line range, the Step 2 block spliced into the scratch `ref_tasks.go` (head, block, tail; both headings single lines). Step 1 as written: `go test -run TestRefTasks` fails only the new test, with `does not mention "FROM CHROME"` (and "MonoAgent Bridge", "Saving into"). Step 2 as written: `gofmt -l` prints nothing and `-run 'TestRefTasks|TestEveryTaskCommand|TestRootHelpPoints'` PASSes, 20 tests, all of P1's ref tests as they now stand (the lease scans, the gate walk, the anchors, the limits) and the new one. Largest code blocks the plan creates: 411, 414 and 319 lines, all under 500.

Anchors relied on: `WHO MAY DO WHAT` in `ref_tasks.go` (Step 2's anchor, not changed by me): once as a whole line and once in the file. Counted once each in the current tree, as a courtesy: the AGENTS.md table header and its last row, the SECURITY.md bullet, `## [Unreleased]` in CHANGELOG.md, the README bullet, the spec's `- A command` bullet and `## 12. The macOS menu (P5)`.

Concern for the lead (not mine to fix): P5's Ruling 20 and its Task 6 step 1 replace `refTaskSub` and `refHasFlag` in P1's `ref_tasks_test.go` and add `refTaskCommands` and `refTaskCallee` to it. With 3 lines of headroom that file will probably pass 500 lines; the new helpers may belong in a file of P5's own (same package).
