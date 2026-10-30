-- 142_task_session_tree.sql
-- Sessions form a tree.
-- parent_session_id is NULL for a top-level session; kind says what a session is for; approval_basis records who
-- approved it and why; the operator's approval sets it ('' for a draft and for rows approved before this migration).
--
-- Additive only, on task_sessions: the tasks table is not touched. Old rows become top-level 'board' sessions.
-- The depth cap and the other tree rules are the store's (internal/tasks), not the schema's.
ALTER TABLE task_sessions ADD COLUMN parent_session_id INTEGER REFERENCES task_sessions(id) ON DELETE SET NULL;
ALTER TABLE task_sessions ADD COLUMN kind TEXT NOT NULL DEFAULT 'board';
ALTER TABLE task_sessions ADD COLUMN approval_basis TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_task_sessions_parent ON task_sessions(parent_session_id) WHERE parent_session_id IS NOT NULL;
