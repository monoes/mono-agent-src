-- 166_mission_pursuit.sql
-- The policy record of a mission's pursuit (T6): whether the person has started the perpetual pursuit of the mission's
-- ultimate goal, and how far it may run while they are away. Additive only: one new table, nothing existing is rebuilt,
-- and with no rows it is inert. This is a record, not a loop: nothing here runs anything.
--   enabled                 1 once the person started the pursuit; never goes back to 0 (a pursuit ends with its session)
--   away_budget_parked_max  stop opening work after this many parked questions since the person last acted (1..20)
--   away_budget_pct         ... or after this share of the budget was spent since then (1..100)
--   paused_by               who paused it ('' = running); only the person pauses and resumes
--   locked_by               the person who started it, which locked the goal with it
-- The database refuses to enable a pursuit unless the mission's goal is confirmed (mission_goals, 164) and a person is
-- named, refuses any change of the limits or of who started it, and deletes the row only with its session.
CREATE TABLE IF NOT EXISTS mission_pursuit (
  session_id             INTEGER PRIMARY KEY REFERENCES task_sessions(id) ON DELETE CASCADE,
  enabled                INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
  away_budget_parked_max INTEGER NOT NULL DEFAULT 3 CHECK (away_budget_parked_max BETWEEN 1 AND 20),
  away_budget_pct        INTEGER NOT NULL DEFAULT 20 CHECK (away_budget_pct BETWEEN 1 AND 100),
  paused_by              TEXT NOT NULL DEFAULT '',
  locked_by              TEXT NOT NULL DEFAULT '',
  created_at             TEXT NOT NULL,
  updated_at             TEXT NOT NULL
);

-- Never replaced (a REPLACE would delete the old row without these rules seeing it).
CREATE TRIGGER IF NOT EXISTS mission_pursuit_no_replace BEFORE INSERT ON mission_pursuit
WHEN EXISTS (SELECT 1 FROM mission_pursuit p WHERE p.session_id = NEW.session_id)
BEGIN SELECT RAISE(ABORT, 'a mission pursuit exists: rows are never replaced'); END;

-- A row is born running (not paused), and enabled only for a confirmed goal and a named person.
CREATE TRIGGER IF NOT EXISTS mission_pursuit_born BEFORE INSERT ON mission_pursuit
WHEN NEW.paused_by <> ''
  OR (NEW.enabled = 1 AND (NEW.locked_by = '' OR NOT EXISTS (SELECT 1 FROM mission_goals g WHERE g.session_id = NEW.session_id AND g.state = 'confirmed')))
  OR (NEW.enabled = 0 AND NEW.locked_by <> '')
BEGIN SELECT RAISE(ABORT, 'a mission pursuit starts only for a confirmed goal, by a named person'); END;

-- Forward only: enabled never goes back; it turns on only for a confirmed goal, with a named person. Once started the
-- limits, who started it and the dates never change; pausing and resuming (paused_by) is what moves.
CREATE TRIGGER IF NOT EXISTS mission_pursuit_forward_only BEFORE UPDATE ON mission_pursuit
WHEN NOT (
  NEW.session_id = OLD.session_id AND NEW.created_at = OLD.created_at AND NEW.enabled >= OLD.enabled
  AND (NEW.enabled = OLD.enabled OR (NEW.locked_by <> '' AND EXISTS (SELECT 1 FROM mission_goals g WHERE g.session_id = OLD.session_id AND g.state = 'confirmed')))
  AND (NEW.locked_by = OLD.locked_by OR (OLD.enabled = 0 AND NEW.enabled = 1))
  AND (OLD.enabled = 0 OR (NEW.away_budget_parked_max = OLD.away_budget_parked_max AND NEW.away_budget_pct = OLD.away_budget_pct))
)
BEGIN SELECT RAISE(ABORT, 'a mission pursuit only moves forward, and its limits and its starter do not change'); END;

-- A started pursuit is deleted only with its session.
CREATE TRIGGER IF NOT EXISTS mission_pursuit_started_no_delete BEFORE DELETE ON mission_pursuit
WHEN OLD.enabled = 1 AND EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = OLD.session_id)
BEGIN SELECT RAISE(ABORT, 'a started mission pursuit is never deleted'); END;
