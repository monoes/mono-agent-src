-- Open-ended ("doomsday") mode state. Additive only; nothing here starts a run, and with no rows the mode is off.
-- Insert-only tables are enforced by triggers, so a stray UPDATE or DELETE aborts instead of rewriting authority.

CREATE TABLE IF NOT EXISTS doomsday_charters (
    profile        TEXT    NOT NULL,
    generation     INTEGER NOT NULL,
    charter_id     TEXT    NOT NULL,
    canon_version  INTEGER NOT NULL,
    charter_hash   BLOB    NOT NULL,
    body           TEXT    NOT NULL,
    mac            BLOB    NOT NULL,
    created_at     INTEGER NOT NULL,
    PRIMARY KEY (profile, generation),
    UNIQUE (charter_id)
);

CREATE TABLE IF NOT EXISTS doomsday_runs (
    profile     TEXT    NOT NULL,
    generation  INTEGER NOT NULL,
    charter_id  TEXT    NOT NULL,
    state       TEXT    NOT NULL CHECK (state IN ('running','stop-requested','authority-revoked','cleanup-pending','quiescent')),
    intent_at   INTEGER NOT NULL DEFAULT 0,
    intent_by   TEXT    NOT NULL DEFAULT '',
    cause       TEXT    NOT NULL DEFAULT '',
    mac         BLOB    NOT NULL,
    PRIMARY KEY (profile, generation)
);
-- One active (not quiescent) run per profile.
CREATE UNIQUE INDEX IF NOT EXISTS idx_doomsday_runs_one_active ON doomsday_runs(profile) WHERE state <> 'quiescent';

CREATE TABLE IF NOT EXISTS doomsday_members (
    profile     TEXT    NOT NULL,
    generation  INTEGER NOT NULL,
    member_id   TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    pid         INTEGER NOT NULL DEFAULT 0,
    stamp       TEXT    NOT NULL DEFAULT '',
    live        INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (profile, generation, member_id)
);

CREATE TABLE IF NOT EXISTS doomsday_audit (
    profile     TEXT    NOT NULL,
    generation  INTEGER NOT NULL,
    seq         INTEGER NOT NULL,
    kind        TEXT    NOT NULL,
    payload     TEXT    NOT NULL,
    prev        BLOB    NOT NULL,
    mac         BLOB    NOT NULL,
    PRIMARY KEY (profile, generation, seq)
);

CREATE TABLE IF NOT EXISTS doomsday_counter_deltas (
    profile      TEXT    NOT NULL,
    generation   INTEGER NOT NULL,
    kind         TEXT    NOT NULL,
    approval_id  TEXT    NOT NULL,
    n            INTEGER NOT NULL CHECK (n > 0),
    PRIMARY KEY (profile, generation, kind, approval_id),
    UNIQUE (approval_id)
);

-- Insert-only.
CREATE TRIGGER IF NOT EXISTS doomsday_charters_no_update BEFORE UPDATE ON doomsday_charters BEGIN SELECT RAISE(ABORT, 'doomsday_charters is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_charters_no_delete BEFORE DELETE ON doomsday_charters BEGIN SELECT RAISE(ABORT, 'doomsday_charters is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_audit_no_update BEFORE UPDATE ON doomsday_audit BEGIN SELECT RAISE(ABORT, 'doomsday_audit is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_audit_no_delete BEFORE DELETE ON doomsday_audit BEGIN SELECT RAISE(ABORT, 'doomsday_audit is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_counter_no_update BEFORE UPDATE ON doomsday_counter_deltas BEGIN SELECT RAISE(ABORT, 'doomsday_counter_deltas is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_counter_no_delete BEFORE DELETE ON doomsday_counter_deltas BEGIN SELECT RAISE(ABORT, 'doomsday_counter_deltas is insert-only'); END;

-- Runs are never deleted and their state moves forward by exactly one step.
CREATE TRIGGER IF NOT EXISTS doomsday_runs_no_delete BEFORE DELETE ON doomsday_runs BEGIN SELECT RAISE(ABORT, 'doomsday_runs is never deleted'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_runs_forward_only BEFORE UPDATE ON doomsday_runs
WHEN NOT (
    NEW.profile = OLD.profile AND NEW.generation = OLD.generation AND NEW.charter_id = OLD.charter_id AND
    (CASE OLD.state WHEN 'running' THEN 'stop-requested' WHEN 'stop-requested' THEN 'authority-revoked'
                    WHEN 'authority-revoked' THEN 'cleanup-pending' WHEN 'cleanup-pending' THEN 'quiescent' END) = NEW.state
)
BEGIN SELECT RAISE(ABORT, 'doomsday run state moves forward one step only'); END;
-- A run cannot be quiescent while a member is live.
CREATE TRIGGER IF NOT EXISTS doomsday_runs_quiescent_needs_no_live BEFORE UPDATE ON doomsday_runs
WHEN NEW.state = 'quiescent' AND EXISTS (SELECT 1 FROM doomsday_members m WHERE m.profile = NEW.profile AND m.generation = NEW.generation AND m.live = 1)
BEGIN SELECT RAISE(ABORT, 'doomsday run has live members'); END;

-- Members are never deleted and, once released (live 0), never live again.
CREATE TRIGGER IF NOT EXISTS doomsday_members_no_delete BEFORE DELETE ON doomsday_members BEGIN SELECT RAISE(ABORT, 'doomsday_members is never deleted'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_members_release_only BEFORE UPDATE ON doomsday_members
WHEN NOT (OLD.live = 1 AND NEW.live = 0 AND NEW.profile = OLD.profile AND NEW.generation = OLD.generation AND NEW.member_id = OLD.member_id AND NEW.kind = OLD.kind AND NEW.pid = OLD.pid AND NEW.stamp = OLD.stamp)
BEGIN SELECT RAISE(ABORT, 'doomsday member can only be released'); END;

-- The charter and generation a session belongs to ('' / 0 for every session outside a run).
ALTER TABLE coder_sessions ADD COLUMN doomsday_charter_id TEXT NOT NULL DEFAULT '';
ALTER TABLE coder_sessions ADD COLUMN doomsday_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_sessions ADD COLUMN doomsday_charter_id TEXT NOT NULL DEFAULT '';
ALTER TABLE task_sessions ADD COLUMN doomsday_generation INTEGER NOT NULL DEFAULT 0;
