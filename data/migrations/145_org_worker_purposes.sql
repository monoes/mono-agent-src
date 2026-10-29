-- 145_org_worker_purposes.sql
-- A kept dynamic-org worker (a veteran) records what the operator's purpose model sets saw when it was staffed:
-- whether its role was built-in, and the purposes whose sets limited it (comma-separated ids). A follow-up to
-- the veteran rechecks its model against the operator's CURRENT sets from these. A row kept before this
-- migration has the empty defaults, which the check treats as an unmapped role (every restricted purpose applies).
--
-- Additive only: two columns with empty defaults.
ALTER TABLE ai_chat_org_workers ADD COLUMN builtin INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ai_chat_org_workers ADD COLUMN purposes TEXT NOT NULL DEFAULT '';
