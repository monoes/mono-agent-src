-- 131_coder_session_checkin.sql
-- What the check-in scheduler remembers about a master session: when it last
-- looked, and a snapshot of its children as it saw them (to tell what changed).
-- Additive.
ALTER TABLE coder_sessions ADD COLUMN checkin_at TEXT NOT NULL DEFAULT '';
ALTER TABLE coder_sessions ADD COLUMN checkin_snapshot TEXT NOT NULL DEFAULT '';
