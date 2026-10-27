-- 137_coder_person_items.sql
-- A coder session's request that only the person answers, with no timeout. One row links the
-- request to what it is about: kind says which table ref_id points into (today a parked question
-- of a session with no master; later other kinds). The row has no status of its own: it is open
-- exactly while the row it points at is open, so there is one source of truth per kind.
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
