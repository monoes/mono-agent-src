-- 129_coder_sessions.sql
-- Persistent coder sessions: the state of a coder conversation as the session
-- host (`coder host`) sees it, and the commands waiting for the host. Additive
-- only: no existing table is rebuilt.
CREATE TABLE IF NOT EXISTS coder_sessions (
    profile_id             TEXT NOT NULL,
    conversation_id        TEXT NOT NULL,
    state                  TEXT NOT NULL DEFAULT 'idle'
                           CHECK (state IN ('idle', 'running', 'waiting_user', 'paused', 'stopped')),
    role                   TEXT NOT NULL DEFAULT 'solo' CHECK (role IN ('solo', 'master', 'child')),
    parent_conversation_id TEXT NOT NULL DEFAULT '',
    unattended             INTEGER NOT NULL DEFAULT 1,
    turn_id                TEXT NOT NULL DEFAULT '',
    turn_pid               INTEGER NOT NULL DEFAULT 0,
    turn_stamp             TEXT NOT NULL DEFAULT '',
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL,
    PRIMARY KEY (profile_id, conversation_id)
);

CREATE TABLE IF NOT EXISTS coder_session_commands (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id      TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('send', 'stop', 'pause', 'resume')),
    turn_id         TEXT NOT NULL DEFAULT '',
    message         TEXT NOT NULL DEFAULT '',
    attachments     TEXT NOT NULL DEFAULT '[]',
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'failed')),
    error           TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_coder_session_commands_pending ON coder_session_commands(status, id);
