-- data/migrations/123_ai_task_applies.sql
-- One row per AI task proposal the operator applied (`workflow apply-fix`) or saved
-- (`workflow save-draft`): the audit line (who, which task, which workflow, how many
-- edits; never a value) and the guard that a proposal is applied once. task_id is the
-- primary key, so a second apply of the same task conflicts and is refused.
CREATE TABLE IF NOT EXISTS ai_task_applies (
    task_id     TEXT PRIMARY KEY,
    profile_id  TEXT NOT NULL,
    kind        TEXT NOT NULL,
    workflow_id TEXT NOT NULL DEFAULT '',
    edit_count  INTEGER NOT NULL DEFAULT 0,
    actor       TEXT NOT NULL,
    applied_at  TEXT NOT NULL
);
