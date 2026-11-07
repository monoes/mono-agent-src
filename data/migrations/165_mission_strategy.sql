-- 165_mission_strategy.sql
-- The strategy layer of a mission's pursuit (T6): directions, epochs, judgments and the novelty ledger, keyed by the mission
-- task session. Additive only: four new tables, nothing existing is rebuilt, and with no rows it is inert. This is the
-- structure of migration 156 (the open-ended mode's strategy tables) without its run key, its MAC and its audit chain: the
-- rows are NOT tamper-evident, only guarded by the rules below, so nothing may treat them as evidence of what happened.
-- Rows are inserted, and rows move forward, only while the session is 'running'; the one move left after that is closing an
-- unfinished epoch as abandoned. A row is never replaced: an INSERT (of any kind, REPLACE and UPSERT included) that collides
-- with an existing key is refused before the conflict rule can delete the old row. Deleting the session deletes its rows with
-- it (the cascade is the one delete these rules let through: by then the session row is already gone).
CREATE TABLE IF NOT EXISTS mission_strategy_directions (
    session_id       INTEGER NOT NULL REFERENCES task_sessions(id) ON DELETE CASCADE,
    direction_id     TEXT    NOT NULL,
    key_hash         BLOB    NOT NULL,
    title            TEXT    NOT NULL,
    state            TEXT    NOT NULL CHECK (state IN ('proposed','active','stalled','finished','pruned')),
    proposer_session TEXT    NOT NULL DEFAULT '',
    lead_session     TEXT    NOT NULL DEFAULT '',
    created_epoch    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (session_id, direction_id),
    UNIQUE (session_id, key_hash)
);

CREATE TABLE IF NOT EXISTS mission_strategy_epochs (
    session_id     INTEGER NOT NULL REFERENCES task_sessions(id) ON DELETE CASCADE,
    epoch_seq      INTEGER NOT NULL CHECK (epoch_seq >= 1),
    trigger        TEXT    NOT NULL CHECK (trigger IN ('timer','direction-finished','direction-stalled','budget-share','new-evidence','finish-parked')),
    started_at     INTEGER NOT NULL,
    judgment_hash  BLOB    NOT NULL DEFAULT x'',
    model_pin      TEXT    NOT NULL DEFAULT '',
    state          TEXT    NOT NULL CHECK (state IN ('open','judged','applied','bad-judgment','abandoned')),
    PRIMARY KEY (session_id, epoch_seq)
);

CREATE TABLE IF NOT EXISTS mission_strategy_judgments (
    session_id  INTEGER NOT NULL REFERENCES task_sessions(id) ON DELETE CASCADE,
    epoch_seq   INTEGER NOT NULL CHECK (epoch_seq >= 1),
    body        TEXT    NOT NULL,
    body_hash   BLOB    NOT NULL,
    PRIMARY KEY (session_id, epoch_seq)
);

CREATE TABLE IF NOT EXISTS mission_strategy_novelty (
    novelty_id    INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id    INTEGER NOT NULL REFERENCES task_sessions(id) ON DELETE CASCADE,
    epoch_seq     INTEGER NOT NULL DEFAULT 0,
    key_hash      BLOB    NOT NULL,
    direction_id  TEXT    NOT NULL DEFAULT '',
    matched_id    TEXT    NOT NULL DEFAULT '',
    outcome       TEXT    NOT NULL CHECK (outcome IN ('accepted','accepted-unchecked','refused-ledger','refused-direction','refused-corpus'))
);
CREATE INDEX IF NOT EXISTS idx_mission_strategy_novelty_key ON mission_strategy_novelty(session_id, key_hash);

-- Nothing is written for a session that is not running (a pause, a stop or an end closes every insert path at the database).
CREATE TRIGGER IF NOT EXISTS mission_strategy_directions_running BEFORE INSERT ON mission_strategy_directions
WHEN NOT EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = NEW.session_id AND s.state = 'running')
BEGIN SELECT RAISE(ABORT, 'mission strategy rows are written only for a running session'); END;
CREATE TRIGGER IF NOT EXISTS mission_strategy_epochs_running BEFORE INSERT ON mission_strategy_epochs
WHEN NOT EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = NEW.session_id AND s.state = 'running')
BEGIN SELECT RAISE(ABORT, 'mission strategy rows are written only for a running session'); END;
CREATE TRIGGER IF NOT EXISTS mission_strategy_judgments_running BEFORE INSERT ON mission_strategy_judgments
WHEN NOT EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = NEW.session_id AND s.state = 'running')
BEGIN SELECT RAISE(ABORT, 'mission strategy rows are written only for a running session'); END;
CREATE TRIGGER IF NOT EXISTS mission_strategy_novelty_running BEFORE INSERT ON mission_strategy_novelty
WHEN NOT EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = NEW.session_id AND s.state = 'running')
BEGIN SELECT RAISE(ABORT, 'mission strategy rows are written only for a running session'); END;

-- No REPLACE: INSERT OR REPLACE deletes the colliding row without firing the delete rules, which would walk a row back.
-- A BEFORE INSERT trigger runs ahead of every conflict rule, so an insert whose key exists is refused outright.
-- (A direction also collides on its session-unique key hash.)
CREATE TRIGGER IF NOT EXISTS mission_strategy_directions_no_replace BEFORE INSERT ON mission_strategy_directions
WHEN EXISTS (SELECT 1 FROM mission_strategy_directions d WHERE d.session_id = NEW.session_id AND (d.direction_id = NEW.direction_id OR d.key_hash = NEW.key_hash))
BEGIN SELECT RAISE(ABORT, 'mission strategy row exists: rows are never replaced'); END;
CREATE TRIGGER IF NOT EXISTS mission_strategy_epochs_no_replace BEFORE INSERT ON mission_strategy_epochs
WHEN EXISTS (SELECT 1 FROM mission_strategy_epochs e WHERE e.session_id = NEW.session_id AND e.epoch_seq = NEW.epoch_seq)
BEGIN SELECT RAISE(ABORT, 'mission strategy row exists: rows are never replaced'); END;
CREATE TRIGGER IF NOT EXISTS mission_strategy_judgments_no_replace BEFORE INSERT ON mission_strategy_judgments
WHEN EXISTS (SELECT 1 FROM mission_strategy_judgments j WHERE j.session_id = NEW.session_id AND j.epoch_seq = NEW.epoch_seq)
BEGIN SELECT RAISE(ABORT, 'mission strategy row exists: rows are never replaced'); END;
CREATE TRIGGER IF NOT EXISTS mission_strategy_novelty_no_replace BEFORE INSERT ON mission_strategy_novelty
WHEN NEW.novelty_id IS NOT NULL AND EXISTS (SELECT 1 FROM mission_strategy_novelty n WHERE n.novelty_id = NEW.novelty_id)
BEGIN SELECT RAISE(ABORT, 'mission strategy row exists: rows are never replaced'); END;

-- A judgment belongs to an open epoch of the same session (one per epoch by the primary key).
CREATE TRIGGER IF NOT EXISTS mission_strategy_judgments_needs_open_epoch BEFORE INSERT ON mission_strategy_judgments
WHEN NOT EXISTS (SELECT 1 FROM mission_strategy_epochs e WHERE e.session_id = NEW.session_id AND e.epoch_seq = NEW.epoch_seq AND e.state = 'open')
BEGIN SELECT RAISE(ABORT, 'mission strategy judgment needs an open epoch'); END;

-- Insert-only: judgments and the novelty ledger (deleted only with the session).
CREATE TRIGGER IF NOT EXISTS mission_strategy_judgments_no_update BEFORE UPDATE ON mission_strategy_judgments BEGIN SELECT RAISE(ABORT, 'mission_strategy_judgments is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS mission_strategy_judgments_no_delete BEFORE DELETE ON mission_strategy_judgments
WHEN EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = OLD.session_id)
BEGIN SELECT RAISE(ABORT, 'mission_strategy_judgments is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS mission_strategy_novelty_no_update BEFORE UPDATE ON mission_strategy_novelty BEGIN SELECT RAISE(ABORT, 'mission_strategy_novelty is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS mission_strategy_novelty_no_delete BEFORE DELETE ON mission_strategy_novelty
WHEN EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = OLD.session_id)
BEGIN SELECT RAISE(ABORT, 'mission_strategy_novelty is insert-only'); END;

-- Directions are deleted only with the session; only the state and the one-time lead binding change, and the state moves
-- forward (proposed -> active|pruned, active -> stalled|finished|pruned, stalled -> finished|pruned), only while running.
CREATE TRIGGER IF NOT EXISTS mission_strategy_directions_no_delete BEFORE DELETE ON mission_strategy_directions
WHEN EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = OLD.session_id)
BEGIN SELECT RAISE(ABORT, 'mission_strategy_directions is never deleted'); END;
CREATE TRIGGER IF NOT EXISTS mission_strategy_directions_forward_only BEFORE UPDATE ON mission_strategy_directions
WHEN NOT (
    EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = OLD.session_id AND s.state = 'running') AND
    NEW.session_id = OLD.session_id AND NEW.direction_id = OLD.direction_id AND
    NEW.key_hash = OLD.key_hash AND NEW.title = OLD.title AND NEW.proposer_session = OLD.proposer_session AND
    NEW.created_epoch = OLD.created_epoch AND (OLD.lead_session = '' OR NEW.lead_session = OLD.lead_session) AND
    ((OLD.state = 'proposed' AND NEW.state IN ('active','pruned')) OR
     (OLD.state = 'active' AND NEW.state IN ('stalled','finished','pruned')) OR
     (OLD.state = 'stalled' AND NEW.state IN ('finished','pruned')))
)
BEGIN SELECT RAISE(ABORT, 'mission strategy direction moves forward only'); END;

-- Epochs are deleted only with the session; only the state and the one-time judgment hash change, state forward
-- (open -> judged|bad-judgment|abandoned, judged -> applied|abandoned). While the session is running an epoch moves
-- forward; once it is not running the one move left is closing an unfinished epoch as abandoned.
CREATE TRIGGER IF NOT EXISTS mission_strategy_epochs_no_delete BEFORE DELETE ON mission_strategy_epochs
WHEN EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = OLD.session_id)
BEGIN SELECT RAISE(ABORT, 'mission_strategy_epochs is never deleted'); END;
CREATE TRIGGER IF NOT EXISTS mission_strategy_epochs_forward_only BEFORE UPDATE ON mission_strategy_epochs
WHEN NOT (
    NEW.session_id = OLD.session_id AND NEW.epoch_seq = OLD.epoch_seq AND
    NEW.trigger = OLD.trigger AND NEW.started_at = OLD.started_at AND NEW.model_pin = OLD.model_pin AND
    (OLD.judgment_hash = x'' OR NEW.judgment_hash = OLD.judgment_hash) AND
    ((OLD.state = 'open' AND NEW.state IN ('judged','bad-judgment','abandoned')) OR
     (OLD.state = 'judged' AND NEW.state IN ('applied','abandoned'))) AND
    (NEW.state = 'abandoned' OR EXISTS (SELECT 1 FROM task_sessions s WHERE s.id = OLD.session_id AND s.state = 'running'))
)
BEGIN SELECT RAISE(ABORT, 'mission strategy epoch moves forward only'); END;
