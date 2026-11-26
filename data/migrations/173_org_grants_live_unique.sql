-- 173_org_grants_live_unique.sql
-- One live org_grants row per slot: per (profile, org, role, alias) for an
-- automation grant, and per (profile, org, role) for an org-tool grant.
-- Writers used to read and then write with no lock, so two at once could leave
-- two live rows, and revoking or narrowing one left the other in force.
--
-- The rows that already share a slot are merged just before these statements,
-- in the same transaction, by dedupeOrgGrants (internal/storage), which
-- records what it revoked or rewrote in org_grant_dedupe_log.
--
-- The slot expressions below are the ones dedupeOrgGrants groups by. A row
-- whose tools_json cannot be read has no alias and is not constrained.

CREATE UNIQUE INDEX IF NOT EXISTS org_grants_live_alias
    ON org_grants(profile_id, org_name, role_id,
        (CASE WHEN json_valid(tools_json) THEN json_extract(tools_json, '$[0].alias') END))
    WHERE revoked_at IS NULL AND tools_json NOT IN ('[]', 'null');

CREATE UNIQUE INDEX IF NOT EXISTS org_grants_live_org_tools
    ON org_grants(profile_id, org_name, role_id)
    WHERE revoked_at IS NULL AND tools_json IN ('[]', 'null');
