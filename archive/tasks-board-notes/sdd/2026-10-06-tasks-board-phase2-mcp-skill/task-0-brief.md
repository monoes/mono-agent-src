### Task 0: Confirm the contract

**Files:** none (read-only checks).

**Interfaces:**
- Consumes: P1 as built. Produces: the go-ahead for Task 1.

This plan was written while P1 was being built, from spec section 6 and P1's plan. Confirm every item below against the code in this worktree. If anything differs from what is written here, spec section 6 wins: stop and tell the lead which item differs and how; do not adapt the plan on your own. (Exception: the document anchors of Step 6 may have been reworded in P1's reviews; if one is missing, use the sentence that says the same thing and report it at hand-over.)

- [ ] **Step 1: P1 is complete on this branch**

Run: `git log --oneline -40` and `ls internal/tasks cmd/monoagentcli/task*.go cmd/monoagentcli/ref_tasks.go`
Expected: the commits of all thirteen tasks of P1's plan (their subjects may be reworded, and review rounds add `fix(tasks)`, `test(tasks)` and `docs(tasks)` commits), the last of them P1's verification; `internal/tasks` holds `claims.go`, `clean.go`, `model.go`, `ops.go`, `read.go`, `store.go`, `watch.go`; the CLI files `task.go`, `task_read.go`, `task_ops.go`, `task_agent.go`, `ref_tasks.go` exist. If a file is missing, P1 is not complete: stop and tell the lead.

- [ ] **Step 2: The store's methods this phase calls**

Run each (one call each) and compare the signature line:

| Command | Expected signature |
|---|---|
| `go doc ./internal/tasks NewStore` | `func NewStore(db *sql.DB) *Store` |
| `go doc ./internal/tasks Store.Profile` | `func (s *Store) Profile(ctx context.Context, profileID string) (Profile, error)` |
| `go doc ./internal/tasks Store.Add` | `func (s *Store) Add(ctx context.Context, profileID string, in AddInput, actor Actor) (Task, bool, error)` |
| `go doc ./internal/tasks Store.List` | `func (s *Store) List(ctx context.Context, profileID string, f Filter, actor Actor) ([]Task, error)` |
| `go doc ./internal/tasks Store.Get` | `func (s *Store) Get(ctx context.Context, profileID string, id int64) (Task, []Event, error)` |
| `go doc ./internal/tasks Store.Next` | `func (s *Store) Next(ctx context.Context, profileID string, actor Actor, claim bool, lease time.Duration) (*Task, error)` |
| `go doc ./internal/tasks Store.Claim` | `func (s *Store) Claim(ctx context.Context, profileID string, id int64, actor Actor, lease time.Duration) (Task, error)` |
| `go doc ./internal/tasks Store.Comment` | `func (s *Store) Comment(ctx context.Context, profileID string, id int64, text string, actor Actor) (Task, error)` |
| `go doc ./internal/tasks Store.Finish` | `func (s *Store) Finish(ctx context.Context, profileID string, id int64, o Outcome, actor Actor) (Task, error)` |
| `go doc ./internal/tasks Store.Release` | `func (s *Store) Release(ctx context.Context, profileID string, id int64, note string, actor Actor) (Task, error)` |
| `go doc ./internal/tasks Store.Move` | `func (s *Store) Move(ctx context.Context, profileID string, id int64, to Status, p Placement, actor Actor) (Task, error)` |
| `go doc ./internal/tasks Store.Counts` | `func (s *Store) Counts(ctx context.Context, profileID string) (Counts, error)` |

- [ ] **Step 3: The types and their JSON names**

Run: `grep -n 'json:"' internal/tasks/model.go`
Expected: `Source` {kind, url, title, app}; `Claim` {by, until (time.Time), stale}; `LastEvent` {actor, kind, at}; `Task` {id, profile_id, title, notes, status, position, source, claim (pointer), last_event (pointer), created_at, updated_at}; `Event` {id, at, actor, kind, from_status, to_status, note}; `Profile` {id, name}; `Counts` {inbox, ready, in_progress, review, done, stale}.
Run: `grep -n 'type Filter\|type AddInput\|type Outcome\|type Actor\|Human ActorKind\|AgentTasksPerHour\|ErrNotFound\|ErrNotClaimant\|type ClaimedError\|func ParseStatus' internal/tasks/model.go`
Expected: `Filter{Statuses []Status; Source; ClaimedBy; Stale; Limit int}`, `AddInput{Title, Notes, Text, Ready, SourceKind, SourceURL, SourceTitle, SourceApp, ClientID}`, `Outcome{Result, Question}`, `Actor{Kind ActorKind; Name string}` with `Human`, `Agent`, `Capture`, `AgentTasksPerHour = 20`, the seven `Err*` values, `ClaimedError{By string; Until time.Time}` matching `ErrClaimed`, `ParseStatus(name string) (Status, error)`.

- [ ] **Step 4: The rules the tools rely on**

Run (one call each):
- `grep -n "func defaultStatuses" -A 6 internal/tasks/read.go`. Expected: an `Agent` gets `StatusReady, StatusInProgress, StatusReview`.
- `grep -n "func sourceKindFor" -A 8 internal/tasks/store.go`. Expected: an `Agent`'s task is `SourceAgent`; another requested source is refused.
- `grep -n "func needName" -A 10 internal/tasks/claims.go` and `grep -n "nameRE" internal/tasks/clean.go`. Expected: an agent's name is 1 to 64 characters of `[A-Za-z0-9._#@:-]`.
- `grep -n "func (s \*Store) Next" -A 12 internal/tasks/claims.go`. Expected: with `claim` false it only reads; nothing to do is a nil task and a nil error.
- `grep -n "func (a Actor) Label" -A 10 internal/tasks/model.go`. Expected: a `Human` is written `you`, an `Agent` by its `Name`.
- `grep -n "events" internal/tasks/claims.go`. Expected: the lead's P1 rule, a claim refused with `limit` when the task already holds 2,000 events (Task 4 seeds exactly 2,000). If it is missing, stop and tell the lead.
- `grep -rn '"capture"' internal/tasks/*.go`. Expected: the reserved agent labels (`you`, `agent`, `capture`, `chrome`, `os`) refused as an agent's name.

- [ ] **Step 5: The CLI pieces the tests and the texts use**

Run (one call each):
- `grep -n 'func newTaskCmd\|PersistentFlags().String("as"' cmd/monoagentcli/task.go`
- `grep -n 'func newTaskTestDB\|func runTask\|func mustTaskJSON\|func failedTaskJSON\|type addedJSON\|type taskJSON' cmd/monoagentcli/task_test.go`
- `grep -n 'func taskCut' cmd/monoagentcli/task_read.go`
- `grep -n 'writeJSONTo' cmd/monoagentcli/task_read.go cmd/monoagentcli/task_agent.go cmd/monoagentcli/task.go`
- `grep -n 'callerFor(flagAs(cmd))' cmd/monoagentcli/task_read.go`
- `grep -n '^func id(' cmd/monoagentcli/*_test.go`

Expected: all present; `list --json` is `{"profile", "tasks"}`, `show --json` is `{"profile", "task", "events"}`, `next --json` and `claim --json` are `{"profile", "task"}`, `add --json` is `{"profile", "created", "task"}`, each holding the store's `tasks.Task` and `tasks.Event` values as they are (`"tasks": ts`, `"task": t`, `"events": events`), not a view of their own: Task 6's pipe tests compare the tools against them field by field. `list` takes its actor from `callerFor(flagAs(cmd))`, so `list --as reader` lists an agent's default columns. `addedJSON` has `Created`, `Profile`, `Task taskJSON` with `ID`. P1's test package has a function `id(n int64) string`: never declare another package-level `id` in `cmd/monoagentcli` tests.

- [ ] **Step 6: The P1 documents this phase edits**

Run (one call each):
- `grep -n '^## Task board' AGENTS.md SECURITY.md`
- `grep -n 'Where the board can be reached from\|^| Session-start hook |' AGENTS.md`
- `grep -n 'people, orgs; not the' AGENTS.md`
- `grep -n 'is the interface in this release' AGENTS.md`
- `grep -n 'so what the operator approved is what it reads.\|No HTTP route and no new port' SECURITY.md`
- `grep -n 'Task board, phase 1' CHANGELOG.md` and `grep -n 'the macOS menu follow in later releases.' CHANGELOG.md`
- `grep -c 'SEE ALSO' cmd/monoagentcli/ref_tasks.go`

Expected: a `## Task board` section in both files; in AGENTS.md P1's table "Where the board can be reached from" (header `| Surface | Reaches the board through |`), whose last row is `| Session-start hook | ...`, and two sentences once each: "Most of this surface (vault, secrets, people, orgs; not the `api_*` tools) is ..." and "`monoagentcli task` is the interface in this release." (at the top of the `## Task board` section); in SECURITY.md the end of P1's first bullet (the gate) and the bullet "No HTTP route and no new port" once each; the phase 1 bullet, ending in its "later releases" sentence, once in CHANGELOG.md; `SEE ALSO` exactly once in `ref_tasks.go`.

No commit: nothing changed.

---

