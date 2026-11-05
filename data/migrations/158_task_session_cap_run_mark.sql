-- 158_task_session_cap_run_mark.sql
-- A cap run records, when it registers, the newest reservation id that existed (reservation ids only grow). The id
-- is a clock-free ORDER between a cap run and the reservations of its session: a reservation above the mark was
-- taken after the run began, one at or below it before. The heartbeats use it to refuse a renewal that would put the
-- two accounting modes side by side whatever the writers' clocks say (two handles whose clocks differ by more than
-- the lease and the grace read each other's live rows as lapsed). Rows that exist now cannot have a reservation
-- granted beside them yet, so they take the newest id there is.
ALTER TABLE task_session_cap_runs ADD COLUMN reservation_mark INTEGER NOT NULL DEFAULT 0;
UPDATE task_session_cap_runs SET reservation_mark = (SELECT COALESCE(MAX(id), 0) FROM task_session_reservations);
