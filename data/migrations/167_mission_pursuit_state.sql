-- 167_mission_pursuit_state.sql
-- What the host loop remembers of a mission's pursuit between ticks and restarts (T6, S4). Additive only: one new table,
-- nothing existing is rebuilt, and with no rows it is inert. Written by the host loop alone, from host rows; no agent and
-- no verb writes it.
--   person_at          the person's latest action the host saw (an answered question, a pause or resume, a message
--                      to the lead), as the host read it from its own rows; '' = none seen yet
--   base_spent_micro   the session's spend in micro-USD at that moment: the away spend is measured from here
--   waiting            why the pursuit opens no new epoch ('' = it may): any non-empty value is read as waiting, so an
--                      unknown value waits too ('parked', 'spend', 'budget-unknown', 'limits' are what the host writes)
--   woken_epoch        the latest epoch whose wake-up of the lead was queued: a restart never wakes the lead twice for
--                      one epoch
-- Not tamper-evident (no MAC): a writer with the database file can write anything, as with mission_pursuit.
CREATE TABLE IF NOT EXISTS mission_pursuit_state (
  session_id       INTEGER PRIMARY KEY REFERENCES task_sessions(id) ON DELETE CASCADE,
  person_at        TEXT NOT NULL DEFAULT '',
  base_spent_micro INTEGER NOT NULL DEFAULT 0 CHECK (base_spent_micro >= 0),
  waiting          TEXT NOT NULL DEFAULT '',
  woken_epoch      INTEGER NOT NULL DEFAULT 0 CHECK (woken_epoch >= 0),
  updated_at       TEXT NOT NULL
);
