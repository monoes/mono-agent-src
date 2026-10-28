-- 146_coder_session_asks.sql
-- The upward ASK channel (docs/mastermind/plans/2026-10-28-mission-research-directions.md, sections 3, 5, 8):
-- a mission lead asks a proper ancestor session's lead for context. One row per ask; the question text is
-- cleaned data an agent wrote. status: open | answered | expired | void. A critical ask has a person item
-- (kind ancestor_question, ref_id = this id) and is answered by the person only. Additive.
CREATE TABLE IF NOT EXISTS coder_session_asks (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id       TEXT NOT NULL,
    asker_session    INTEGER NOT NULL,
    asker_conv       TEXT NOT NULL,
    ancestor_session INTEGER NOT NULL,
    ancestor_conv    TEXT NOT NULL DEFAULT '',
    level            TEXT NOT NULL,
    importance       TEXT NOT NULL CHECK (importance IN ('normal', 'critical')),
    question         TEXT NOT NULL,
    bytes            INTEGER NOT NULL,
    status           TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'answered', 'expired', 'void')),
    command_id       INTEGER NOT NULL DEFAULT 0,
    answer           TEXT NOT NULL DEFAULT '',
    answered_by      TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    answered_at      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_coder_asks_asker ON coder_session_asks(profile_id, asker_conv, status);
CREATE INDEX IF NOT EXISTS idx_coder_asks_ancestor ON coder_session_asks(profile_id, ancestor_conv, status);
