-- 134_coder_session_decisions.sql
-- Autonomy for coder sessions: the ledger of decisions made for a session
-- without asking a person (so they can be reviewed and reverted), and the
-- questions parked while nobody was there to answer. Additive.
CREATE TABLE IF NOT EXISTS coder_session_decisions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id      TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    class           TEXT NOT NULL,
    question        TEXT NOT NULL DEFAULT '',
    choice          TEXT NOT NULL DEFAULT '',
    alternatives    TEXT NOT NULL DEFAULT '[]',
    reason          TEXT NOT NULL DEFAULT '',
    reversible      INTEGER NOT NULL DEFAULT 0,
    made_by         TEXT NOT NULL CHECK (made_by IN ('model', 'master')),
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'reverted', 'not_applied')),
    reverted_by     TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_coder_session_decisions_conv ON coder_session_decisions(profile_id, conversation_id, id);

CREATE TABLE IF NOT EXISTS coder_session_questions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id      TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    class           TEXT NOT NULL,
    question        TEXT NOT NULL DEFAULT '',
    options         TEXT NOT NULL DEFAULT '[]',
    default_answer  TEXT NOT NULL DEFAULT '',
    reversible      INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'answered', 'defaulted')),
    answer          TEXT NOT NULL DEFAULT '',
    answered_by     TEXT NOT NULL DEFAULT '',
    command_id      INTEGER NOT NULL DEFAULT 0,
    decision_id     INTEGER NOT NULL DEFAULT 0,
    reopen_count    INTEGER NOT NULL DEFAULT 0,
    last_failed_at  TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    answered_at     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_coder_session_questions_conv ON coder_session_questions(profile_id, conversation_id, status);
