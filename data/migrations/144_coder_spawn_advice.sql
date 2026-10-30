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
-- the decisions ledger, when the spawn is complete. At most ONE intent exists per spawner (the key), whatever the
-- request, so a lead cannot pile up unresolved spawns. state is 'pending' while the spawn runs and 'sweeping' once the
-- host's sweep has claimed it; generation names the claim: the spawn is created with one, the sweep's claim bumps it,
-- and every write of the spawn (the child, the finish) carries its own, so an old spawn can never touch the claim of
-- the sweep or a replacement intent. A pending row that outlives its lease belongs to a spawn that crashed (or whose
-- cleanup failed): the sweep claims it, removes the child session and its draft task session, and drops the row.
-- A claim stamps claimed_at and counts in attempts: a claimed row is claimed again only after a lease that doubles with
-- every attempt, and after 5 attempts, or as soon as the child's task session is no longer a draft (the operator
-- approved it), the row becomes 'needs-operator': nothing is touched any more and the lead stays blocked until a person
-- looks at it.
-- child is the conversation the spawn made, set once it is linked below the spawner. Additive only.
CREATE TABLE IF NOT EXISTS coder_spawn_intents (
    profile_id TEXT NOT NULL,
    spawner    TEXT NOT NULL,
    scope_hash TEXT NOT NULL,
    child      TEXT NOT NULL DEFAULT '',
    state      TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'sweeping', 'needs-operator')),
    generation INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    claimed_at TEXT NOT NULL DEFAULT '',
    attempts   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (profile_id, spawner)
);
