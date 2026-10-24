-- data/migrations/124_ai_task_apply_audit.sql
-- Events of the AI task apply commands that are not an apply: today, a workflow file that could
-- not be put back after a failed save (the file and the database may disagree). Task, profile,
-- workflow and event name only; never a value.
CREATE TABLE IF NOT EXISTS ai_task_apply_audit (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     TEXT NOT NULL,
    profile_id  TEXT NOT NULL,
    workflow_id TEXT NOT NULL DEFAULT '',
    event       TEXT NOT NULL,
    at          TEXT NOT NULL
);
