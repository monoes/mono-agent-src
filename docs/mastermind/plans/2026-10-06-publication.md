# Publication Implementation Plan

> **For agentic workers:** Use mastermind-execute to implement this plan task by task.

**Goal:** Store and display profile-scoped publications with automatic capture, CLI, MCP and desktop access.

**Architecture:** A shared SQLite repository stores normalized publication entries. Execution adapters capture successful publishing operations; custom publishers register explicitly. All access surfaces share that repository.

**Tech Stack:** Go, SQLite migrations, Cobra, MCP, Wails, React.

## Global constraints

Preserve existing uncommitted user changes. Never repeat a remote publication because tracking failed. Exclude reads, likes, drafts and private messages. Scope every query and write to the execution profile. No real-account publishing during validation.

### Task 1: Repository and CLI

- [x] Create `data/migrations/061_publications.sql`, `internal/publication/store.go`, and repository tests. Define `Entry` with string fields `ID, ProfileID, Kind, Platform, Title, Body, URL, RemoteID, ParentURL, Account, WorkflowID, ExecutionID, NodeID, AgentID, OrgID, RoleID, PublishedAt, RecordedAt, IdempotencyKey`; `Media []string`. Define `Filter` with `Search, Platform, Kind, WorkflowID, AgentID, Since, Until string`, `Limit, Offset int`. JSON uses snake_case.
- [x] Implement `NewStore(db *sql.DB, profileID string) *Store`, `Register(ctx context.Context, entry Entry) (*Entry,error)`, `List(ctx context.Context, filter Filter) ([]Entry,error)`, `Get(ctx context.Context,id string) (*Entry,error)`, `Stats(ctx context.Context) (map[string]interface{},error)`. Expose `ErrNotFound`. Validate platform/kind/body-or-title-or-url/media, dates, paging. Idempotency keys belong to a profile; deduplicate remote identity within profile/platform/account/kind.
- [x] Create `cmd/monoagentcli/publication.go`, register it in `root.go`. Supply list/get/register --stdin-json/stats, filter flags and mapped exits.
- [x] Run `go test ./internal/publication ./cmd/monoagentcli` and check JSON with a temporary database.

### Task 2: Automatic capture

- [x] Create `internal/publication/capture.go` with independent input/output structures (no workflow dependency). Normalize an explicit inventory of known publishing node types/operations and successful item results. Record content and returned identity without raw credentials. Support partial-success items; rejected/error results create no entry.
- [x] Integrate capture into `internal/workflow/execution.go` and `cmd/monoagentcli/node.go`, preserving execution profile and using a unique standalone run ID. Add browser per-item hooks where final output does not include resolved content. Check legacy social CLI publication paths and integrate shared recording where they bypass nodes.
- [x] Capture tracking errors as warnings, preserving successful publishing status. Test success, error outputs, per-item content, duplicate registration, tracking failure, and nonpublishing operations.

### Task 3: MCP, assistant tools and custom workflow node

- [x] Create `internal/mcp/publication.go` and append tools in `allTools()`. Reuse repository, filters and scope. Read tools are always available; register is mutating.
- [x] Add assistant publication tools through focused handlers in `internal/ai/chat/publication.go` and minimal dispatch/definitions changes.
- [x] Register `publication.register` in `internal/noderegistry` with a schema and node implementation that accepts explicit records and context source IDs.
- [x] Update `AGENTS.md`, offline reference and agent-facing publishing guidance. Test MCP gating/isolation and registration node.

### Task 4: Desktop

- [x] Add `wails-app/app_publication.go` bridge methods calling CLI JSON. Update bindings additively, preserving user modifications.
- [x] Create `wails-app/frontend/src/pages/Publication.jsx`, API methods, sidebar navigation, App page mounting, and English/Spanish translation keys. Provide filter/search/paging/stats/detail/open URL, execution navigation, activation/profile refresh and visible loading/error states.
- [x] Run frontend publication tests and build, and compile the Wails backend.

### Task 5: Integration review

- [x] Run affected Go suites and frontend verification; inspect diff for profile leaks, duplicate remote writes and coverage gaps. Resolve defects, update coverage documentation and mark the plan complete.

## Verification result

Publication repository, CLI, workflow registration, MCP/assistant, automatic publishing capture, source attribution, profile isolation, retry/partial-success tracking, org grant classification and frontend checks passed. Browser preview used mocked publications; no live account publishing occurred. CLI default and nosocial builds passed; frontend production build and Wails bridge compilation/tests passed.

The environment-sensitive test fixtures were corrected: the cancellation helper symlinks the signed system executable, discovery assertions allow legitimate system fallback installations while checking ordering and deduplication, the missing-runtime test injects failed discovery without invoking an installed agent, and capture/coder fixtures use canonical temporary paths. The previously failing cases and the full `go test ./...` suite now pass with the default macOS TMPDIR (3,984 tests passed across 117 tested packages; 11 packages have no tests). Application runtime discovery behavior is unchanged.

Automatic capture covers native browser posts/comments/replies, Bluesky, Mastodon, Reddit, Dev.to, Hashnode, Product Hunt, YouTube, shared Slack/Discord/Telegram destinations, GitHub releases/issues/PRs, Jira/Linear issues and Jira comments, and Notion page creation. External code/HTTP publishers use explicit registration. Discord needs channel metadata to distinguish shared destinations from private messages; a lookup failure warns and preserves the successfully sent message's outcome.
