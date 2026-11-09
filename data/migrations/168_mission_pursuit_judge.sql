-- 168_mission_pursuit_judge.sql
-- What the judge of a mission's pursuit costs, and the rule that a pursuit starts only with a rubric criterion (T6, S5a).
-- Additive only: one new table, new columns with defaults on mission_pursuit_state, and new triggers; nothing existing is
-- rebuilt, and with no rows it is inert. Written by the host loop alone, from host measurements; no agent and no verb writes it.
--
-- mission_pursuit_spend: the judge ledger. One row per judge call, written BEFORE the call at the per-call cap (settled = 0,
-- known = 0: a call whose process dies, or whose runtime reports no cost, stays charged at the cap and is never free), and
-- settled once, afterwards, with the cost the runtime reported (settled = 1). Rows are never deleted (except with their
-- session) and a settled row never changes. The away rule adds the rows after base_judge_id to the metered spend, and the
-- host-wide hourly cap on judge calls counts them.
--
-- mission_pursuit_state gains what the status shows and the stops read:
--   bad_rounds       consecutive judge rounds that failed (invalid, oversized, model failure) since the last good one;
--                    reaching the limit makes the pursuit wait for the person ('judge-failing'); the person acting resets it
--   base_judge_id    the highest ledger id when the person last acted: the judge spend after it is the away judge spend
--   base_lead_micro  the lead's and its helpers' turn-journal spend (micro-USD) when the person last acted
--   judge_micro / lead_micro / judge_calls   the away judge spend, the away lead-turn spend and the judge calls, as shown
--   hour_judges      judge calls of ALL missions in the last hour, as shown
--   last_round       how the latest judge round ended ('' none, 'applied', 'invalid', 'model-failed', 'input')
--
-- A reservation row is born unsettled and unknown, at no less than the per-call cap (500000 micro-USD), and an id is never
-- replaced (an INSERT OR REPLACE would otherwise swap a reserved row for a free settled one, with no error). Like 165 and 166,
-- the ledger refuses a replace at the insert. This migration is UNRELEASED (it is not in master and no database has applied it),
-- so it is edited in place; a released migration would get a new number instead.
--
-- The pursuit may start only with a rubric criterion in its goal (the person is always the final judge). The goal rules
-- (164) already refuse to confirm a goal without one; these triggers say it again at the pursuit itself, so a pursuit row
-- cannot be enabled for a goal that has none, whatever path writes it.
CREATE TABLE IF NOT EXISTS mission_pursuit_spend (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id INTEGER NOT NULL REFERENCES task_sessions(id) ON DELETE CASCADE,
  kind       TEXT    NOT NULL CHECK (kind IN ('judge')),
  at         TEXT    NOT NULL,
  micro      INTEGER NOT NULL CHECK (micro >= 0),
  known      INTEGER NOT NULL DEFAULT 0 CHECK (known IN (0, 1)),
  settled    INTEGER NOT NULL DEFAULT 0 CHECK (settled IN (0, 1))
);
CREATE INDEX IF NOT EXISTS idx_mission_pursuit_spend_session ON mission_pursuit_spend(session_id, id);
CREATE INDEX IF NOT EXISTS idx_mission_pursuit_spend_at ON mission_pursuit_spend(at);

CREATE TRIGGER IF NOT EXISTS mission_pursuit_spend_no_replace BEFORE INSERT ON mission_pursuit_spend
WHEN EXISTS (SELECT 1 FROM mission_pursuit_spend x WHERE x.id = NEW.id)
BEGIN SELECT RAISE(ABORT, 'a judge spend row exists: rows are never replaced'); END;
CREATE TRIGGER IF NOT EXISTS mission_pursuit_spend_born_reserved BEFORE INSERT ON mission_pursuit_spend
WHEN NEW.settled <> 0 OR NEW.known <> 0 OR NEW.micro < 500000
BEGIN SELECT RAISE(ABORT, 'a judge spend row is born unsettled, at no less than the per-call cap'); END;
CREATE TRIGGER IF NOT EXISTS mission_pursuit_spend_settle_once BEFORE UPDATE ON mission_pursuit_spend
WHEN NOT (OLD.settled = 0 AND NEW.settled = 1 AND NEW.id = OLD.id AND NEW.session_id = OLD.session_id AND NEW.kind = OLD.kind AND NEW.at = OLD.at)
BEGIN SELECT RAISE(ABORT, 'a judge spend row is settled once and never changes'); END;
CREATE TRIGGER IF NOT EXISTS mission_pursuit_spend_no_delete BEFORE DELETE ON mission_pursuit_spend
WHEN EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = OLD.session_id)
BEGIN SELECT RAISE(ABORT, 'a judge spend row is never deleted'); END;

ALTER TABLE mission_pursuit_state ADD COLUMN bad_rounds INTEGER NOT NULL DEFAULT 0 CHECK (bad_rounds >= 0);
ALTER TABLE mission_pursuit_state ADD COLUMN base_judge_id INTEGER NOT NULL DEFAULT 0 CHECK (base_judge_id >= 0);
ALTER TABLE mission_pursuit_state ADD COLUMN base_lead_micro INTEGER NOT NULL DEFAULT 0 CHECK (base_lead_micro >= 0);
ALTER TABLE mission_pursuit_state ADD COLUMN judge_micro INTEGER NOT NULL DEFAULT 0 CHECK (judge_micro >= 0);
ALTER TABLE mission_pursuit_state ADD COLUMN lead_micro INTEGER NOT NULL DEFAULT 0 CHECK (lead_micro >= 0);
ALTER TABLE mission_pursuit_state ADD COLUMN judge_calls INTEGER NOT NULL DEFAULT 0 CHECK (judge_calls >= 0);
ALTER TABLE mission_pursuit_state ADD COLUMN hour_judges INTEGER NOT NULL DEFAULT 0 CHECK (hour_judges >= 0);
ALTER TABLE mission_pursuit_state ADD COLUMN last_round TEXT NOT NULL DEFAULT '';

CREATE TRIGGER IF NOT EXISTS mission_pursuit_needs_rubric_insert BEFORE INSERT ON mission_pursuit
WHEN NEW.enabled = 1 AND NOT EXISTS (SELECT 1 FROM mission_goal_criteria c WHERE c.session_id = NEW.session_id AND c.kind = 'rubric')
BEGIN SELECT RAISE(ABORT, 'a mission pursuit starts only for a goal with a rubric criterion'); END;
CREATE TRIGGER IF NOT EXISTS mission_pursuit_needs_rubric_update BEFORE UPDATE ON mission_pursuit
WHEN NEW.enabled = 1 AND OLD.enabled = 0 AND NOT EXISTS (SELECT 1 FROM mission_goal_criteria c WHERE c.session_id = OLD.session_id AND c.kind = 'rubric')
BEGIN SELECT RAISE(ABORT, 'a mission pursuit starts only for a goal with a rubric criterion'); END;
