-- 167_mission_pursuit_state.sql
-- What the host loop remembers of a mission's pursuit between ticks and restarts (T6, S4). Additive only: one new table,
-- nothing existing is rebuilt, and with no rows it is inert. Written by the host loop alone, from host rows; no agent and
-- no verb writes it.
--   person_at          the person's latest action the host saw (an answered question, a pause or resume, a message
--                      to the lead), as the host read it from its own rows; '' = none seen yet
--   base_spent_micro   the session's spend in micro-USD at that moment: the away spend is measured from here
--   waiting            why the pursuit opens no new epoch ('' = it may): any non-empty value is read as waiting, so an
--                      unknown value waits too ('parked', 'spend', 'budget-unknown', 'limits' are what the host writes)
--   wakes_since        wake-ups of the lead queued since the person last acted (a hard stop that does not depend on the meter)
--   woken_epoch        the latest epoch whose wake-up of the lead was queued: a restart never wakes the lead twice for
--                      one epoch
-- This migration is UNRELEASED (it is not in master and no database has applied it), so it is edited in place as the pursuit
-- loop's design settled; a released migration would get a new number instead.
-- Not tamper-evident (no MAC): a writer with the database file can write anything, as with mission_pursuit.
CREATE TABLE IF NOT EXISTS mission_pursuit_state (
  session_id       INTEGER PRIMARY KEY REFERENCES task_sessions(id) ON DELETE CASCADE,
  person_at        TEXT NOT NULL DEFAULT '',
  base_spent_micro INTEGER NOT NULL DEFAULT 0 CHECK (base_spent_micro >= 0),
  waiting          TEXT NOT NULL DEFAULT '',
  woken_epoch      INTEGER NOT NULL DEFAULT 0 CHECK (woken_epoch >= 0),
  wakes_since      INTEGER NOT NULL DEFAULT 0 CHECK (wakes_since >= 0),
  -- what the status shows (written by the loop with the rest, as of updated_at): the away counters and the hourly cap
  parked           INTEGER NOT NULL DEFAULT 0 CHECK (parked >= 0),
  spent_away_micro INTEGER NOT NULL DEFAULT 0 CHECK (spent_away_micro >= 0),
  budget_micro     INTEGER NOT NULL DEFAULT 0 CHECK (budget_micro >= 0),
  hour_wakes       INTEGER NOT NULL DEFAULT 0 CHECK (hour_wakes >= 0),
  hour_frees_at    TEXT NOT NULL DEFAULT '',
  updated_at       TEXT NOT NULL
);

-- How many wake-ups of the lead the pursuit may queue since the person last acted (a hard stop that does not depend on
-- the meter), set when the person starts the pursuit like the other away limits and fixed from then on.
ALTER TABLE mission_pursuit ADD COLUMN away_wakes_max INTEGER NOT NULL DEFAULT 6 CHECK (away_wakes_max BETWEEN 1 AND 50);
CREATE TRIGGER IF NOT EXISTS mission_pursuit_wakes_fixed BEFORE UPDATE ON mission_pursuit
WHEN NEW.away_wakes_max <> OLD.away_wakes_max AND OLD.enabled = 1
BEGIN SELECT RAISE(ABORT, 'a started mission pursuit keeps its wake-up limit'); END;

-- The loop walks a mission's chats downwards (parent_conversation_id) on every pass; without this index each step scans the
-- profile's whole coder_sessions table. Additive and idempotent.
CREATE INDEX IF NOT EXISTS idx_coder_sessions_parent ON coder_sessions(profile_id, parent_conversation_id);
