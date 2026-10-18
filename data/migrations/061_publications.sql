CREATE TABLE IF NOT EXISTS publications (
    id TEXT NOT NULL PRIMARY KEY,
    profile_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT '',
    platform TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    remote_id TEXT NOT NULL DEFAULT '',
    parent_url TEXT NOT NULL DEFAULT '',
    account TEXT NOT NULL DEFAULT '',
    workflow_id TEXT NOT NULL DEFAULT '',
    execution_id TEXT NOT NULL DEFAULT '',
    node_id TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT '',
    org_id TEXT NOT NULL DEFAULT '',
    role_id TEXT NOT NULL DEFAULT '',
    published_at TEXT NOT NULL DEFAULT '',
    recorded_at TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    media TEXT NOT NULL DEFAULT '[]'
);
CREATE UNIQUE INDEX IF NOT EXISTS publications_idempotency ON publications(profile_id, idempotency_key) WHERE idempotency_key <> '';
CREATE UNIQUE INDEX IF NOT EXISTS publications_remote ON publications(profile_id, platform, account, kind, remote_id) WHERE remote_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS publications_url ON publications(profile_id, platform, account, kind, url) WHERE remote_id = '' AND url <> '';
CREATE INDEX IF NOT EXISTS publications_recent ON publications(profile_id, published_at DESC, id DESC);
