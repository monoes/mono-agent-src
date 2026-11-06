-- 163_coder_sessions_chat_mode.sql
-- The chat mode of a coder conversation: 'off', 'mission' or 'open' (open-ended). It replaces the Mission flag of the
-- settings document, is chosen before the first message and never changes afterwards. Additive: a column on the
-- existing table, no rebuild.
ALTER TABLE coder_sessions ADD COLUMN chat_mode TEXT NOT NULL DEFAULT 'off' CHECK (chat_mode IN ('off', 'mission', 'open'));

-- A conversation whose settings had the Mission flag on is a mission chat. Done before the trigger exists, so that
-- chats which already have messages are backfilled too.
UPDATE coder_sessions SET chat_mode = 'mission' WHERE json_valid(settings) AND json_extract(settings, '$.mission') = 1;

-- The lock: once the conversation has any message (a send waiting for the host, or a turn), a change of the mode is
-- refused. Writing the value it already has is no change.
CREATE TRIGGER IF NOT EXISTS coder_sessions_chat_mode_locked BEFORE UPDATE OF chat_mode ON coder_sessions
WHEN NEW.chat_mode IS NOT OLD.chat_mode AND (
  EXISTS (SELECT 1 FROM coder_session_commands c WHERE c.profile_id = OLD.profile_id AND c.conversation_id = OLD.conversation_id AND c.kind = 'send')
  OR EXISTS (SELECT 1 FROM ai_chat_turns t WHERE t.profile_id = OLD.profile_id AND t.conversation_id = OLD.conversation_id))
BEGIN
  SELECT RAISE(ABORT, 'chat_mode is locked: the conversation already has a message');
END;
