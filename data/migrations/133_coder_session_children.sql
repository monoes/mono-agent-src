-- 133_coder_session_children.sql
-- Master coder sessions create and control their descendant sessions (P4 of
-- docs/mastermind/plans/2026-10-26-coder-session-host.md). Additive only.
--
-- budget_usd: the spend cap carved out of the parent's for a child session (0 = none).
-- coder_session_tokens: the caller identity of a host-started turn; only the SHA-256
--   of the token is stored, and it names one profile, conversation and turn. pid and
--   stamp are the turn's process (set by the host right after it starts the turn) and
--   mac signs the whole row under a key derived from the profile's vault key.
-- coder_session_turn_caps: the spend cap each turn of a budgeted session ran under (a turn whose runtime
--   reported no cost counts as having spent that cap).
-- coder_session_commands.issuer: the master that queued a command ('' = a person or the host).
-- coder_session_spawns: one row per child a session created, for the spawn rate limit.
ALTER TABLE coder_sessions ADD COLUMN budget_usd REAL NOT NULL DEFAULT 0;
ALTER TABLE coder_session_commands ADD COLUMN issuer TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS coder_session_tokens (
    token_hash      TEXT PRIMARY KEY,
    profile_id      TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    turn_id         TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    expires_at      TEXT NOT NULL,
    pid             INTEGER NOT NULL DEFAULT 0,
    stamp           TEXT NOT NULL DEFAULT '',
    mac             TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_coder_session_tokens_conv ON coder_session_tokens(profile_id, conversation_id);

CREATE TABLE IF NOT EXISTS coder_session_spawns (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id      TEXT NOT NULL,
    parent          TEXT NOT NULL,
    child           TEXT NOT NULL,
    created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_coder_session_spawns_parent ON coder_session_spawns(profile_id, parent, id);

CREATE TABLE IF NOT EXISTS coder_session_turn_caps (
    profile_id      TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    turn_id         TEXT NOT NULL,
    cap_usd         REAL NOT NULL,
    started         INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (profile_id, conversation_id, turn_id)
);

-- coder_session_host_turns: turns that started from a send queued by a master (issuer set) under a turn id
--   without a known host-made prefix; the check-in streak counts them like master- and checkin- turns.
CREATE TABLE IF NOT EXISTS coder_session_host_turns (
    profile_id      TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    turn_id         TEXT NOT NULL,
    PRIMARY KEY (profile_id, conversation_id, turn_id)
);
