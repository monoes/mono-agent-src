# Workflow definitions: one recoverable publication path (#977)

Date: 2026-10-24. Base: master `cbd1fe7df`. Issue: monoes/mono-agent-src#977 (refs #976, #951). Design only.

Facts: **[V]** verified by reading the cited code, **[U]** not verified.

## 0. The problem

A workflow definition lives in two places that readers treat differently. The JSON file in
`~/.monoagent/workflows/<id>.json` is what `HybridWorkflowStore.GetWorkflow`, `ListWorkflows`,
`ListAllProfilesWorkflows`, `NodeCounts` and `fileCopy` read first (hybrid_store.go:78-90, 95-123, 160-182) [V].
The SQLite rows (`workflows`, `workflow_nodes`, `workflow_connections`, `workflow_versions`,
`workflow_approvals`) are what executions, versions and approvals use. A save has to change both, and
the two stores cannot share a transaction. Today each writer picks its own order and its own
compensation, and only the guarded path (`SaveNodesGuarded`) coordinates anything across processes,
and even that only until the commit. The goal: every writer publishes through one protocol whose
intermediate states are visible to other processes and are resolved the same way by whoever sees them first.

## 1. Inventory

### 1.1 Writers of a definition

"First" = which store is written first. "Lock" = what excludes concurrent writers. `fileWriteMu` is the
package-level in-process mutex (guarded_save.go:57) [V]. "SQL lock" = the SQLite write lock held by an open
write transaction (busy_timeout 5 s, WAL, pool of connections per process: storage/database.go:84-86) [V].

| # | Writer | Entry (file:line) | First | Lock | Version | Approvals | Notes |
|---|--------|-------------------|-------|------|---------|-----------|-------|
| W1 | `workflow apply-fix` / AI-task apply | workflowfix.go:313 -> `SaveNodesGuarded` guarded_save.go:104 | DB tx, then file, then commit | SQL lock for the whole call + `fileWriteMu` from publish to commit | recorded in the tx (`recordVersionTx`) | revoked in the tx (`RevokeApprovals:true`) | Compare-and-swap on `ReviewHash`. File is renamed BEFORE commit: window visible to other processes [V] |
| W2 | `workflow save-draft`, `workflow edit save`, MCP `SaveWorkflow`, AI `generate/save.go`, `WithTxHook` users | workflowaitask.go:367, workflowedit/save.go:164, mcp/authoring.go:431, ai/generate/save.go:63 -> `saveDefinition` versions_save.go:98 | DB tx opened (first statement is a write), file written in `beforeCommit`, then commit | SQL lock; `files.SaveWorkflow` takes `fileWriteMu` only around the file write and releases it BEFORE commit (file_store.go:100-104, versions_save.go:136) | recorded in the tx | none in the tx. save-draft revokes afterwards, outside the tx (workflowaitask.go:335, error ignored) | No expected-hash check. A failed commit restores the file (versions_save.go:144) [V] |
| W3 | `CreateWorkflow` | hybrid_store.go:72 -> `saveDefinition` | as W2 | as W2 | v1 | none | used by import, templates, duplicate, `workflow create` via `createOrOverwriteWorkflowAtomically` (cmd/monoagentcli/workflow.go:56-107) [V] |
| W4 | `createOrOverwriteWorkflowAtomically` (import, templates use, duplicate, record) | workflow.go:81-96; callers workflowio/import.go:347, workflowmanage/manage.go:439, workflowtemplates/templates.go:344, record.go:105 | W3, then `SaveWorkflowNodes` and `SaveWorkflowConnections` again (redundant, each rewrites the file via W5) | none beyond W2/W5 | extra best-effort `captureVersion` per call | none | Compensation is logical (re-save of a snapshot, workflow.go:129-153), not byte-exact, and is itself several writes [V] |
| W5 | `SaveWorkflowNodes` / `SaveWorkflowConnections` (`workflow node set/add/remove`, connections, MCP authoring, engine `CreateWorkflow`, `saveDefinition` fallback in engine_versions.go:60-75) | hybrid_store.go:380-398; callers workflowedit/node.go:89,217,295-298,367,426; mcp/authoring.go:184,187,334; engine_workflows.go:19-28 | DB commit FIRST, then `syncFileNodes` (hybrid_store.go:359-378) | `fileWriteMu` around the file step only; none across the DB write and the file step | best effort `captureVersion` afterwards; error ignored | none in the store. `node set` approves the result afterwards via `operator.ApproveWorkflow` (node.go:92) | `syncFileNodes` swallows every error (`_ =`, :377) and returns nothing: a failed or skipped file write is silent. Reads the file, replaces nodes/connections with the DB's, rewrites it: a read-modify-write across processes with no cross-process lock [V] |
| W6 | `UpdateWorkflow` (metadata) | hybrid_store.go:184 -> `persist` :51 | FILE first, then `mirrorToSQL` (3 separate SQL transactions: row, nodes, connections) | `fileWriteMu` for the file write | none | none | Reverse order of W1/W2. A crash after the file write leaves file ahead of the DB [V] |
| W7 | `SetWorkflowActive` (`workflow activate/deactivate`, GUI toggle, AI chat tool, engine activate/deactivate with its revert at engine_workflows.go:217) | hybrid_store.go:303-312 | FILE first (whole-file read-modify-write with the copy read OUTSIDE the lock), then `mirrorToSQL` | `fileWriteMu` for the write only | none | none (approvals are hash-keyed and `is_active` is not in the definition hash, see 1.3) | Two processes toggling and editing can lose one update [V] |
| W8 | `DeleteWorkflow` (`workflow delete`, engine, MCP, rollbacks in W4) | hybrid_store.go:288-299; engine_workflows.go:161-185 | file removed first, then SQL (`ON DELETE CASCADE` takes approvals, versions) | none | n/a | cascade | A crash between leaves a row without a file; reads fall back to the row, so the workflow reappears [V] |
| W9 | Restore a version (`workflow versions restore`) | workflowmanage/versions.go:130 -> `SaveWorkflowWithNote` | as W2 | as W2 | recorded with note | none | [V] call site, W2 semantics |
| W10 | File to DB import | `SyncFileWorkflowsToSQL` workflowio/filesync.go:30; its only call site is `openWorkflowStore` cmd/monoagentcli/workflow.go:33 | DB (reads file, writes rows with `SaveWorkflowNodes/Connections`) | none; stamp marker `workflows-sqlite-sync.json` written with a plain temp+rename, read-modify-write across processes (filesync.go:92-98) | none (captured lazily) | `ApproveBackfilledFile` only for pre-approvals files (:84) | Skips files of unknown profiles and id-sharing workflows [V]. Runs in whichever process opens the store first: any CLI call, or a GUI-spawned CLI call [V]; whether the daemon's engine store runs it at start [U] (enginebuild not opened) |
| W11 | Profile stamping | `StampFileWorkflowProfiles` workflow/file_stamp.go:25, called from filesync.go:120 and chat_run.go:527, summary.go:97, httpapi/runtime.go:220, mcp/engine.go:265, mcp/grant_runtime.go:22 | FILE only (rewrites `profile_id`), atomic | none | none | none | Changes file bytes without a DB write; does not change the definition hash [V] |
| W12 | `workflow migrate` (DB -> files) | workflowio/migrate.go:76 | file only | `fileWriteMu` per file | none | none | Operator tool, rare [V] |
| W13 | Hand edit of the JSON file (editor, script, agent with file access) | documented: ref/workflow.go:33 and ref/examples.go:134 tell users to write `<id>.json` into the folder; `BackfillApprovals` mentions hand-edited files (approvals_backfill.go) | file only | none | captured lazily by `captureVersion` at next execution (versions_save.go:163) | none; the new hash is simply not approved | **Supported by documentation** [V] |
| W14 | GUI app (`wails-app`) | shells out to the CLI for delete/activate (app_workflows.go:254-285) [V]; `SaveWorkflow` request path [U] | n/a | n/a | n/a | n/a | Per the project rule the GUI renders and calls the CLI, so it is a source of concurrent CLI processes, not of new write paths [U for SaveWorkflow] |
| W15 | AI delete snapshots | `<id>.ai-backup.json` in the SAME folder (ai/chat/tools_edit.go:20-75) | file, plain `os.WriteFile` | none | none | none | Ends in `.json` and shares the folder; readers must tolerate it (it has no workflow `id`). A quarantine area must not sit inside this folder [V] |

### 1.2 Readers of the canonical file

| # | Reader | Where | Notes |
|---|--------|-------|-------|
| R1 | Daemon trigger reconcile, every 5 s | engine_reconcile.go:21-135 -> `ListAllProfilesWorkflows` + `GetWorkflow`; polling, no fsnotify anywhere in the module (orgdesign/watch.go:13) [V] | Sees a renamed-but-uncommitted file within 5 s. A key built from `UpdatedAt`/`Version` plus a trigger fingerprint decides whether to re-arm triggers [V] |
| R2 | Every CLI command's engine / store | `GetWorkflow` file-first (hybrid_store.go:78) | A run started during a W1/W2 window runs the uncommitted definition and pins whatever `captureVersion` reads (versions_save.go:163-181: it reads the file BEFORE taking the SQL lock, so it can record a version for a definition that then rolls back) [V] |
| R3 | `SyncFileWorkflowsToSQL` | W10 | Imports by `modtime+size` stamp, not by content; will import a pending file if it runs inside the window and the stamp differs (this is the scenario in the issue) [V] |
| R4 | Approvals backfill | approvals_backfill.go:134 | One-time; reads files [V] |

### 1.3 Facts the design depends on

- `definitionHash` covers name, description, settings, nodes (with position, disabled, config), and
  connections; it does not cover `is_active`, `profile_id`, timestamps or `version` (versions.go:100-135,
  `snapshotOf`) [V]. `ReviewHash` = definition hash plus active flag (guarded_save.go:20) [V].
- Approvals are rows `(workflow_id, definition_hash)` (migration 106); a definition is approved if its
  hash has a row. Revoking deletes all rows of the workflow. Only W1 revokes inside its transaction [V].
- `SaveNodesGuarded` opens a deferred transaction but its first statement is a write, so it holds the
  SQL write lock from then on [V]. `storage/reconcile.go:93` already uses an explicit `BEGIN IMMEDIATE`
  on a dedicated connection [V], so that is an accepted idiom here.
- Pool is uncapped; `synchronous=NORMAL` in WAL (database.go:86) [V]: a commit survives a process kill, not
  necessarily power loss (3.7).
- Test seams today are in-process package variables (`guardedFileFault`, `guardedCommitFault`,
  `guardedRestoreFault`); helper-process tests exist (capturetask/lock_test.go) [V].

## 2. Failure modes today

"Guarded" = already covered by W1 (`SaveNodesGuarded`).

| # | Scenario | Result today | Covered by W1? |
|---|----------|--------------|----------------|
| F1 | Process killed after DB commit, before the file write (W5: DB first) | File keeps the old definition; reads are file-first, so the edit is invisible; the stamp marker still matches so the importer never re-reads it; the DB holds a definition nobody reads. Permanent until the next edit | N/A (W1 never commits first) |
| F2 | Process killed after file rename, before commit (W1, W2) | File has the new definition, DB the old; nothing restores the file (restore code runs in the dying process) | No. This is the issue's headline gap |
| F3 | File written, commit fails (W1, W2) | In-process restore of previous bytes; if restore fails, `ErrFileInconsistent` is reported | Yes within the process; readers in other processes may have used the file meanwhile |
| F4 | Two processes save the same workflow | Both take the SQL lock in turn; W1 compares `ReviewHash` so the second gets `ErrChangedSincePreview`. W2 has no compare: last commit wins on the DB, last rename wins on the file, and the two orders can differ (file from A, rows from B). W5/W7 lose updates through unlocked file read-modify-write | Only between W1 callers; `fileWriteMu` does not exist across processes |
| F5 | Hand edit while the daemon runs | Daemon uses the new file within 5 s. DB rows update only when some CLI opens the store (W10). Approval of the new hash is absent, so it runs as unapproved (intended). A hand edit racing a W1 save: the rename of one overwrites the other and the DB records only W1 | No |
| F6 | Importer reads a half-written file | Our own writers use temp+fsync+rename (file_store.go:189-219), so a torn read does not come from them. A non-atomic editor can truncate in place: `parseWorkflowFile` fails and the file is skipped until the next call [V], or parses as a smaller valid workflow and is imported [U] | n/a |
| F7 | Pending W1 file imported by W10 and then the commit fails | DB holds proposed content, file restored: the issue's third bullet | No |
| F8 | Crash between file removal and row delete (W8) or between file write and mirror (W6, W7) | Resurrected workflow (W8); file ahead of rows (W6/W7), healed on the next store open because the file stamp changed | No |
| F9 | Power loss after a fsynced file rename, with the DB commit still unsynced (`synchronous=NORMAL`) | Same shape as F2 | No |

## 3. Design

### 3.1 Principles

1. The database is the commit point; the file is a projection that must equal the committed rows.
2. An intent is durable and cross-process-visible BEFORE the file changes. An open transaction is not
   visible to other processes, so the intent is its own short transaction.
3. Recovery is a pure function of (intent row, file bytes, rows hash); anyone may run it.
4. Roll back, not forward, when the commit did not happen: the dead caller never reported success.
5. No new daemon or lock service: SQLite's write lock is the cross-process mutex.

### 3.2 New table (migration 125, additive)

```
workflow_publications (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  workflow_id   TEXT NOT NULL,            -- no FK: a delete publication outlives the row
  kind          TEXT NOT NULL,            -- 'save' | 'delete' | 'import'
  state         TEXT NOT NULL,            -- 'pending' | 'unresolved'   (completed rows are deleted)
  base_hash     TEXT NOT NULL,            -- ReviewHash of the rows when the intent was recorded
  target_hash   TEXT NOT NULL,            -- definition hash the rows will have ('' for delete)
  target_sha    TEXT NOT NULL,            -- sha256 of the file bytes to publish ('' for delete)
  prev_sha      TEXT NOT NULL,            -- sha256 of the file before ('' if none)
  prev_bytes    BLOB,                     -- previous file bytes; NULL when the file did not exist
  prev_existed  INTEGER NOT NULL,
  next_version  INTEGER NOT NULL,
  origin        TEXT NOT NULL,            -- writer name, e.g. 'apply-fix', 'node-set', 'import'
  owner_pid     INTEGER NOT NULL,
  started_at    TIMESTAMP NOT NULL,
  note          TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX workflow_publications_one ON workflow_publications(workflow_id);

workflow_file_state (
  workflow_id    TEXT PRIMARY KEY,
  file_sha       TEXT NOT NULL,           -- bytes of the file as last published or imported
  definition_hash TEXT NOT NULL,
  updated_at     TIMESTAMP NOT NULL
);
```

The unique index is the per-workflow mutex across processes: a second writer's insert fails while a
publication is open. `prev_bytes` in the database keeps the backup under the same durability as the
intent (no pointer to manage). A workflow file is small (kilobytes; the largest realistic one is far
below a megabyte [U, not measured]); a size cap (proposed 8 MiB) turns an oversize file into a clear
refusal instead of a silent skip. Alternative considered: backup file beside the workflows folder with a
pointer in the row; rejected because it adds a second thing to recover and a path to sanitise. Completed
rows are deleted; the audit trail stays in `workflow_versions` and the existing apply audit table.

### 3.3 The protocol (one function: `HybridWorkflowStore.Publish`)

Input: workflow id, a function that maps the current committed definition to the new one (or the whole
new definition), an optional `ExpectHash`, `RevokeApprovals`, note, origin, and `kind`. The existing
methods (`SaveWorkflow`, `CreateWorkflow`, `UpdateWorkflow`, `SaveWorkflowNodes/Connections`,
`SetWorkflowActive`, `DeleteWorkflow`, `SaveWorkflowWithNote`, `SaveNodesGuarded`) become thin wrappers.

```
Step 0  recover(workflow_id)                 -- section 3.4; runs first, so a stale intent never blocks
Step 1  TX1  (dedicated conn, BEGIN IMMEDIATE, synchronous=FULL for this conn)
          - if an intent row exists and is live -> ErrPublicationBusy (bounded retry, then refuse)
          - read committed rows (or import the file first if the workflow has no rows, see 3.5)
          - if ExpectHash set and != ReviewHash(rows) -> ErrChangedSincePreview
          - compute target definition, next_version, rendered file bytes, hashes
          - read previous file bytes (+sha); INSERT intent (pending); COMMIT
Step 2  PUBLISH FILE  (fileWriteMu in-process, then writeFileAtomic: temp, fsync, rename, fsync dir)
Step 3  TX2  (BEGIN IMMEDIATE, synchronous=FULL)
          - verify the intent row is still mine and pending; if not -> the file was already restored
            by a recoverer: return ErrPublicationLost
          - re-verify rows still hash to base_hash (nothing else wrote the rows)
          - write rows (workflow, settings, nodes, connections); record version (this is the only
            place a version is recorded, so no version ever names an uncommitted definition)
          - revoke approvals when asked; run the WithTxHook function (apply-once claim)
          - upsert workflow_file_state; DELETE the intent row; COMMIT
Step 4  onSaved hook (grant tiers), as today
```

A failure at step 2 (file write error) -> TX3: restore from `prev_bytes` if needed, delete the intent;
report `ErrFileSyncFailed`. A failure at step 3 (commit error, hook refusal, `ErrPublicationLost`) -> same
restore, report. If the restore itself fails the intent row is set to `unresolved` and the caller reports
`file_inconsistent` exactly as `workflowfix` does today (workflowfix.go:318), now with a record that
`doctor` can find. The `WithTxHook` contract is unchanged: it runs inside TX2, so the AI-task apply-once
claim commits or rolls back with the rows.

A delete is the same protocol: TX1 records `kind=delete` and the previous bytes; step 2 removes the file;
TX2 deletes the rows (cascade takes approvals, versions) and the intent. Recovery of a pending delete
puts the file back (rows still exist). This removes F8's resurrection case.

Cost: two commits per save, both `synchronous=FULL`; saves are operator-rate events [U, measure in P7].

### 3.4 Recovery (`recover`), deterministic

Run under `BEGIN IMMEDIATE`, one intent at a time. Inputs: `F` = sha of the file now (or "absent"), `D` =
definition hash of the rows now.

| Condition | Action | Result |
|-----------|--------|--------|
| owner process alive and `started_at` younger than the lease (30 s, proposed) | do nothing | caller sees `busy` |
| `D == target_hash` (TX2 effectively happened, intent row left behind) | make the file equal the target (rewrite from the rows if `F != target_sha`), delete intent | roll forward; defensive, TX2 deletes the row itself so this should not occur |
| `F == prev_sha` (rename never happened) | delete intent | nothing to restore |
| `F == target_sha` (renamed, TX2 never committed) | write `prev_bytes` atomically (or remove the file if `prev_existed=0`), delete intent | roll back; file matches the committed rows again |
| otherwise (file is neither: an external edit landed in the window) | do not touch the file; copy it to the quarantine folder; set `state=unresolved` | reported by `doctor`; readers serve the committed rows meanwhile |

Owner liveness: `kill(pid, 0)` on Unix; lease only on Windows [U]. A reused pid only delays recovery.

Where it runs: (a) step 0 of every `Publish`; (b) in `openWorkflowStore`, as one indexed `SELECT` of
`workflow_publications` (a no-op on the normal path), before `SyncFileWorkflowsToSQL`; (c) in each
daemon reconcile tick (engine_reconcile.go), the same query; (d) `monoagentcli doctor` (3.6).

Quarantine folder: `~/.monoagent/workflow-quarantine/` (a sibling of `workflows/`, like the sync marker
`workflows-sqlite-sync.json`, because every `*.json` inside `workflows/` is read as a workflow and
`<id>.ai-backup.json` already lives there).

### 3.5 Readers and importers

Rule: a file is trusted for a workflow only if there is no open intent for it. One cheap query at the
top of `ListWorkflows`, `ListAllProfilesWorkflows`, `NodeCounts`, `GetWorkflow`, `fileCopy` collects
the ids with a `workflow_publications` row; for those ids the store serves the DB definition (for
`delete`, serves the file, since the rows still exist). The daemon's reconcile and the engine then
never see an uncommitted file, which closes F2/F7 for readers. `captureVersion` reads the definition
under the same rule and takes the write lock before reading, which closes the stale-capture case in R2.

Importer (`SyncFileWorkflowsToSQL`, also the entry for hand edits):
- replaces the `modtime+size` stamp by content: compare `sha256(file)` with `workflow_file_state.file_sha`;
- skips every id with an open intent (same retry-next-call treatment as the existing id-sharing skip);
- if the file's definition hash equals the rows' hash, only refreshes `file_sha` (this covers profile
  stamping W11 and re-serialisation by an editor: no import, no version, no approval change);
- otherwise runs the import as `kind=import` through `Publish` with the file bytes as the target, so it
  takes the same unique-intent lock and cannot interleave with a save;
- reads the file twice around a stat (size and mtime unchanged, mtime older than a settle time of 2 s,
  proposed) and requires it to parse and to pass the existing validation before importing; a file that
  does not pass stays as it is and is reported by `doctor` (not imported, not deleted, not quarantined
  unless it also blocks a pending intent).

### 3.6 External edits: decision for the code as it stands

Hand-editing the file is a documented workflow (W13), the importer exists for files written outside the
CLI (filesync.go:21-29 comment) [V], and the daemon already acts on edits within 5 s. Removing that would
be a user-visible regression. Proposed rule: **supported, imported as a publication, never silently
overwritten**.
- The daemon keeps reading a hand-edited file (as today) when no intent is open, so triggers re-arm on
  edits without waiting for a CLI call.
- The import records rows, a version (note `edited outside the app`), and **revokes approvals** for that
  workflow. Hash-keyed approvals already leave the new definition unapproved unless it equals an older
  approved one; revoking closes the "paste an old approved definition back" case for an editor that
  cannot be told apart from an agent with file access. This is a behaviour change (see section 5).
- W1/W2 refuse to overwrite a file whose sha differs from both `file_sha` and the committed rows'
  rendering; they import it first (3.3 step 0 does this) so the user's edit is never discarded.

`doctor` (3.7) reports; `doctor --fix` resolves.

### 3.7 `doctor`, and durability

New check `workflow.publications` in internal/health (pattern: db_schema.go) [V pattern]:
`ok` when `workflow_publications` is empty and every file matches `workflow_file_state` or is a pending
import; `warn` for an open intent older than the lease, a file that does not parse, or an `unresolved`
row. `--fix` runs `recover` and the import for files that are valid; it never deletes a quarantined copy.
`doctor` stays read-only without `--fix`.

Power loss (F9): TX1 and TX2 use `synchronous=FULL` on their own connection (set after `db.Conn`, reset
before release), so the intent is on disk before the file is renamed. Without it, a rename that survives
while the intent is lost looks like an external edit and would be imported. Recorded as an open item in
section 5, since it changes commit latency.

### 3.8 Versions and approvals

- Versions: recorded only in TX2. `captureVersion` stays as the lazy net, under the write lock, on a trusted file only.
- Approvals: `RevokeApprovals` stays a per-publication flag, set by AI-authored writers (apply-fix,
  save-draft, MCP authoring, generate) and by every `kind=import`. Operator writers (`node set` then
  `ApproveWorkflow`, GUI saves) keep today's semantics, but the approval moves into TX2 as an option
  (`ApproveResult`) so there is no window between the save and the approval (node.go:89-92 today).
  `is_active` flips (W7) neither revoke nor approve, as today (not in the hash).
- `next_version` is reserved in TX1 and checked in TX2: file number == version row.

### 3.9 Cross-process coordination, summarised

- Mutex per workflow: the unique intent row. Mutex for the SQL critical sections: `BEGIN IMMEDIATE`
  (precedent storage/reconcile.go:93). In-process: `fileWriteMu` stays, cheap, and no longer carries the
  cross-process burden.
- Waiting: `ErrPublicationBusy`, bounded retry (5 s proposed) then refuse; never past a dead owner's lease.
- Edge: workflows with ids shared with another workflow are not mirrored to rows (`ErrSharedIDs`,
  hybrid_store.go:199); they use `workflow_file_state` instead of the rows hash [U how many exist].

## 4. Phases (issue-sized, in order)

Each phase leaves every writer working. Owner roles: **store** (workflow package), **cli** (command
packages), **qa** (tests only), **ops** (doctor, docs).

| Phase | Issue | Owner | Content | Verification |
|-------|-------|-------|---------|--------------|
| P0 | Test rig first | qa | Failpoint mechanism (4.1) and the two-process helper (4.2), driven against the CURRENT code to reproduce F2, F4, F7 as failing/skipped tests tagged with the issue | The reproducers fail on master and are listed in the PR; no production change except the failpoint hook |
| P1 | Tables + `Publish` core | store | Migration 125, `Publish`, `recover`, reader gating (3.5), `SaveNodesGuarded` re-implemented on it with the same signature and error values. No other writer moves. | Existing guarded_save tests pass unchanged (they use the in-process seams, which are kept as thin aliases of the failpoints). New: crash-recovery across two processes (4.3 C1-C4). Race of two W1 writers across processes |
| P2 | W2/W3 and W9 | store, cli | `saveDefinition`, `CreateWorkflow`, `SaveWorkflow`, `SaveWorkflowWithNote` on `Publish`; add expect-hash where the caller has one; save-draft's revoke into TX2 (removes workflowaitask.go:335) | Per-writer test W2/W3/W9: save races `apply-fix` across two processes; file == rows afterwards; version count == commits; approvals as specified |
| P3 | W5 + W4 | store, cli | `SaveWorkflowNodes/Connections` become publications (file failure is now an error, not `_ =`); `createOrOverwriteWorkflowAtomically` becomes one whole-definition publication (drops the two redundant follow-up saves and the logical snapshot restore); `node set` approval inside TX2 | Per-writer tests for node set/add/remove, connections, import (overwrite and create), templates use, duplicate, record. Kill-between test per writer via failpoints |
| P4 | W6, W7, W8 | store | `UpdateWorkflow`, `SetWorkflowActive` (read-modify-write moves inside TX1 so the toggle cannot lose a node edit), `DeleteWorkflow` as `kind=delete` | Toggle vs node-set race across processes; delete killed between file and rows leaves the workflow present, and a second process finishes or reverts it |
| P5 | Importer + stamping | store, cli | `SyncFileWorkflowsToSQL` on content sha, `kind=import`, settle check, skip pending, hash-equal no-op; W11 skips pending ids; stamp marker replaced by `workflow_file_state` (old marker file ignored, then removed by a later release) | Hand-edit tests: edit during daemon run imports once with a version and revoked approvals; truncated file not imported; profile stamping causes no import; importer during a pending save does not import |
| P6 | `doctor` | ops | `workflow.publications` check + `--fix`; ref docs updated (hand editing: allowed, what happens) | `doctor` json shows ok on a clean tree, warn on seeded unresolved rows; `--fix` resolves; read-only without `--fix` |
| P7 | Hardening | store | `synchronous=FULL` on publication connections; measure; remove the old package-level seams and `syncFileNodes` | Benchmark of save latency recorded in the PR; full `go test ./...`, `go vet`, pinned gofmt, `cd wails-app && go mod tidy -diff` |

Rollout safety: P1 adds tables nothing else reads; P2-P4 move writers one family at a time and can ship
alone; mixed versions are the one risk (section 5). The file format does not change.

### 4.1 Deterministic crash injection

Test builds only (build tag `wfcrash`, so release binaries contain no failpoint code and no env lookup):
`failpoint(name string)` in `Publish` is a no-op without the tag; with it, the function reads
`MONOAGENT_WF_FAILPOINT=<name>[:<count>]` and, when the named point is reached, calls
`syscall.Kill(syscall.Getpid(), syscall.SIGKILL)` (real process death: no deferred cleanup runs, which an
`os.Exit` in a deferred-aware test would not guarantee). Points: `after-intent`, `before-rename`,
`after-rename`, `before-tx2`, `inside-tx2-before-commit`, `after-commit`, `during-restore`. Variants
that return an error instead of killing (`MONOAGENT_WF_FAILPOINT_ERR`) replace the three package
variables. `MONOAGENT_WF_FAILPOINT_PAUSE=<name>:<fifo path>` blocks at the point until the test writes to the
FIFO, so "reader runs inside the window" tests are exact, not timing-based.

### 4.2 Two-process harness

Same pattern as capturetask/lock_test.go: the test binary re-executes itself (`os.Args[0]
-test.run=TestHelperPublish`) with an env var carrying root, workflow id and action; the helper opens the
store on a scratch HOME (per the project rule: scratch HOME, never a copy of the real database; no
network, `MONOES_BASE_URL` pointed at a dead local port) and does one publication.
Helper actions: `save`, `apply-fix`, `node-set`, `toggle`, `delete`, `import`, `list`, `recover`.

### 4.3 What is tested and where

All of it runs on this Linux machine (SQLite is in-process pure Go; signals and `kill -0` are POSIX; no
Chrome or GUI). Not testable here: Windows liveness (lease-only path), real power loss (reasoning review
of `synchronous=FULL`), macOS fsync semantics (Mac mini host for a later smoke run).

| Test | Setup | Assert |
|------|-------|--------|
| C1 kill after rename, before TX2 | helper A `apply-fix` with `after-rename` kill; process B `recover` | file bytes == previous bytes; rows == previous; no intent row; no new version; approvals untouched |
| C2 kill after intent, before rename | A killed at `after-intent`; B lists | B serves rows; recovery deletes intent; file unchanged |
| C3 kill after commit | A killed at `after-commit` | no intent; file == target; rows == target; one version |
| C4 external edit in the window | A paused at `after-rename`; test overwrites the file; A killed; B recovers | file untouched, copy in quarantine, row `unresolved`, `doctor` warns, readers serve rows |
| C5 reader inside the window | A paused at `after-rename`; B runs list, get, importer, reconcile tick | none returns the target; importer imports nothing; after release, all see the target |
| C6 importer vs failing commit | A paused after rename, then forced to fail TX2 (error failpoint) | DB never holds the proposed content (the issue's third bullet) |
| R1..Rn race | one test per writer (W1-W9): helper `apply-fix` vs helper running that writer, 50 rounds with a start barrier (file lock or FIFO) | after each round: file definition hash == rows hash; versions strictly increasing; exactly the expected number of winners; no `.tmp` leftovers; no intent rows |
| E1 hand edit | edit while a long-lived engine runs | imported once, version noted, approvals revoked, triggers re-armed |

The acceptance bar from the issue ("a test per writer races it against `apply-fix` across two
processes") is the R-series; the crash tests C1-C6 are the crash-recovery bar.

## 5. Risks and open decisions for the owner

1. **Is hand-editing the canonical file supported?** The code and the docs say yes (W13). The design
   keeps it and treats an edit as an import. If the answer is "no, edit through the CLI", the importer
   and the 5 s reads could be reduced to detect-and-report, which is much simpler. Decide before P5.
2. **Revoke approvals on every external change?** Recommended (section 3.6) because a file edit cannot
   be attributed. Visible change: a hand edit that reverts to an older approved definition would now need
   approving again.
3. **Should `doctor --fix` repair?** Proposed yes for rollback/roll-forward by the table in 3.4 and for
   importing valid files; never for `unresolved` rows (the owner picks the file or the rows). Open: should
   plain `doctor` (no `--fix`) auto-run the recovery that stores run anyway? Proposed no.
4. **Durability cost**: `synchronous=FULL` on two commits per save. Expected small at operator rates [U];
   measure in P7. The alternative is accepting F9 for power loss only.
5. **Mixed versions**: during upgrade, an older CLI (or a GUI-bundled one, see the memory note on the
   three binaries) writes with the old order and never creates intents. Effect: its writes look like
   external edits and are imported (rows follow the file), which is acceptable, and its mid-write
   crashes are not recovered. Mitigation: ship P1-P4 in one release train, and let `doctor` flag a DB
   written by an older binary using the existing schema-ahead check (internal/health/db_schema.go).
6. **Behaviour visible to users**: (a) a failed file step in `node set` and friends is now an error
   (today silent); (b) `import`/`templates use` no longer do the three-step write and can no longer
   leave a partial state; (c) a concurrent save now returns `busy` after 5 s instead of racing;
   (d) hand edits are versioned and un-approve; (e) an open intent means served from rows. File format unchanged.
8. **Unverified items to confirm before P1**: whether the daemon's engine store runs
   `SyncFileWorkflowsToSQL` at start (enginebuild); how the GUI `SaveWorkflow` request reaches the store
   (CLI shell-out per project rule, not opened); how many workflows have shared ids (the
   `ErrSharedIDs` special case); Windows process liveness.

## 6. As built, and the choices made on the open questions

Branch `ai4/design977`. Differences from sections 3 and 4:

- **Migration number.** The tables are in migration 128 (125 to 127 were taken on master by other changes).
- **`Publish`** is `HybridWorkflowStore.Publish` (publish.go); recovery is publish_recover.go; the
  outside-edit importer is `ImportFile` (publish_import.go). The failpoints are in failpoint_on.go
  (build tag `wfcrash`); release builds compile the no-op in failpoint_off.go.
- **Which transaction takes the lock.** Every transaction of the protocol starts with a write
  statement on `workflow_publications`, which takes SQLite's write lock for the rest of that
  transaction; `BEGIN IMMEDIATE` on a dedicated connection was not needed.
- **Version in the file.** The next version number is worked out in TX1 (same rule as
  `recordVersionTx`) so the file carries it; TX2 refuses to commit if the rule gave a different
  number.
- **Base of an edit.** Node and connection edits, `apply-fix` and the active toggle start from the
  committed rows plus the name, description, settings and active flag of the file (the toggle from the
  whole file), so a hand edit of those survives, as it did when the file was read-modify-written.
  Removing a node takes its connections out of the file as well as the rows.
- **Unresolved owner.** When a rollback in the owner's own process cannot put the file back, the intent
  keeps `owner_pid = 0` so the next process retries it at once. A file that is neither the old nor the
  new bytes is copied to `workflow-quarantine/` and the intent becomes `unresolved`; the owner gets
  `ErrFileInconsistent`.
- **Behaviour changes that are visible.** A node or connection edit whose file cannot be written is
  now an error and changes nothing (it was dropped silently). `DeleteWorkflow` is all or nothing (a file
  that cannot be removed keeps the rows). A second change to a workflow with an open change waits up to
  5 s and then returns `ErrPublicationBusy`. After a failed rollback the next save of that workflow
  reports `ErrFileInconsistent` until the change is resolved (`doctor --fix` or any command that opens
  the store). `import`, `templates use`, duplicate and `record` are one publication; their old
  compensation code is gone.

Decisions on section 5:

1. **Hand edits stay supported.** An edit that changes the definition is imported by content: the rows
   follow the file, a version with the note `edited outside the app` is recorded, and the file is never
   rewritten. An edit that does not change the definition (re-serialisation, profile stamping) only
   updates the remembered bytes and the active flag. The daemon keeps reading the file as before.
2. **Approvals are revoked on a definition-changing outside import.** (Reversed after review: an
   earlier draft kept hash-keyed approvals, which let approve A, edit to B, restore A run A as approved
   with no operator action.) The import deletes the workflow's approvals in the same transaction. This is
   a visible change and is in the CHANGELOG and `ref workflow`. `apply-fix` and save-draft also revoke,
   inside the commit.
3. **`doctor`** shows `workflows.publications` and offers a fix that runs the recovery every command
   that opens the store runs anyway; plain `doctor` changes nothing. `unresolved` rows are only reported.
4. **Durability.** See the note in section 7 on `synchronous=FULL`.

## 7. Durability measurement and what remains

The three transactions of a publication run on a connection set to `synchronous=FULL`
(`beginDurable`), restored afterwards. `BenchmarkPublish` (one node edit: intent, file, commit) on the
shared, loaded development machine was too noisy to separate the two settings: FULL gave 16 to 44 ms per
save and NORMAL 4 to 49 ms across three runs of 300. The file's own fsyncs (temp file and folder)
dominate either way, and saves are operator-rate events. Real power loss and macOS fsync semantics were
not exercised.

Not done: skipping pending ids in profile stamping (a stamp that lands in the window makes a crash
there `unresolved` instead of rolled back, which is safe but louder); the three package-level test
seams in guarded_save.go are kept (non-tagged tests use them) next to the tagged failpoints; Windows
liveness is lease-only (30 s) as designed.

## 8. After review (Codex, 9 findings)

Recovery always rolls back a surviving intent (the commit removes its intent in the same transaction as
the rows, so hash equality proves nothing). The file step is fenced: the ownership check and the rename
happen in one transaction holding the write lock, and the new bytes are staged before the lock is taken;
a writer that still finds its intent gone repairs the file from the committed rows. File reads repeat
until no change started during them (`publication_gen` from `sqlite_sequence`). Node and connection id
ownership is rechecked inside the commit. A guarded save with an empty hash is refused. The importer
follows the active flag of a same-definition file, and `ResolveUnresolved` (doctor fix
`workflows.publications.resolve:<id>:keep-rows|keep-file`) ends an unresolved change. Intents carry the
profile of the workflow they create.
