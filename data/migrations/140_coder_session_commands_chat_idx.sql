-- the host pass looks up the newest stop commands of one conversation (internal/coderhost/missionchild.go); the
-- existing index serves the queue, not that lookup
CREATE INDEX IF NOT EXISTS idx_coder_session_commands_chat ON coder_session_commands(profile_id, conversation_id, kind);
