-- 130_coder_session_settings.sql
-- Per-session settings of a coder session (check-ins, autonomy, limits, mesh):
-- one JSON document, validated by the coder host's settings model. Additive.
ALTER TABLE coder_sessions ADD COLUMN settings TEXT NOT NULL DEFAULT '{}';
