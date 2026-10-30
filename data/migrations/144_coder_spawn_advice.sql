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

-- The spawn intent: written before a spawn creates anything and dropped, in one transaction with the acceptance row of
-- the decisions ledger, when the spawn is complete. A row that outlives its lease belongs to a spawn that crashed (or
-- whose cleanup failed): the host's sweep removes the child session and its draft task session and drops the row. While
-- a row exists the same request (spawner and scope hash) is refused, so a crash cannot be followed by a second child.
-- child is the conversation the spawn made, set once it is linked below the spawner. Additive only.
CREATE TABLE IF NOT EXISTS coder_spawn_intents (
    profile_id TEXT NOT NULL,
    spawner    TEXT NOT NULL,
    scope_hash TEXT NOT NULL,
    child      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, spawner, scope_hash)
);
