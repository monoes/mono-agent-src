-- 154_task_reservation_exec.sql
-- One hold per EXEC of a worker, and the cost that exec reported (#1248, #1160). Additive: nothing here changes a row
-- or a rule of migration 149, and the store functions that use it (ReserveExec, ChargeExec in internal/tasks) are
-- not called by any worker yet.
--
-- exec_id names the run (one launch of a worker) a reservation was taken for. '' is a hold taken by the older
-- Reserve, which has no run to name. For a run that names itself the table allows one hold per (profile, session,
-- worker_ref, exec_id): a retry of the reserve finds the row instead of taking a second hold, and a late call of an
-- earlier run of the same worker holds the id of ITS row, never the next run's. The id is fixed for the life of the row.
ALTER TABLE task_session_reservations ADD COLUMN exec_id TEXT NOT NULL DEFAULT '' CHECK (typeof(exec_id) = 'text' AND length(CAST(exec_id AS BLOB)) <= 256 AND instr(exec_id, char(0)) = 0);
CREATE UNIQUE INDEX IF NOT EXISTS uq_task_session_reservations_exec ON task_session_reservations(profile_id, session_id, worker_ref, exec_id) WHERE exec_id <> '';
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservations_exec_pinned BEFORE UPDATE OF exec_id ON task_session_reservations
WHEN NEW.exec_id IS NOT OLD.exec_id
BEGIN SELECT RAISE(ABORT, 'reservations: the exec of a reservation cannot change'); END;

-- What one exec reported of its cost so far, one row per report, so the charge can be rebuilt from the record:
-- the charge of a reservation is MAX(what it was charged, the largest cumulative_usd reported HERE), which a report
-- below an earlier one cannot lower. seq counts the reports of the reservation from 1; final marks the report the run
-- ended with (a run that ended without any report has no row, and is charged its whole grant). The table only grows:
-- a row is neither changed nor removed by hand, goes with its reservation (session or profile delete), and is
-- written only while the reservation is live and has no final report yet.
CREATE TABLE IF NOT EXISTS task_session_reservation_costs (
    reservation_id INTEGER NOT NULL REFERENCES task_session_reservations(id) ON DELETE CASCADE,
    seq            INTEGER NOT NULL CHECK (seq >= 1),
    cumulative_usd REAL NOT NULL CHECK (typeof(cumulative_usd) IN ('real', 'integer') AND cumulative_usd >= 0 AND cumulative_usd <= 1e9),
    final          INTEGER NOT NULL CHECK (final IN (0, 1)),
    reported_at    TEXT NOT NULL CHECK (typeof(reported_at) = 'text' AND length(CAST(reported_at AS BLOB)) = 20 AND instr(reported_at, char(0)) = 0 AND reported_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z'),
    PRIMARY KEY (reservation_id, seq)
) WITHOUT ROWID;

CREATE TRIGGER IF NOT EXISTS trg_task_session_reservation_costs_live BEFORE INSERT ON task_session_reservation_costs
WHEN NOT EXISTS (SELECT 1 FROM task_session_reservations WHERE id = NEW.reservation_id AND exec_id <> '' AND state = 'live')
BEGIN SELECT RAISE(ABORT, 'reservation costs: a report is for a live reservation of an exec'); END;
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservation_costs_seq BEFORE INSERT ON task_session_reservation_costs
WHEN NEW.seq <> COALESCE((SELECT MAX(seq) FROM task_session_reservation_costs WHERE reservation_id = NEW.reservation_id), 0) + 1
BEGIN SELECT RAISE(ABORT, 'reservation costs: the reports of a reservation are numbered in order'); END;
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservation_costs_after_final BEFORE INSERT ON task_session_reservation_costs
WHEN EXISTS (SELECT 1 FROM task_session_reservation_costs WHERE reservation_id = NEW.reservation_id AND final = 1)
BEGIN SELECT RAISE(ABORT, 'reservation costs: nothing is reported after the final report'); END;
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservation_costs_no_update BEFORE UPDATE ON task_session_reservation_costs
BEGIN SELECT RAISE(ABORT, 'reservation costs: a report is never changed'); END;
CREATE TRIGGER IF NOT EXISTS trg_task_session_reservation_costs_no_delete BEFORE DELETE ON task_session_reservation_costs
WHEN EXISTS (SELECT 1 FROM task_session_reservations WHERE id = OLD.reservation_id)
BEGIN SELECT RAISE(ABORT, 'reservation costs: reports go with their reservation, not by hand'); END;
