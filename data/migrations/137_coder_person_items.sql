-- 137_coder_person_items.sql
-- A coder session's request that only the person answers, with no timeout. One row links the
-- request to what it is about: kind says which table ref_id points into (today a parked question
-- of a session: a risky_action / missing_credential one; later other kinds). The row has no status of its own:
-- whether the person has to act is derived from the row it points at and the session as they are now.
CREATE TABLE IF NOT EXISTS coder_session_person_items (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id      TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    kind            TEXT NOT NULL,
    ref_id          INTEGER NOT NULL,
    created_at      TEXT NOT NULL,
    UNIQUE (profile_id, kind, ref_id)
);
CREATE INDEX IF NOT EXISTS idx_coder_person_items_conv ON coder_session_person_items(profile_id, conversation_id);
-- Questions parked before this table existed: every open risky_action / missing_credential one is an item.
INSERT OR IGNORE INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at)
    SELECT profile_id, conversation_id, 'parked_question', id, created_at FROM coder_session_questions
    WHERE class IN ('risky_action', 'missing_credential') AND status = 'open';
