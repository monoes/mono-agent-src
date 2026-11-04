-- 157_task_session_cap_runs.sql
-- The durable accounting mode of a session's workers (#1248, #1160). A worker run is accounted in ONE of two ways:
-- as a reservation (task_session_reservations: the money is held out of the budget, migration 149/154) or on the
-- session's cap (the run reads what is left, holds nothing, and its cost is added to the spend as it streams). The two
-- must never be in flight on one session together: a cap run's dollars are not held, so a reservation could be
-- granted the same ones. A cap run therefore REGISTERS here for as long as it runs, and the store refuses a
-- reservation on a session while a live cap run is registered (and a cap run while a live reservation exists),
-- both inside the write transaction of the refused call. The mode flips only when the other kind has drained.
--
-- A row holds no money and is never charged: it is presence, with a lease like a reservation's. The owner renews it
-- and deletes it when its run is over; a row whose lease lapsed by more than the grace is dead, counts for nothing,
-- and is swept by the next registration on the session. Deleting the session or the profile removes it.
CREATE TABLE IF NOT EXISTS task_session_cap_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id  TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    session_id  INTEGER NOT NULL REFERENCES task_sessions(id) ON DELETE CASCADE,
    owner_id    TEXT NOT NULL CHECK (typeof(owner_id) = 'text' AND length(CAST(owner_id AS BLOB)) BETWEEN 1 AND 128 AND instr(owner_id, char(0)) = 0),
    run_ref     TEXT NOT NULL CHECK (typeof(run_ref) = 'text' AND length(CAST(run_ref AS BLOB)) BETWEEN 1 AND 256 AND instr(run_ref, char(0)) = 0),
    lease_until TEXT NOT NULL CHECK (typeof(lease_until) = 'text' AND length(CAST(lease_until AS BLOB)) = 20 AND instr(lease_until, char(0)) = 0 AND lease_until GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z'),
    created_at  TEXT NOT NULL CHECK (typeof(created_at) = 'text' AND length(CAST(created_at AS BLOB)) = 20 AND instr(created_at, char(0)) = 0 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z'),
    CHECK (id > 0)
);
CREATE INDEX IF NOT EXISTS idx_task_session_cap_runs_session ON task_session_cap_runs(profile_id, session_id);
