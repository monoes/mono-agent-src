-- 164_mission_goals.sql
-- The ultimate goal of a mission, as typed criteria (the T6 mission pursuit). Additive only: two new tables, nothing
-- existing is rebuilt, and with no rows it is inert. A mission session has at most one goal record.
--   mission_goals          one row per mission session: proposed -> confirmed -> locked, forward only
--   mission_goal_criteria  its criteria in order (pos 1..16): command | file | output | rubric; hash is the criterion's
--                          hash, criteria_hash (on the goal) the hash of the whole ordered set, which is what the
--                          person is shown and what a confirmation is given back
-- The lead (or any agent) may only PROPOSE, and may revise its proposal until the person confirms it. A confirmed goal is
-- final: its criteria and hashes never change and the row is never replaced, so that a changed goal means a new mission.
-- The database refuses a confirmation that names nobody, one without a rubric criterion (the person is always the final
-- judge), and every edit of a confirmed or locked goal, whatever code path writes. Deleting the session deletes both
-- tables' rows with it (the cascade is the one delete these rules let through).
CREATE TABLE IF NOT EXISTS mission_goals (
  session_id    INTEGER PRIMARY KEY REFERENCES task_sessions(id) ON DELETE CASCADE,
  state         TEXT NOT NULL CHECK (state IN ('proposed', 'confirmed', 'locked')),
  criteria_hash TEXT NOT NULL,
  proposed_by   TEXT NOT NULL,
  confirmed_by  TEXT NOT NULL DEFAULT '',
  confirmed_at  TEXT NOT NULL DEFAULT '',
  locked_at     TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS mission_goal_criteria (
  session_id INTEGER NOT NULL REFERENCES mission_goals(session_id) ON DELETE CASCADE,
  pos        INTEGER NOT NULL CHECK (pos BETWEEN 1 AND 16),
  kind       TEXT NOT NULL CHECK (kind IN ('command', 'file', 'output', 'rubric')),
  spec       TEXT NOT NULL,
  expected   TEXT NOT NULL DEFAULT '',
  hash       TEXT NOT NULL,
  PRIMARY KEY (session_id, pos)
);

-- A goal row is born proposed, and never replaced (a REPLACE would delete the old row without these rules seeing it).
CREATE TRIGGER IF NOT EXISTS mission_goals_born_proposed BEFORE INSERT ON mission_goals
WHEN NEW.state <> 'proposed' OR NEW.confirmed_by <> '' OR NEW.confirmed_at <> '' OR NEW.locked_at <> ''
BEGIN SELECT RAISE(ABORT, 'a mission goal starts as a proposal'); END;
CREATE TRIGGER IF NOT EXISTS mission_goals_no_replace BEFORE INSERT ON mission_goals
WHEN EXISTS (SELECT 1 FROM mission_goals g WHERE g.session_id = NEW.session_id)
BEGIN SELECT RAISE(ABORT, 'a mission goal exists: rows are never replaced'); END;

-- Forward only: a proposal may be revised (hash, proposer) and stays a proposal; it becomes confirmed only with a named
-- confirmer, a time and a rubric criterion, and nothing else changes; confirmed becomes locked, and nothing else changes;
-- locked is final.
CREATE TRIGGER IF NOT EXISTS mission_goals_forward_only BEFORE UPDATE ON mission_goals
WHEN NOT (
  NEW.session_id = OLD.session_id AND NEW.created_at = OLD.created_at AND (
    (OLD.state = 'proposed' AND NEW.state = 'proposed' AND NEW.confirmed_by = '' AND NEW.confirmed_at = '' AND NEW.locked_at = '')
    OR (OLD.state = 'proposed' AND NEW.state = 'confirmed' AND NEW.confirmed_by <> '' AND NEW.confirmed_at <> '' AND NEW.locked_at = ''
        AND NEW.criteria_hash = OLD.criteria_hash AND NEW.proposed_by = OLD.proposed_by
        AND EXISTS (SELECT 1 FROM mission_goal_criteria c WHERE c.session_id = OLD.session_id AND c.kind = 'rubric'))
    OR (OLD.state = 'confirmed' AND NEW.state = 'locked' AND NEW.locked_at <> ''
        AND NEW.criteria_hash = OLD.criteria_hash AND NEW.proposed_by = OLD.proposed_by
        AND NEW.confirmed_by = OLD.confirmed_by AND NEW.confirmed_at = OLD.confirmed_at)
  ))
BEGIN SELECT RAISE(ABORT, 'a mission goal only moves forward, and a confirmed goal does not change'); END;

-- A confirmed or locked goal is deleted only with its session.
CREATE TRIGGER IF NOT EXISTS mission_goals_final_no_delete BEFORE DELETE ON mission_goals
WHEN OLD.state IN ('confirmed', 'locked') AND EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = OLD.session_id)
BEGIN SELECT RAISE(ABORT, 'a confirmed mission goal is never deleted: a changed goal means a new mission'); END;

-- The criteria of a confirmed or locked goal are frozen: no insert, no edit, no delete (except with the session).
CREATE TRIGGER IF NOT EXISTS mission_goal_criteria_frozen_insert BEFORE INSERT ON mission_goal_criteria
WHEN EXISTS (SELECT 1 FROM mission_goals g WHERE g.session_id = NEW.session_id AND g.state <> 'proposed')
BEGIN SELECT RAISE(ABORT, 'the criteria of a confirmed mission goal never change'); END;
CREATE TRIGGER IF NOT EXISTS mission_goal_criteria_frozen_update BEFORE UPDATE ON mission_goal_criteria
WHEN EXISTS (SELECT 1 FROM mission_goals g WHERE g.session_id IN (OLD.session_id, NEW.session_id) AND g.state <> 'proposed')
BEGIN SELECT RAISE(ABORT, 'the criteria of a confirmed mission goal never change'); END;
CREATE TRIGGER IF NOT EXISTS mission_goal_criteria_frozen_delete BEFORE DELETE ON mission_goal_criteria
WHEN EXISTS (SELECT 1 FROM mission_goals g WHERE g.session_id = OLD.session_id AND g.state <> 'proposed')
 AND EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = OLD.session_id)
BEGIN SELECT RAISE(ABORT, 'the criteria of a confirmed mission goal never change'); END;
