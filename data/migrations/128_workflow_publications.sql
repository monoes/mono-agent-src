-- One open row per workflow while its definition is being published: the file and the rows
-- cannot share a transaction, so the intent is written first, in a transaction of its own, and
-- whoever sees an abandoned row puts the file back to match the committed rows.
CREATE TABLE IF NOT EXISTS workflow_publications (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    workflow_id   TEXT NOT NULL,
    profile_id    TEXT NOT NULL DEFAULT '',
    kind          TEXT NOT NULL,
    state         TEXT NOT NULL DEFAULT 'pending',
    base_hash     TEXT NOT NULL DEFAULT '',
    target_hash   TEXT NOT NULL DEFAULT '',
    target_sha    TEXT NOT NULL DEFAULT '',
    prev_sha      TEXT NOT NULL DEFAULT '',
    prev_bytes    BLOB,
    prev_existed  INTEGER NOT NULL DEFAULT 0,
    next_version  INTEGER NOT NULL DEFAULT 0,
    origin        TEXT NOT NULL DEFAULT '',
    owner_pid     INTEGER NOT NULL DEFAULT 0,
    started_at    TIMESTAMP NOT NULL,
    note          TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS workflow_publications_one ON workflow_publications(workflow_id);

-- The file bytes last published or imported for a workflow, and the definition they held.
CREATE TABLE IF NOT EXISTS workflow_file_state (
    workflow_id     TEXT PRIMARY KEY,
    file_sha        TEXT NOT NULL,
    definition_hash TEXT NOT NULL,
    updated_at      TIMESTAMP NOT NULL
);
