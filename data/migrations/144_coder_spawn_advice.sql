-- 144_coder_spawn_advice.sql
-- The spawn advisory (docs/mastermind/plans/2026-10-28-mission-research-directions.md, section 3): a small piece of work
-- is refused a session of its own once ("doable in this session"), and the second call with a reason is accepted. One
-- row remembers that the first call was refused, so the rule survives a restart of the host or the tool process. It is
-- keyed by the spawner's conversation and a hash of the request's title, kind and scope, and removed when the spawn that
-- repeats it is accepted. Additive only.
CREATE TABLE IF NOT EXISTS coder_spawn_advice (
    profile_id TEXT NOT NULL,
    spawner    TEXT NOT NULL,
    scope_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, spawner, scope_hash)
);
