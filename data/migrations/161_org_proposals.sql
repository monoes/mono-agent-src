-- 161_org_proposals.sql
-- Proposals to change an org's grants or autonomy, made by a chat or an agent and confirmed by the user (#1477).
-- A row is inert: it changes nothing until `org proposal confirm` (operator only) claims it and runs the real
-- `org grant add` / `org autonomy set` under the gate's verdict at that moment. Nothing stored here stands in for
-- that verdict.
--   args_json     the typed arguments, never a command line
--   preview_text  what the user is shown, rendered by the CLI from args_json and the live org
--   state_digest  digest of the live grant row / autonomy doc the change is made against; confirm refuses when it moved
--   hash          sha256 over id, profile, org, verb, args_json, preview_text and state_digest: what the card shows and
--                 what confirm is given back; recomputed from the row, never trusted from this column
--   status        pending -> applying (the single-use claim) -> applied | failed; or declined, stale, expired
-- Decided rows are kept 30 days (the audit trail), then dropped by the next create.
CREATE TABLE IF NOT EXISTS org_proposals (
  id              TEXT PRIMARY KEY,
  profile_id      TEXT NOT NULL,
  org_name        TEXT NOT NULL,
  conversation_id TEXT NOT NULL DEFAULT '',
  verb            TEXT NOT NULL CHECK (verb IN ('grant.add', 'autonomy.set')),
  target          TEXT NOT NULL DEFAULT '',   -- what a newer proposal supersedes: "<role>/<alias>" or "autonomy"
  args_json       TEXT NOT NULL,
  preview_text    TEXT NOT NULL,
  state_digest    TEXT NOT NULL,
  hash            TEXT NOT NULL,
  created_by      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  expires_at      TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'applying', 'applied', 'failed', 'declined', 'stale', 'expired')),
  result_json     TEXT NOT NULL DEFAULT '',
  decided_at      TEXT
);
CREATE INDEX IF NOT EXISTS org_proposals_org ON org_proposals(profile_id, org_name, status, expires_at);
-- One pending proposal per target: a newer one supersedes the older in the same transaction, and two creates that
-- race cannot both leave a pending row.
CREATE UNIQUE INDEX IF NOT EXISTS org_proposals_one_pending ON org_proposals(profile_id, org_name, verb, target) WHERE status = 'pending';
