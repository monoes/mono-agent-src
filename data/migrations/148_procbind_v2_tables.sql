-- 148_procbind_v2_tables.sql
-- Storage for the peer-credential process binding (docs/mastermind/specs/2026-10-28-peer-credential-binding.md,
-- section 7). Nothing reads or writes these tables yet: they exist, off, until the binding service is wired
-- in behind its operator-only flag. A token bound this way lives ONLY here, never in agent_tokens or
-- coder_session_tokens, so a binary that predates it cannot resolve the token at all.
--
-- One rule for all five tables: UPDATE is always rejected, DELETE is rejected unless the row is past
-- retention (7 days after the row's expiry; a host row, once a newer one supersedes it). There is no
-- exempt table or operation. The retention window is fixed in the triggers; a DELETE of a row inside it
-- fails, with or without the vault key (a writer who drops the triggers is the database-writer case the
-- design excludes). Timestamps are the same text form as agent_tokens (2006-01-02T15:04:05.000Z), so
-- they compare as strings; a CHECK holds every one of them to exactly that form (text, 24 bytes, the pattern),
-- so retention can read them. The byte length is taken on a BLOB cast because SQLite's length() and GLOB
-- stop at an embedded NUL.

CREATE TABLE IF NOT EXISTS agent_tokens_v2 (
    id               TEXT PRIMARY KEY,
    profile_id       TEXT NOT NULL,
    token_hash       TEXT NOT NULL UNIQUE,
    mission_owner    TEXT NOT NULL DEFAULT '',
    mission_session  INTEGER NOT NULL DEFAULT 0,
    mission_task     INTEGER NOT NULL DEFAULT 0,
    mission_role     TEXT NOT NULL DEFAULT '',
    mission_actor    TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL CHECK (typeof(created_at) = 'text' AND length(CAST(created_at AS BLOB)) = 24 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
    expires_at       TEXT NOT NULL CHECK (typeof(expires_at) = 'text' AND length(CAST(expires_at AS BLOB)) = 24 AND expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
    version          INTEGER NOT NULL,
    mac              TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_tokens_v2_owner ON agent_tokens_v2(profile_id, mission_owner);

CREATE TABLE IF NOT EXISTS coder_session_tokens_v2 (
    id               TEXT PRIMARY KEY,
    profile_id       TEXT NOT NULL,
    token_hash       TEXT NOT NULL UNIQUE,
    conversation_id  TEXT NOT NULL,
    turn_id          TEXT NOT NULL,
    created_at       TEXT NOT NULL CHECK (typeof(created_at) = 'text' AND length(CAST(created_at AS BLOB)) = 24 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
    expires_at       TEXT NOT NULL CHECK (typeof(expires_at) = 'text' AND length(CAST(expires_at AS BLOB)) = 24 AND expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
    version          INTEGER NOT NULL,
    mac              TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_coder_session_tokens_v2_session ON coder_session_tokens_v2(profile_id, conversation_id);

-- One binding per token: the worker process the token is bound to, signed with its mode and version.
CREATE TABLE IF NOT EXISTS process_bindings (
    token_id         TEXT PRIMARY KEY,
    profile_id       TEXT NOT NULL,
    scope            TEXT NOT NULL,
    mode             TEXT NOT NULL CHECK (mode = 'peercred'),
    worker_pid       INTEGER NOT NULL,
    worker_stamp     TEXT NOT NULL,
    host_generation  TEXT NOT NULL,
    version          INTEGER NOT NULL,
    created_at       TEXT NOT NULL CHECK (typeof(created_at) = 'text' AND length(CAST(created_at AS BLOB)) = 24 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
    expires_at       TEXT NOT NULL CHECK (typeof(expires_at) = 'text' AND length(CAST(expires_at AS BLOB)) = 24 AND expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
    mac              TEXT NOT NULL
);

-- A revocation follows the token it revokes and is kept until that token is long expired.
CREATE TABLE IF NOT EXISTS token_revocations_v2 (
    token_kind       TEXT NOT NULL CHECK (token_kind IN ('mission', 'session')),
    token_id         TEXT NOT NULL,
    profile_id       TEXT NOT NULL,
    reason           TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL CHECK (typeof(created_at) = 'text' AND length(CAST(created_at AS BLOB)) = 24 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
    mac              TEXT NOT NULL,
    PRIMARY KEY (token_kind, token_id)
);

-- Host starts: one row per start, the highest seq of a profile is the current generation. It protects the
-- latest row only; it makes no claim about the continuity of older history.
CREATE TABLE IF NOT EXISTS procbind_host (
    profile_id       TEXT NOT NULL,
    seq              INTEGER NOT NULL,
    generation       TEXT NOT NULL,
    started_at       TEXT NOT NULL CHECK (typeof(started_at) = 'text' AND length(CAST(started_at AS BLOB)) = 24 AND started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
    mac              TEXT NOT NULL,
    PRIMARY KEY (profile_id, seq)
);

-- UPDATE is rejected outright on every table.
CREATE TRIGGER IF NOT EXISTS trg_agent_tokens_v2_insert_only BEFORE UPDATE ON agent_tokens_v2
BEGIN SELECT RAISE(ABORT, 'procbind: rows are insert-only'); END;
CREATE TRIGGER IF NOT EXISTS trg_coder_session_tokens_v2_insert_only BEFORE UPDATE ON coder_session_tokens_v2
BEGIN SELECT RAISE(ABORT, 'procbind: rows are insert-only'); END;
CREATE TRIGGER IF NOT EXISTS trg_process_bindings_insert_only BEFORE UPDATE ON process_bindings
BEGIN SELECT RAISE(ABORT, 'procbind: rows are insert-only'); END;
CREATE TRIGGER IF NOT EXISTS trg_token_revocations_v2_insert_only BEFORE UPDATE ON token_revocations_v2
BEGIN SELECT RAISE(ABORT, 'procbind: rows are insert-only'); END;
CREATE TRIGGER IF NOT EXISTS trg_procbind_host_insert_only BEFORE UPDATE ON procbind_host
BEGIN SELECT RAISE(ABORT, 'procbind: rows are insert-only'); END;

-- Token and binding rows: removable only when their own expiry is more than 7 days past.
CREATE TRIGGER IF NOT EXISTS trg_agent_tokens_v2_retention BEFORE DELETE ON agent_tokens_v2
WHEN OLD.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-7 days')
BEGIN SELECT RAISE(ABORT, 'procbind: the row is within its retention window'); END;
CREATE TRIGGER IF NOT EXISTS trg_coder_session_tokens_v2_retention BEFORE DELETE ON coder_session_tokens_v2
WHEN OLD.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-7 days')
BEGIN SELECT RAISE(ABORT, 'procbind: the row is within its retention window'); END;
CREATE TRIGGER IF NOT EXISTS trg_process_bindings_retention BEFORE DELETE ON process_bindings
WHEN OLD.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-7 days')
BEGIN SELECT RAISE(ABORT, 'procbind: the row is within its retention window'); END;

-- A revocation: removable when the token of ITS kind expired more than 7 days ago (a mission token and a
-- session token may share an id, so the kind picks the table); with no token row left, when the
-- revocation itself is more than 7 days old.
CREATE TRIGGER IF NOT EXISTS trg_token_revocations_v2_retention BEFORE DELETE ON token_revocations_v2
WHEN COALESCE(
        CASE OLD.token_kind
            WHEN 'mission' THEN (SELECT expires_at FROM agent_tokens_v2 WHERE id = OLD.token_id)
            ELSE (SELECT expires_at FROM coder_session_tokens_v2 WHERE id = OLD.token_id)
        END,
        OLD.created_at) > strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-7 days')
BEGIN SELECT RAISE(ABORT, 'procbind: the row is within its retention window'); END;

-- A host row: only one a newer row of the same profile supersedes, and only past the window. The newest
-- row of a profile is never removable, not even after the profile is gone.
CREATE TRIGGER IF NOT EXISTS trg_procbind_host_retention BEFORE DELETE ON procbind_host
WHEN OLD.started_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-7 days')
  OR NOT EXISTS (SELECT 1 FROM procbind_host WHERE profile_id = OLD.profile_id AND seq > OLD.seq)
BEGIN SELECT RAISE(ABORT, 'procbind: the row is the newest or within its retention window'); END;

-- REPLACE, INSERT OR REPLACE and an upsert replace a row without firing the DELETE or UPDATE triggers above
-- (recursive triggers are off), so an insert whose key already exists is refused outright, whatever its
-- conflict clause.
CREATE TRIGGER IF NOT EXISTS trg_agent_tokens_v2_no_replace BEFORE INSERT ON agent_tokens_v2
WHEN EXISTS (SELECT 1 FROM agent_tokens_v2 WHERE id = NEW.id OR token_hash = NEW.token_hash)
BEGIN SELECT RAISE(ABORT, 'procbind: rows are insert-only'); END;
CREATE TRIGGER IF NOT EXISTS trg_coder_session_tokens_v2_no_replace BEFORE INSERT ON coder_session_tokens_v2
WHEN EXISTS (SELECT 1 FROM coder_session_tokens_v2 WHERE id = NEW.id OR token_hash = NEW.token_hash)
BEGIN SELECT RAISE(ABORT, 'procbind: rows are insert-only'); END;
CREATE TRIGGER IF NOT EXISTS trg_process_bindings_no_replace BEFORE INSERT ON process_bindings
WHEN EXISTS (SELECT 1 FROM process_bindings WHERE token_id = NEW.token_id)
BEGIN SELECT RAISE(ABORT, 'procbind: rows are insert-only'); END;
CREATE TRIGGER IF NOT EXISTS trg_token_revocations_v2_no_replace BEFORE INSERT ON token_revocations_v2
WHEN EXISTS (SELECT 1 FROM token_revocations_v2 WHERE token_kind = NEW.token_kind AND token_id = NEW.token_id)
BEGIN SELECT RAISE(ABORT, 'procbind: rows are insert-only'); END;
CREATE TRIGGER IF NOT EXISTS trg_procbind_host_no_replace BEFORE INSERT ON procbind_host
WHEN EXISTS (SELECT 1 FROM procbind_host WHERE profile_id = NEW.profile_id AND seq = NEW.seq)
BEGIN SELECT RAISE(ABORT, 'procbind: rows are insert-only'); END;
