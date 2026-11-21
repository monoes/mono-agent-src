-- 011_profiles.sql
-- Multi-profile support: every user-owned entity is scoped to a profile.
-- Running workflows from a non-active profile continue uninterrupted —
-- they are subprocesses; only the UI read-filter changes.

CREATE TABLE IF NOT EXISTS profiles (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- No profile is seeded: a new database starts with zero profiles and the
-- first-run onboarding has the user create and name the first one. This file
-- used to insert a 'default' profile and an active_profile_id setting;
-- databases that already have that row keep it, and 'default' stays a valid
-- profile id. The column DEFAULT 'default' clauses below are unchanged.

-- Add profile_id to every user-scoped table. modernc.org/sqlite does not
-- support "ADD COLUMN IF NOT EXISTS"; schema_migrations already guarantees
-- this file runs at most once, so a plain ADD COLUMN is safe here.
ALTER TABLE people              ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE crawler_sessions    ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE actions             ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE social_lists        ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE threads             ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE workflows           ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE workflow_executions ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE credentials         ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vault_images        ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE hil_pending         ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE tags                ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default';

-- Performance indexes for profile-scoped queries.
CREATE INDEX IF NOT EXISTS idx_people_profile              ON people(profile_id);
CREATE INDEX IF NOT EXISTS idx_sessions_profile            ON crawler_sessions(profile_id);
CREATE INDEX IF NOT EXISTS idx_actions_profile             ON actions(profile_id);
CREATE INDEX IF NOT EXISTS idx_social_lists_profile        ON social_lists(profile_id);
CREATE INDEX IF NOT EXISTS idx_workflows_profile           ON workflows(profile_id);
CREATE INDEX IF NOT EXISTS idx_workflow_executions_profile ON workflow_executions(profile_id);
CREATE INDEX IF NOT EXISTS idx_credentials_profile         ON credentials(profile_id);
CREATE INDEX IF NOT EXISTS idx_vault_images_profile        ON vault_images(profile_id);
CREATE INDEX IF NOT EXISTS idx_hil_pending_profile         ON hil_pending(profile_id);
CREATE INDEX IF NOT EXISTS idx_tags_profile                ON tags(profile_id);
