-- 162_org_proposals_revoke.sql
-- A proposal can now revoke a grant (verb 'grant.remove', #1477): the verb column carries a CHECK, so the table is
-- rebuilt with the wider list (create / copy / drop / rename; migrations run with foreign keys off and nothing references
-- this table). Rows, their hashes and the two indexes are kept as they were; see 161 for what each column means.
CREATE TABLE org_proposals_v2 (
  id              TEXT PRIMARY KEY,
  profile_id      TEXT NOT NULL,
  org_name        TEXT NOT NULL,
  conversation_id TEXT NOT NULL DEFAULT '',   -- a LABEL the proposer chose, never ownership: caps and supersede are per (org, label); lookups trust the row and its hash
  verb            TEXT NOT NULL CHECK (verb IN ('grant.add', 'grant.remove', 'autonomy.set')),
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
INSERT INTO org_proposals_v2 (id, profile_id, org_name, conversation_id, verb, target, args_json, preview_text, state_digest, hash,
                              created_by, created_at, expires_at, status, result_json, decided_at)
  SELECT id, profile_id, org_name, conversation_id, verb, target, args_json, preview_text, state_digest, hash,
         created_by, created_at, expires_at, status, result_json, decided_at FROM org_proposals;
DROP TABLE org_proposals;
ALTER TABLE org_proposals_v2 RENAME TO org_proposals;
CREATE INDEX IF NOT EXISTS org_proposals_org ON org_proposals(profile_id, org_name, status, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS org_proposals_one_pending ON org_proposals(profile_id, org_name, conversation_id, verb, target) WHERE status = 'pending';
