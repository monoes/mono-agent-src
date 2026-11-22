### Task 0: Confirm the contract and prepare the worktree

**Files:** none change. `npm ci` creates `wails-app/frontend/node_modules` and the build creates `wails-app/frontend/dist`, both ignored by git.

**Interfaces:**
- Consumes (P1, `internal/tasks`): `NewStore(*sql.DB) *Store`; `(*Store).Watch(ctx, profileID string, interval time.Duration, fn func(Change)) error` (it blocks, so the app runs it in a goroutine; it returns nil when `ctx` ends, an ErrInvalid error at once for a profile that does not exist, and an error that wraps ErrNotFound when the profile is deleted while it is watched); `Change{Rev int64; Counts Counts}`; `Counts{Inbox, Ready, InProgress, Review, Done, Stale int}`; `(*Store).Profile(ctx, profileID string) (Profile, error)` (ErrInvalid for an unknown profile); `(*Store).Rev(ctx, profileID) (int64, error)` and `(*Store).Counts(ctx, profileID) (Counts, error)` (neither checks the profile: an unknown one reads revision 0 and no cards); `(*Store).Board(ctx context.Context, profileID string, doneLimit int, actor Actor) (Board, error)` (the operator's whole-board read: every actor but `Actor{Kind: Human}`, the zero `Actor` included, is refused with ErrOperatorOnly before the profile is looked up) with `Board{Profile, Rev, Counts, Tasks map[Status][]Task}` (JSON `profile`, `rev`, `counts`, `tasks`; a column with no card is `[]`); `(*Store).Add(ctx, profileID string, in AddInput, actor Actor) (Task, bool, error)`; `Actor{Kind, Name}` with `Human`; `AddInput{Title string, ...}`. From `internal/orgsign`: `AgentContextMarker() string`, `AgentContextMarkers() []string`.
- Consumes (P1 CLI, as the app calls it): `task board --done-limit N` (operator-only; the app reads the same document in process, Task 1), `task show ID`, `task add [--source app] [--ready] [--notes=TEXT] [--stdin] -- TITLE`, `task edit ID [--title=T] [--notes=N]`, `task move ID STATUS [--before ID|--after ID|--top|--bottom]`, `task approve ID... [--top]`, `task archive ID...`, `task unarchive ID...`, `task comment ID -- TEXT`; JSON documents `{"profile","task"}`, `{"profile","tasks"}`, `{"profile","task","events"}`, `{"profile","created","task"}`, and the board `{"profile","rev","counts","tasks":{"inbox":[],"ready":[],"in_progress":[],"review":[],"done":[]}}`; a task is `{id, profile_id, title, notes, status, position, source:{kind,url,title,app}, claim:{by,until,stale}|null, last_event:{actor,kind,at}|null, created_at, updated_at}`; an event is `{id, at, actor, kind, from_status, to_status, note}`; the operator's actor label is `"you"`; errors are `{"error","code"}` on stdout with exit 2 or 3.
- Produces: nothing. If anything below differs from this plan, spec section 6 wins: stop and tell the lead which line differed and what the code says.

- [ ] **Step 1: The branch holds P1**

Run (separate calls): `git branch --show-current`, then `git merge-base --is-ancestor feat/tasks-board HEAD`, then `ls internal/tasks`, then `ls cmd/monoagentcli | grep '^task'`.
Expected: `feat/tasks-board-gui`; the second exits 0; `internal/tasks` lists `claims.go clean.go doc.go model.go ops.go read.go store.go watch.go` (and tests); the CLI lists `task.go task_agent.go task_ops.go task_read.go` (and tests).

- [ ] **Step 2: The package's names**

Run each: `go doc ./internal/tasks Store.Watch`, `go doc ./internal/tasks Store.Profile`, `go doc ./internal/tasks Change`, `go doc ./internal/tasks Store.Rev`, `go doc ./internal/tasks Store.Counts`, `go doc ./internal/tasks Store.Board`, `go doc ./internal/tasks Board`, `go doc ./internal/tasks Counts`, `go doc ./internal/tasks Store.Add`, `go doc ./internal/tasks NewStore`, `go doc ./internal/tasks Actor`, `go doc ./internal/orgsign AgentContextMarker`, `go doc ./internal/orgsign AgentContextMarkers`, then `grep -n 'reservedNames' internal/tasks/store.go`.
Expected: the signatures in the Interfaces block above, word for word, and `go doc ./internal/tasks Store.Board` says that every actor but the operator is refused with ErrOperatorOnly; `reservedNames` lists `you`, `agent`, `capture`, chrome and os (no agent can call itself `you`, so an event by `you` is always the operator's, which Tasks 4b and 9 rely on).

- [ ] **Step 3: The frontend's packages and a first build**

Run: `df -h /Users/morteza/Desktop/monoes` (expected: more than 3 GB available; if not, stop and tell the lead), `node --version` (expected: 22.22 or later in 22, 24.15 or later in 24, or 26 and up: the lock's engines) and `npm --version` (expected: 12 or later; older npm hits npm/cli#7622 in `npm ci`, which is why CI installs npm 12), then `npm --prefix wails-app/frontend ci`, then `npm --prefix wails-app/frontend run build`, then `ls wails-app/frontend/dist`, then `git status --short`.
Expected: `ci` and `build` succeed; `dist` holds `index.html`; `git status` shows no new file (both folders are ignored). If `npm ci` fails, stop and report its output: do not use `npm install`, which rewrites the lock file.

- [ ] **Step 4: Every icon this plan imports exists**

Run: `grep -cE 'declare const (SquareKanban|Search|Plus|X|Check|CheckCheck|Undo2|Globe|AppWindow|Terminal|Sparkles|Bot|MessageCircleQuestionMark|CircleCheck|CircleQuestionMark|Inbox|ListTodo|LoaderCircle|ExternalLink|Archive|Pencil|Send|Copy|TriangleAlert|NotebookText|BookOpen): ' wails-app/frontend/node_modules/lucide-react/dist/lucide-react.d.ts`
Expected: `26`. A smaller number: find the missing name with one grep each and tell the lead (do not substitute an icon silently).

- [ ] **Step 5: The CLI as the app will call it**

Run `mktemp -d` and write down the directory it prints; below it is `T`. Then, as separate calls, with `T` written out in full:
1. `go build -o T/monoagentcli ./cmd/monoagentcli`
2. `mkdir T/home`
3. `env -i HOME=T/home PATH=/usr/bin:/bin T/monoagentcli --profile default --json task add --source app --notes=--help -- "-x first"` — expected: exit 0, `"created":true`, the task's `"title":"-x first"`, `"notes":"--help"`, `"status":"inbox"`, `"source":{"kind":"app",…}`, `"claim":null`, `"last_event":{"actor":"you","kind":"created",…}`.
4. `env -i HOME=T/home PATH=/usr/bin:/bin T/monoagentcli --profile default --json task add --source app --ready -- second` — expected: `"status":"ready"`.
5. `env -i HOME=T/home PATH=/usr/bin:/bin T/monoagentcli --profile default --json task add --source app -- third` (id 3, Inbox).
6. `env -i HOME=T/home PATH=/usr/bin:/bin T/monoagentcli --profile default --json task board --done-limit 50` — expected: top-level keys `profile` (`id`, `name`), `rev` (a number), `counts` (`inbox`, `ready`, `in_progress`, `review`, `done`, `stale`), `tasks` with exactly the five keys `inbox`, `ready`, `in_progress`, `review`, `done`, each an array (empty ones `[]`).
7. `env -i HOME=T/home PATH=/usr/bin:/bin T/monoagentcli --profile default --json task move 1 ready --before 2` — expected: `{"profile":…,"task":{"id":1,…,"status":"ready",…}}`.
8. `env -i HOME=T/home PATH=/usr/bin:/bin T/monoagentcli --profile default --json task edit 1 --title=--renamed` — expected: title `--renamed`.
9. `env -i HOME=T/home PATH=/usr/bin:/bin T/monoagentcli --profile default --json task comment 1 -- -looks odd` — expected: exit 0, `"last_event":{"actor":"you","kind":"comment",…}`.
10. `env -i HOME=T/home PATH=/usr/bin:/bin T/monoagentcli --profile default --json task show 1` — expected: `{"profile","task","events"}`; each event has `id`, `at`, `actor`, `kind`, `from_status`, `to_status`, `note`.
11. `env -i HOME=T/home PATH=/usr/bin:/bin T/monoagentcli --profile default --json task approve 3` then `… task archive 3` then `… task unarchive 3` — expected: `{"profile","tasks":[…]}` each time; after `unarchive` task 3 is `ready` again.
12. `env -i HOME=T/home PATH=/usr/bin:/bin CLAUDECODE=1 T/monoagentcli --profile default --json task move 1 inbox` — expected: exit 3, stdout `{"code":"operator_only","error":"CLAUDECODE is set, …"}`.
13. `env -i HOME=T/home PATH=/usr/bin:/bin CLAUDECODE=1 T/monoagentcli --profile default --json task add --source app -- x` — expected: exit 3, code `invalid_input` (this is why the app goes read-only under a marker, not only on `operator_only`).
14. `env -i HOME=T/home PATH=/usr/bin:/bin T/monoagentcli task add --help` — expected: the `--source` line names `app` (the lead passed this on to P1). If it does not, say so in your report and go on: step 3 already showed the CLI accepts it.
15. `env -i HOME=T/home PATH=/usr/bin:/bin CLAUDECODE=1 T/monoagentcli --profile default --json task board` — expected: exit 3, code `operator_only` (`board` is operator-only, which is why `TaskBoard` reads in process, Task 1).

Leave `T` in place (it is a temporary directory; no `rm -r`). Nothing to commit in this task.

