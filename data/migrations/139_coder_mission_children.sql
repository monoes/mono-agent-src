-- 139_coder_mission_children.sql
-- Mission tasks as child coder sessions (docs/mastermind/plans/2026-10-27-mission-child-sessions.md,
-- section 2.1): the policy of a task session (who may have child sessions and how many), one row
-- per task the lead marked to run in its own session, and the audit trail of changes to either.
-- Owned by internal/coderhost. Additive only: no existing table is changed, and no column is added
-- to tasks or task_sessions. Only task ids are stored, never task text.
CREATE TABLE IF NOT EXISTS coder_mission_policy (
    profile_id           TEXT NOT NULL,
    session_id           INTEGER NOT NULL,                 -- task_sessions.id
    lead_conversation_id TEXT NOT NULL,
    mode                 TEXT NOT NULL DEFAULT 'marked' CHECK (mode IN ('off', 'marked', 'all')),
    max_children         INTEGER NOT NULL DEFAULT 2 CHECK (max_children >= 0),
    share_pct            INTEGER NOT NULL DEFAULT 50 CHECK (share_pct >= 1 AND share_pct <= 80),
    default_budget_usd   REAL NOT NULL DEFAULT 0 CHECK (default_budget_usd >= 0),  -- 0 = derive from the share
    raised_by            TEXT NOT NULL DEFAULT '',
    -- the person's floors and caps, written only through the operator's path; the lead may lower its own
    -- numbers but may not leave an operator off or pass an operator cap (cleared by the operator)
    operator_mode_off      INTEGER NOT NULL DEFAULT 0 CHECK (operator_mode_off IN (0, 1)),
    operator_max_children  INTEGER CHECK (operator_max_children IS NULL OR operator_max_children >= 0),
    operator_share_pct     INTEGER CHECK (operator_share_pct IS NULL OR (operator_share_pct >= 1 AND operator_share_pct <= 80)),
    updated_at           TEXT NOT NULL,
    rev                  INTEGER NOT NULL DEFAULT 0,       -- bumped by every write; a policy write goes against the revision it read
    PRIMARY KEY (profile_id, session_id)
);

CREATE TABLE IF NOT EXISTS coder_mission_children (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id           TEXT NOT NULL,
    session_id           INTEGER NOT NULL,
    task_id              INTEGER NOT NULL,
    lead_conversation_id TEXT NOT NULL,
    child_conversation_id TEXT NOT NULL DEFAULT '',
    pending_conversation_id TEXT NOT NULL DEFAULT '',  -- created by a spawn that has not linked it yet
    reserved_usd         REAL NOT NULL DEFAULT 0 CHECK (reserved_usd >= 0),  -- the carve held while spawning
    origin               TEXT NOT NULL CHECK (origin IN ('lead', 'rule')),
    size                 TEXT NOT NULL DEFAULT '' CHECK (size IN ('', 'large', 'small')),
    budget_usd           REAL NOT NULL DEFAULT 0 CHECK (budget_usd >= 0),   -- the wish; the carve lives on coder_sessions
    state                TEXT NOT NULL CHECK (state IN ('marked', 'spawning', 'spawned', 'ended', 'failed', 'gone', 'released')),
    end_reason           TEXT NOT NULL DEFAULT '' CHECK (end_reason IN ('', 'done', 'no_finish', 'stopped', 'budget', 'expired', 'deleted', 'lead_stopped', 'cascade')),
    attempts             INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    spawned_at           TEXT NOT NULL DEFAULT '',
    ended_at             TEXT NOT NULL DEFAULT '',
    UNIQUE (profile_id, session_id, task_id)               -- idempotence: one row per task
);
CREATE INDEX IF NOT EXISTS idx_coder_mission_children_lead ON coder_mission_children(profile_id, lead_conversation_id);
-- a child conversation belongs to at most one task
CREATE UNIQUE INDEX IF NOT EXISTS idx_coder_mission_children_child ON coder_mission_children(profile_id, child_conversation_id) WHERE child_conversation_id <> '';
-- and a pending (created, not yet linked) conversation belongs to at most one task
CREATE UNIQUE INDEX IF NOT EXISTS idx_coder_mission_children_pending ON coder_mission_children(profile_id, pending_conversation_id) WHERE pending_conversation_id <> '';

CREATE TABLE IF NOT EXISTS coder_mission_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id TEXT NOT NULL,
    session_id INTEGER NOT NULL,
    task_id    INTEGER NOT NULL DEFAULT 0,
    kind       TEXT NOT NULL,                              -- mark | raise | lower | mode | floor | budget | spawn | end | cascade
    who        TEXT NOT NULL DEFAULT '',                   -- lead:<conversation> | operator | host
    note       TEXT NOT NULL DEFAULT '',
    at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_coder_mission_events_session ON coder_mission_events(profile_id, session_id, id);

-- the host's pass looks up the newest stop commands of one conversation (internal/coderhost/missionchild.go); the
-- existing index serves the queue, not that lookup
CREATE INDEX IF NOT EXISTS idx_coder_session_commands_chat ON coder_session_commands(profile_id, conversation_id, kind);
