-- 149_task_session_reservations.sql
-- Money a worker holds out of a task session's budget, one row per reservation (design note for #1160). Nothing
-- reads or writes this table yet: the store API (internal/tasks) exists, dark, until a later change wires the
-- workers to it. The session's held money is DERIVED from these rows (plus its spend and its children), never
-- kept as a counter that could drift, so deleting a session or a profile returns its money with no repair.
--
-- id is the reservation's generation: AUTOINCREMENT, so it is never reused. owner_id is minted per process
-- run, and only the owner may heartbeat, settle or release a live row. A row whose lease lapsed is marked
-- expired by anyone and STILL holds what is left of it until the operator resolves it. A session with no
-- budget still records its rows (bounded says whether the worker named a cap), so a budget set later sees them.
--
-- Rows are not deleted by hand: a direct DELETE is refused while the session of the row exists, in every state (ended
-- rows are the audit trail). A session or profile delete cascades, which is the intended way for the money to
-- come back (the worker's next heartbeat then finds no row and stops). The amount columns are capped at 1e15,
-- an overflow guard that the store also holds every budget, amount and cost to (tasks.MaxBudgetUSD).
-- The supported writer is the store API in internal/tasks (owner-fenced functions). The triggers below keep the
-- shape of a row honest; they are not a defence against an operator who edits charged_usd or state with raw SQL
-- (which would also have to keep task_sessions.spent_usd in step), and that is out of scope.
-- Timestamps are the same text
-- form as tasks (2006-01-02T15:04:05Z), held to it by a CHECK that reads the bytes (GLOB and length stop at an
-- embedded NUL, so the byte length and instr(.., char(0)) are checked too).
CREATE TABLE IF NOT EXISTS task_session_reservations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id   TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    session_id   INTEGER NOT NULL REFERENCES task_sessions(id) ON DELETE CASCADE,
    worker_ref   TEXT NOT NULL CHECK (typeof(worker_ref) = 'text' AND length(CAST(worker_ref AS BLOB)) BETWEEN 1 AND 256 AND instr(worker_ref, char(0)) = 0),
    owner_id     TEXT NOT NULL CHECK (typeof(owner_id) = 'text' AND length(CAST(owner_id AS BLOB)) BETWEEN 1 AND 128 AND instr(owner_id, char(0)) = 0),
    amount_usd   REAL NOT NULL CHECK (typeof(amount_usd) IN ('real', 'integer') AND amount_usd >= 0 AND amount_usd <= 1e15),
    bounded      INTEGER NOT NULL CHECK (bounded IN (0, 1)),
    charged_usd  REAL NOT NULL DEFAULT 0 CHECK (typeof(charged_usd) IN ('real', 'integer') AND charged_usd >= 0 AND charged_usd <= 1e15),
    state        TEXT NOT NULL CHECK (state IN ('live', 'settled', 'released', 'expired', 'resolved')),
    lease_until  TEXT NOT NULL CHECK (typeof(lease_until) = 'text' AND length(CAST(lease_until AS BLOB)) = 20 AND instr(lease_until, char(0)) = 0 AND lease_until GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z'),
    heartbeat_at TEXT NOT NULL CHECK (typeof(heartbeat_at) = 'text' AND length(CAST(heartbeat_at AS BLOB)) = 20 AND instr(heartbeat_at, char(0)) = 0 AND heartbeat_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z'),
    created_at   TEXT NOT NULL CHECK (typeof(created_at) = 'text' AND length(CAST(created_at AS BLOB)) = 20 AND instr(created_at, char(0)) = 0 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z'),
    ended_at     TEXT NOT NULL DEFAULT '' CHECK (ended_at = '' OR (typeof(ended_at) = 'text' AND length(CAST(ended_at AS BLOB)) = 20 AND instr(ended_at, char(0)) = 0 AND ended_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z')),
    end_note     TEXT NOT NULL DEFAULT '' CHECK (typeof(end_note) = 'text' AND length(CAST(end_note AS BLOB)) <= 1024 AND instr(end_note, char(0)) = 0),
    CHECK (id > 0),
    CHECK (bounded = 1 OR amount_usd = 0)
);
CREATE INDEX IF NOT EXISTS idx_task_session_reservations_held ON task_session_reservations(profile_id, session_id) WHERE state IN ('live', 'expired');

-- Only the table hands out ids (an explicit one could sit near the top of the range and exhaust AUTOINCREMENT),
-- and a reservation is on the profile of its session. (An insert that names no id shows as -1 to a BEFORE trigger,
-- which the CHECK on id keeps from being a real id.)
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_auto_id_only BEFORE INSERT ON task_session_reservations
WHEN NEW.id <> -1
BEGIN SELECT RAISE(ABORT, 'reservations: the id is assigned by the table'); END;
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_profile_tie BEFORE INSERT ON task_session_reservations
WHEN NOT EXISTS (SELECT 1 FROM task_sessions WHERE id = NEW.session_id AND profile_id = NEW.profile_id)
BEGIN SELECT RAISE(ABORT, 'reservations: the profile must be the profile of the session'); END;

-- REPLACE, INSERT OR REPLACE, an upsert and UPDATE OR REPLACE replace a row without firing the UPDATE or DELETE
-- triggers below (recursive triggers are off), so an insert whose id exists is refused outright. An explicit id
-- at or below the highest ever issued is refused too, so a generation is never reused, even after a delete.
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_no_replace BEFORE INSERT ON task_session_reservations
WHEN NEW.id <> -1 AND EXISTS (SELECT 1 FROM task_session_reservations WHERE id = NEW.id)
BEGIN SELECT RAISE(ABORT, 'reservations: a reservation cannot be replaced'); END;
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_generation BEFORE INSERT ON task_session_reservations
WHEN NEW.id <> -1 AND NEW.id <= COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'task_session_reservations'), 0)
BEGIN SELECT RAISE(ABORT, 'reservations: a generation is never reused'); END;
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_starts_live BEFORE INSERT ON task_session_reservations
WHEN NEW.state IS NOT 'live' OR NEW.charged_usd IS NOT 0 OR NEW.ended_at IS NOT ''
BEGIN SELECT RAISE(ABORT, 'reservations: a reservation starts live and uncharged'); END;

-- Identity never changes.
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_pinned
BEFORE UPDATE OF id, profile_id, session_id, worker_ref, owner_id, amount_usd, bounded, created_at ON task_session_reservations
BEGIN SELECT RAISE(ABORT, 'reservations: the identity of a reservation cannot change'); END;
-- A live row ends once (settled, released or expired), an expired row only by the operator's resolve.
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_state BEFORE UPDATE OF state ON task_session_reservations
WHEN NOT ((OLD.state = 'live' AND NEW.state IN ('live', 'settled', 'released', 'expired'))
       OR (OLD.state = 'expired' AND NEW.state IN ('expired', 'resolved')))
BEGIN SELECT RAISE(ABORT, 'reservations: that state change is not allowed'); END;
-- A terminal row never changes again.
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_terminal BEFORE UPDATE ON task_session_reservations
WHEN OLD.state IN ('settled', 'released', 'resolved')
BEGIN SELECT RAISE(ABORT, 'reservations: an ended reservation stays ended'); END;
-- What was charged and how long the lease runs only grow.
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_grow BEFORE UPDATE OF charged_usd, lease_until ON task_session_reservations
WHEN NEW.charged_usd < OLD.charged_usd OR NEW.lease_until < OLD.lease_until
BEGIN SELECT RAISE(ABORT, 'reservations: charged and lease only grow'); END;
-- An expired row is frozen until it is resolved: it holds what is left, nothing else moves.
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_expired_frozen BEFORE UPDATE ON task_session_reservations
WHEN OLD.state = 'expired' AND NEW.state = 'expired'
BEGIN SELECT RAISE(ABORT, 'reservations: an expired reservation changes only by being resolved'); END;
-- A live row has not ended.
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_ended_live BEFORE UPDATE OF state, ended_at ON task_session_reservations
WHEN NEW.state = 'live' AND NEW.ended_at <> ''
BEGIN SELECT RAISE(ABORT, 'reservations: a live reservation has not ended'); END;
-- No direct delete while the session exists; the cascade of removing the session or the profile still works
-- (the parent row is already gone when it runs; a profile delete reaches the reservation before its session, so
-- the profile is checked too).
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_no_delete BEFORE DELETE ON task_session_reservations
WHEN EXISTS (SELECT 1 FROM task_sessions WHERE id = OLD.session_id) AND EXISTS (SELECT 1 FROM profiles WHERE id = OLD.profile_id)
BEGIN SELECT RAISE(ABORT, 'reservations: a reservation is removed with its session, not by hand'); END;
