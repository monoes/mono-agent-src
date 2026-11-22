-- 013_oauth_credentials_per_profile.sql
-- platform_oauth_credentials was a single global row per platform, so a
-- second OAuth connection for the same platform under a different profile
-- (e.g. two separate Outlook accounts, each needing its own Azure app
-- registration) silently overwrote the first one's Client ID, breaking
-- silent token refresh for it. Scope it by (platform, profile_id) instead.
--
-- This table was previously created ad hoc (CREATE TABLE IF NOT EXISTS) by
-- application code rather than a migration, so it may or may not exist yet.

CREATE TABLE IF NOT EXISTS platform_oauth_credentials_v2 (
    platform      TEXT NOT NULL,
    profile_id    TEXT NOT NULL DEFAULT 'default',
    client_id     TEXT NOT NULL,
    client_secret TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL,
    PRIMARY KEY (platform, profile_id)
);

-- On a fresh database, platform_oauth_credentials never existed (it was only
-- ever created ad hoc by old application code), but SQLite must still resolve
-- the table reference below at prepare time even though WHERE EXISTS(...)
-- would filter all rows at runtime — so ensure it exists as an empty stub
-- first. The DROP TABLE below removes it again once migrated.
CREATE TABLE IF NOT EXISTS platform_oauth_credentials (
    platform      TEXT NOT NULL,
    client_id     TEXT NOT NULL,
    client_secret TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL
);

INSERT OR IGNORE INTO platform_oauth_credentials_v2 (platform, profile_id, client_id, client_secret, updated_at)
SELECT platform, 'default', client_id, client_secret, updated_at
FROM platform_oauth_credentials
WHERE EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'platform_oauth_credentials');

-- The copied rows belong to the profile 'default'. 011 no longer seeds it on
-- every database (only where user data already existed), and a database from
-- before profiles may hold OAuth app config and nothing else, so make sure the
-- owner exists. A new database copies nothing and gets no profile.
INSERT OR IGNORE INTO profiles (id, name)
SELECT 'default', 'Default'
WHERE EXISTS (SELECT 1 FROM platform_oauth_credentials_v2 WHERE profile_id = 'default');

DROP TABLE IF EXISTS platform_oauth_credentials;

ALTER TABLE platform_oauth_credentials_v2 RENAME TO platform_oauth_credentials;
