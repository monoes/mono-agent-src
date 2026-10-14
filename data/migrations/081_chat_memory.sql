-- 081_chat_memory.sql
--
-- Conversation memory for AI steps: the messages of each chat session, kept
-- per profile. agent.ask reads a window of it before a turn and appends the
-- turn after; the agent.memory node reads, inserts and deletes messages.
-- seq orders a session's messages (1, 2, 3, … per profile and session).
CREATE TABLE IF NOT EXISTS chat_memory_messages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id  TEXT    NOT NULL DEFAULT 'default',
    session_key TEXT    NOT NULL,
    seq         INTEGER NOT NULL,
    role        TEXT    NOT NULL CHECK (role IN ('human', 'ai', 'system')),
    content     TEXT    NOT NULL,
    created_at  TEXT    NOT NULL,
    UNIQUE (profile_id, session_key, seq)
);
