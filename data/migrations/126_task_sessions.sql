-- Task sessions (docs/mastermind/plans/2026-10-25-coder-missions-task-sessions.md,
-- section 4). A session is one goal with its own tasks: its tasks carry its id,
-- and the personal board (session_id NULL) never lists them. Additive only: no
-- table is rebuilt, so the foreign-key hazard noted in 062_tasks.sql does not
-- apply. Deleting a profile deletes its sessions, and a session's deletion
-- deletes its tasks. tasks.status keeps its CHECK: "blocked" and "verified" are
-- the flags tasks.blocked and tasks.verified, not statuses. A draft session's
-- tasks sit in the inbox column and cannot be taken until the session runs.
CREATE TABLE IF NOT EXISTS task_sessions (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id          TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    conversation_id     TEXT,
    title               TEXT NOT NULL,
    goal                TEXT NOT NULL DEFAULT '',
    state               TEXT NOT NULL DEFAULT 'draft'
                        CHECK (state IN ('draft','running','paused','done','stopped','failed')),
    access_level        TEXT NOT NULL DEFAULT 'scoped',
    budget_usd          REAL NOT NULL DEFAULT 0,
    spent_usd           REAL NOT NULL DEFAULT 0,
    max_tasks           INTEGER NOT NULL DEFAULT 200,
    max_depth           INTEGER NOT NULL DEFAULT 3,
    max_retries         INTEGER NOT NULL DEFAULT 2,
    auto_close_verified INTEGER NOT NULL DEFAULT 0,
    created_by          TEXT NOT NULL,
    approved_by         TEXT NOT NULL DEFAULT '',
    approved_at         TEXT NOT NULL DEFAULT '',
    archived_at         TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    client_id           TEXT -- an idempotency key per profile, as tasks.client_id (062)
);
CREATE INDEX IF NOT EXISTS idx_task_sessions_profile ON task_sessions(profile_id, state);
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_sessions_client ON task_sessions(profile_id, client_id) WHERE client_id IS NOT NULL;

ALTER TABLE tasks ADD COLUMN session_id INTEGER REFERENCES task_sessions(id) ON DELETE CASCADE;
ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'task' CHECK (kind IN ('task','epic','check'));
ALTER TABLE tasks ADD COLUMN parent_id INTEGER;
ALTER TABLE tasks ADD COLUMN acceptance TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN assignee TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN suggested_executor TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0 CHECK (blocked IN (0,1));
ALTER TABLE tasks ADD COLUMN verified INTEGER NOT NULL DEFAULT 0 CHECK (verified IN (0,1));
ALTER TABLE tasks ADD COLUMN retries INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_tasks_session ON tasks(session_id, status, position) WHERE session_id IS NOT NULL;

-- The dependency DAG of a session: task_id waits for depends_on_task_id to be done.
CREATE TABLE IF NOT EXISTS task_deps (
    task_id            INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    depends_on_task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, depends_on_task_id),
    CHECK (task_id <> depends_on_task_id)
);
CREATE INDEX IF NOT EXISTS idx_task_deps_on ON task_deps(depends_on_task_id);

-- Evidence of a task's work: a commit, a file, a report, test output.
CREATE TABLE IF NOT EXISTS task_artifacts (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    kind    TEXT NOT NULL,
    ref     TEXT NOT NULL,
    note    TEXT NOT NULL DEFAULT '',
    at      TEXT NOT NULL,
    actor   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_task_artifacts_task ON task_artifacts(task_id, id);

-- The history of a session itself: its life cycle, its plan changes, its spend.
CREATE TABLE IF NOT EXISTS task_session_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES task_sessions(id) ON DELETE CASCADE,
    at         TEXT NOT NULL,
    actor      TEXT NOT NULL,
    kind       TEXT NOT NULL,
    note       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_task_session_events ON task_session_events(session_id, id);

-- One counter per session, bumped by every write to it, so a watcher of a
-- session detects a change with a primary key read (as task_board_rev does for
-- the personal board, which a session's writes do not move).
CREATE TABLE IF NOT EXISTS task_session_rev (
    session_id INTEGER NOT NULL PRIMARY KEY REFERENCES task_sessions(id) ON DELETE CASCADE,
    rev        INTEGER NOT NULL
);
