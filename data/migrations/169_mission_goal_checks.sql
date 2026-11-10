-- 169_mission_goal_checks.sql
-- The check of a mission's locked goal by the host, and the person's answers to its rubric criteria (T6, S7b). Additive
-- only: three new tables, two new columns on mission_pursuit and one on mission_pursuit_state, and new triggers; nothing
-- existing is rebuilt, and with no rows it is inert. This migration is UNRELEASED (it is not in master and no database has
-- applied it), so it is edited in place; a released migration would get a new number instead.
--
-- mission_goal_checks         one row per check of a locked goal, born running and ended once:
--                               state running -> done | abandoned, forward only; outcome is written once when it is done
--                               (passed-machine failed error blocked stale cannot-check), and the person's answers move a
--                               passed-machine check, once, to accepted or rejected
-- mission_goal_check_results  what the host measured for each machine (non-rubric) criterion: insert-only, while the check runs
-- mission_goal_answers        the person's yes or no for a rubric criterion of a check that passed its machine criteria and
--                             its reviewer: insert-only, one per criterion, never replaced
--
-- Who writes: a check and its results by the host verifier alone (written_by); the answers and the accepted / rejected
-- outcome by the person alone, through the store (a person is named in answered_by). The database refuses what it can see:
-- a check only for a LOCKED goal whose hash is the check's, one running check per session, a result only with the hash and
-- the kind of the locked criterion, an answer only for a rubric criterion with its hash, accepted only when every rubric
-- criterion has a yes and none a no, rejected only when one has a no, and no delete except with the session. Not
-- tamper-evident (no MAC): a writer with the database file can write anything, as with mission_pursuit.
--
-- mission_pursuit.unconfined_checks_ok is the person's switch for letting the machine criteria run without the confinement
-- the verifier library requires (default OFF). It changes only together with the person who changed it (unconfined_by) and a
-- moved updated_at, so a change is also a person's action for the away limits; the store method that sets it is operator-only.
-- mission_pursuit_state.base_check_id is the highest check id when the person last acted (the checks after it are the away
-- checks the host's caps count). NOTHING writes or reads it yet: the host step (S7c1) sets it where it sets base_judge_id.
-- An abandoned or crashed check records cost 0 known 0 unless the host wrote the cap: the caps must count a check that is not
-- cost_known at the cap, never sum cost_micro alone.
CREATE TABLE IF NOT EXISTS mission_goal_checks (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id        INTEGER NOT NULL REFERENCES task_sessions(id) ON DELETE CASCADE,
  goal_hash         TEXT    NOT NULL,
  round             INTEGER NOT NULL DEFAULT 0 CHECK (round >= 0),
  state             TEXT    NOT NULL CHECK (state IN ('running', 'done', 'abandoned')),
  outcome           TEXT    NOT NULL DEFAULT '' CHECK (outcome IN ('', 'passed-machine', 'failed', 'error', 'blocked', 'stale', 'cannot-check', 'accepted', 'rejected')),
  reason            TEXT    NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
  subject_root      TEXT    NOT NULL DEFAULT '' CHECK (length(subject_root) <= 4096),
  subject_digest    TEXT    NOT NULL DEFAULT '' CHECK (length(subject_digest) <= 200),
  subject_files     INTEGER NOT NULL DEFAULT 0 CHECK (subject_files >= 0),
  containment       TEXT    NOT NULL DEFAULT '' CHECK (length(containment) <= 200),
  net               TEXT    NOT NULL DEFAULT '' CHECK (length(net) <= 200),
  reviewer_pin      TEXT    NOT NULL DEFAULT '' CHECK (length(reviewer_pin) <= 200),
  reviewer_observed TEXT    NOT NULL DEFAULT '' CHECK (length(reviewer_observed) <= 200),
  reviewer_verdict  TEXT    NOT NULL DEFAULT '' CHECK (reviewer_verdict IN ('', 'ok', 'block')),
  reviewer_reason   TEXT    NOT NULL DEFAULT '' CHECK (length(reviewer_reason) <= 200),
  results_digest    TEXT    NOT NULL DEFAULT '' CHECK (length(results_digest) <= 200),
  cost_micro        INTEGER NOT NULL DEFAULT 0 CHECK (cost_micro >= 0),
  cost_known        INTEGER NOT NULL DEFAULT 0 CHECK (cost_known IN (0, 1)),
  started_at        TEXT    NOT NULL,
  ended_at          TEXT    NOT NULL DEFAULT '',
  written_by        TEXT    NOT NULL CHECK (written_by = 'host-verifier')
);
CREATE INDEX IF NOT EXISTS idx_mission_goal_checks_session ON mission_goal_checks(session_id, id);

CREATE TABLE IF NOT EXISTS mission_goal_check_results (
  check_id          INTEGER NOT NULL REFERENCES mission_goal_checks(id) ON DELETE CASCADE,
  pos               INTEGER NOT NULL CHECK (pos BETWEEN 1 AND 16),
  criterion_hash    TEXT    NOT NULL,
  kind              TEXT    NOT NULL CHECK (kind IN ('command', 'file', 'output')),
  result            TEXT    NOT NULL CHECK (result IN ('pass', 'fail', 'error', 'timeout', 'oversize', 'tainted', 'not-run')),
  exit_code         INTEGER NOT NULL DEFAULT 0,
  stdout_digest     TEXT    NOT NULL DEFAULT '' CHECK (length(stdout_digest) <= 200),
  stderr_digest     TEXT    NOT NULL DEFAULT '' CHECK (length(stderr_digest) <= 200),
  stdout_excerpt    TEXT    NOT NULL DEFAULT '' CHECK (length(CAST(stdout_excerpt AS BLOB)) <= 1024),
  stderr_excerpt    TEXT    NOT NULL DEFAULT '' CHECK (length(CAST(stderr_excerpt AS BLOB)) <= 1024),
  detail            TEXT    NOT NULL DEFAULT '' CHECK (length(CAST(detail AS BLOB)) <= 400),
  net               TEXT    NOT NULL DEFAULT '' CHECK (length(net) <= 200),
  runs_subject_code INTEGER NOT NULL DEFAULT 0 CHECK (runs_subject_code IN (0, 1)),
  PRIMARY KEY (check_id, pos)
);

CREATE TABLE IF NOT EXISTS mission_goal_answers (
  check_id       INTEGER NOT NULL REFERENCES mission_goal_checks(id) ON DELETE CASCADE,
  pos            INTEGER NOT NULL CHECK (pos BETWEEN 1 AND 16),
  criterion_hash TEXT    NOT NULL,
  answer         TEXT    NOT NULL CHECK (answer IN ('yes', 'no')),
  note           TEXT    NOT NULL DEFAULT '' CHECK (length(note) <= 500),
  dossier_hash   TEXT    NOT NULL CHECK (dossier_hash <> ''),
  answered_by    TEXT    NOT NULL CHECK (answered_by <> ''), -- named (any non-empty text); that it is the person is the store's rule
  answered_at    TEXT    NOT NULL,
  PRIMARY KEY (check_id, pos)
);

-- ---- the checks ----
-- Never replaced (a REPLACE would delete the old row without these rules seeing it).
CREATE TRIGGER IF NOT EXISTS mission_goal_checks_no_replace BEFORE INSERT ON mission_goal_checks
WHEN EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.id = NEW.id)
BEGIN SELECT RAISE(ABORT, 'a goal check exists: rows are never replaced'); END;

-- Born running, not ended, with no outcome, for a LOCKED goal whose hash is the check's.
CREATE TRIGGER IF NOT EXISTS mission_goal_checks_born BEFORE INSERT ON mission_goal_checks
WHEN NEW.state <> 'running' OR NEW.outcome <> '' OR NEW.ended_at <> ''
  OR NOT EXISTS (SELECT 1 FROM mission_goals g WHERE g.session_id = NEW.session_id AND g.state = 'locked' AND g.criteria_hash = NEW.goal_hash)
BEGIN SELECT RAISE(ABORT, 'a goal check starts running, for a locked goal with the hash it was started with'); END;

-- One running check per session.
CREATE TRIGGER IF NOT EXISTS mission_goal_checks_one_running BEFORE INSERT ON mission_goal_checks
WHEN NEW.state = 'running' AND NOT EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.id = NEW.id)
  AND EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.session_id = NEW.session_id AND c.state = 'running')
BEGIN SELECT RAISE(ABORT, 'one check runs at a time for a mission: another is still running'); END;

-- A check's round is the host's own count: it is greater than every earlier round of the mission (so a round is never reused).
CREATE TRIGGER IF NOT EXISTS mission_goal_checks_round_grows BEFORE INSERT ON mission_goal_checks
WHEN NOT EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.id = NEW.id)
  AND NOT EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.session_id = NEW.session_id AND c.state = 'running')
  AND EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.session_id = NEW.session_id AND c.round >= NEW.round)
BEGIN SELECT RAISE(ABORT, 'a check round is greater than every earlier round of the mission'); END;

-- Forward only. What the check was started with never changes. A running check ends once, as done (with an outcome the
-- verifier can reach) or abandoned (with none); a done check that passed its machine criteria moves once to the person's
-- accepted or rejected and nothing else about it changes.
CREATE TRIGGER IF NOT EXISTS mission_goal_checks_forward_only BEFORE UPDATE ON mission_goal_checks
WHEN NOT (
  NEW.id = OLD.id AND NEW.session_id = OLD.session_id AND NEW.goal_hash = OLD.goal_hash AND NEW.round = OLD.round
  AND NEW.started_at = OLD.started_at AND NEW.written_by = OLD.written_by AND (
    (OLD.state = 'running' AND NEW.state = 'done' AND NEW.ended_at <> ''
      AND NEW.outcome IN ('passed-machine', 'failed', 'error', 'blocked', 'stale', 'cannot-check'))
    OR (OLD.state = 'running' AND NEW.state = 'abandoned' AND NEW.ended_at <> '' AND NEW.outcome = '')
    OR (OLD.state = 'done' AND NEW.state = 'done' AND OLD.outcome = 'passed-machine' AND NEW.outcome IN ('accepted', 'rejected')
      AND NEW.reason = OLD.reason AND NEW.subject_root = OLD.subject_root AND NEW.subject_digest = OLD.subject_digest
      AND NEW.subject_files = OLD.subject_files AND NEW.containment = OLD.containment AND NEW.net = OLD.net
      AND NEW.reviewer_pin = OLD.reviewer_pin AND NEW.reviewer_observed = OLD.reviewer_observed
      AND NEW.reviewer_verdict = OLD.reviewer_verdict AND NEW.reviewer_reason = OLD.reviewer_reason
      AND NEW.results_digest = OLD.results_digest AND NEW.cost_micro = OLD.cost_micro AND NEW.cost_known = OLD.cost_known
      AND NEW.ended_at = OLD.ended_at)
  ))
BEGIN SELECT RAISE(ABORT, 'a goal check only moves forward, and its outcome is written once'); END;

-- Accepted only when every rubric criterion of the goal has a yes for this check and none has a no; rejected only when
-- one has a no.
CREATE TRIGGER IF NOT EXISTS mission_goal_checks_accept BEFORE UPDATE ON mission_goal_checks
WHEN OLD.state = 'done' AND OLD.outcome = 'passed-machine' AND NEW.outcome = 'accepted'
  AND (EXISTS (SELECT 1 FROM mission_goal_answers a WHERE a.check_id = OLD.id AND a.answer = 'no')
    OR NOT EXISTS (SELECT 1 FROM mission_goal_criteria c WHERE c.session_id = OLD.session_id AND c.kind = 'rubric')
    OR EXISTS (SELECT 1 FROM mission_goal_criteria c WHERE c.session_id = OLD.session_id AND c.kind = 'rubric'
      AND NOT EXISTS (SELECT 1 FROM mission_goal_answers a WHERE a.check_id = OLD.id AND a.pos = c.pos AND a.criterion_hash = c.hash AND a.answer = 'yes')))
BEGIN SELECT RAISE(ABORT, 'a check is accepted only when every rubric criterion has a yes answer and none a no'); END;
CREATE TRIGGER IF NOT EXISTS mission_goal_checks_reject BEFORE UPDATE ON mission_goal_checks
WHEN OLD.state = 'done' AND OLD.outcome = 'passed-machine' AND NEW.outcome = 'rejected'
  AND NOT EXISTS (SELECT 1 FROM mission_goal_answers a WHERE a.check_id = OLD.id AND a.answer = 'no')
BEGIN SELECT RAISE(ABORT, 'a check is rejected only when a rubric criterion has a no answer'); END;

-- A check is deleted only with its session.
CREATE TRIGGER IF NOT EXISTS mission_goal_checks_no_delete BEFORE DELETE ON mission_goal_checks
WHEN EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = OLD.session_id)
BEGIN SELECT RAISE(ABORT, 'a goal check is never deleted'); END;

-- ---- the machine results ----
CREATE TRIGGER IF NOT EXISTS mission_goal_check_results_no_replace BEFORE INSERT ON mission_goal_check_results
WHEN EXISTS (SELECT 1 FROM mission_goal_check_results r WHERE r.check_id = NEW.check_id AND r.pos = NEW.pos)
BEGIN SELECT RAISE(ABORT, 'a check result exists: rows are never replaced'); END;
CREATE TRIGGER IF NOT EXISTS mission_goal_check_results_running BEFORE INSERT ON mission_goal_check_results
WHEN NOT EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.id = NEW.check_id AND c.state = 'running')
BEGIN SELECT RAISE(ABORT, 'a result is added only to a running check'); END;
-- A rubric criterion has no machine result: the person answers it.
CREATE TRIGGER IF NOT EXISTS mission_goal_check_results_not_rubric BEFORE INSERT ON mission_goal_check_results
WHEN EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.id = NEW.check_id AND c.state = 'running')
  AND (NEW.kind = 'rubric' OR EXISTS (SELECT 1 FROM mission_goal_checks c JOIN mission_goal_criteria g ON g.session_id = c.session_id
    WHERE c.id = NEW.check_id AND g.pos = NEW.pos AND g.kind = 'rubric'))
BEGIN SELECT RAISE(ABORT, 'a rubric criterion has no machine result: the person answers it'); END;
-- A result carries the hash and the kind of the locked criterion at its position.
CREATE TRIGGER IF NOT EXISTS mission_goal_check_results_locked BEFORE INSERT ON mission_goal_check_results
WHEN EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.id = NEW.check_id AND c.state = 'running')
  AND NEW.kind <> 'rubric'
  AND NOT EXISTS (SELECT 1 FROM mission_goal_checks c JOIN mission_goal_criteria g ON g.session_id = c.session_id
    WHERE c.id = NEW.check_id AND g.pos = NEW.pos AND g.kind = 'rubric')
  AND NOT EXISTS (SELECT 1 FROM mission_goal_checks c JOIN mission_goal_criteria g ON g.session_id = c.session_id
    WHERE c.id = NEW.check_id AND g.pos = NEW.pos AND g.hash = NEW.criterion_hash AND g.kind = NEW.kind)
BEGIN SELECT RAISE(ABORT, 'a result is for a locked criterion, with its hash and kind'); END;
CREATE TRIGGER IF NOT EXISTS mission_goal_check_results_no_update BEFORE UPDATE ON mission_goal_check_results
BEGIN SELECT RAISE(ABORT, 'a check result is insert-only and never changes'); END;
CREATE TRIGGER IF NOT EXISTS mission_goal_check_results_no_delete BEFORE DELETE ON mission_goal_check_results
WHEN EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.id = OLD.check_id)
BEGIN SELECT RAISE(ABORT, 'a check result is never deleted'); END;

-- ---- the person's answers ----
CREATE TRIGGER IF NOT EXISTS mission_goal_answers_no_replace BEFORE INSERT ON mission_goal_answers
WHEN EXISTS (SELECT 1 FROM mission_goal_answers a WHERE a.check_id = NEW.check_id AND a.pos = NEW.pos)
BEGIN SELECT RAISE(ABORT, 'an answer exists: rows are never replaced'); END;
-- Only a check that passed its machine criteria and whose reviewer said ok takes an answer.
CREATE TRIGGER IF NOT EXISTS mission_goal_answers_passed BEFORE INSERT ON mission_goal_answers
WHEN NOT EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.id = NEW.check_id AND c.state = 'done' AND c.outcome = 'passed-machine' AND c.reviewer_verdict = 'ok')
BEGIN SELECT RAISE(ABORT, 'an answer is for a check that passed its machine criteria and its reviewer'); END;
-- ... for a rubric criterion of the goal, with the hash of the locked row.
CREATE TRIGGER IF NOT EXISTS mission_goal_answers_rubric BEFORE INSERT ON mission_goal_answers
WHEN EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.id = NEW.check_id AND c.state = 'done' AND c.outcome = 'passed-machine' AND c.reviewer_verdict = 'ok')
  AND NOT EXISTS (SELECT 1 FROM mission_goal_checks c JOIN mission_goal_criteria g ON g.session_id = c.session_id
    WHERE c.id = NEW.check_id AND g.pos = NEW.pos AND g.kind = 'rubric' AND g.hash = NEW.criterion_hash)
BEGIN SELECT RAISE(ABORT, 'an answer is for a rubric criterion of the goal, with its hash'); END;
CREATE TRIGGER IF NOT EXISTS mission_goal_answers_no_update BEFORE UPDATE ON mission_goal_answers
BEGIN SELECT RAISE(ABORT, 'an answer is insert-only and never changes'); END;
CREATE TRIGGER IF NOT EXISTS mission_goal_answers_no_delete BEFORE DELETE ON mission_goal_answers
WHEN EXISTS (SELECT 1 FROM mission_goal_checks c WHERE c.id = OLD.check_id)
BEGIN SELECT RAISE(ABORT, 'an answer is never deleted'); END;

-- ---- the person's switch for unconfined checks, and the meter's base ----
ALTER TABLE mission_pursuit ADD COLUMN unconfined_checks_ok INTEGER NOT NULL DEFAULT 0 CHECK (unconfined_checks_ok IN (0, 1));
ALTER TABLE mission_pursuit ADD COLUMN unconfined_by TEXT NOT NULL DEFAULT '';
ALTER TABLE mission_pursuit_state ADD COLUMN base_check_id INTEGER NOT NULL DEFAULT 0 CHECK (base_check_id >= 0);

-- A pursuit is born with the switch off.
CREATE TRIGGER IF NOT EXISTS mission_pursuit_unconfined_born BEFORE INSERT ON mission_pursuit
WHEN NEW.unconfined_checks_ok <> 0 OR NEW.unconfined_by <> ''
BEGIN SELECT RAISE(ABORT, 'a pursuit starts with the switch for unconfined checks off'); END;
-- The switch moves only on a started pursuit, with who moved it named (any non-empty text: that it is the person is the store's
-- rule, since the database cannot tell) and updated_at strictly later (so it is also the person's action for the away limits);
-- the name recorded changes only with it.
CREATE TRIGGER IF NOT EXISTS mission_pursuit_unconfined_moves BEFORE UPDATE ON mission_pursuit
WHEN (NEW.unconfined_checks_ok <> OLD.unconfined_checks_ok AND (NEW.unconfined_by = '' OR NEW.updated_at <= OLD.updated_at OR OLD.enabled = 0))
  OR (NEW.unconfined_checks_ok = OLD.unconfined_checks_ok AND NEW.unconfined_by <> OLD.unconfined_by)
BEGIN SELECT RAISE(ABORT, 'the switch for unconfined checks changes only with the person named and the date moved forward'); END;
