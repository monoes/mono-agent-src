-- 174_org_worker_session_cost.sql
-- claude reports a resumed session's cost as the session's total (#1744). A kept dynamic-org worker records the
-- session of its last reported cost and the total it had reached, so a follow-up in a later turn is charged only
-- what it adds. A row kept before this migration has the empty defaults: its first resumed run is charged the whole
-- total, as it was before.
--
-- Additive only: two columns with empty defaults.
ALTER TABLE ai_chat_org_workers ADD COLUMN cost_session TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_chat_org_workers ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;
