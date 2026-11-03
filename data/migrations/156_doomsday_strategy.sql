-- Strategy layer of the open-ended ("doomsday") mode: directions, epochs, judgments, the novelty ledger. Additive only:
-- nothing here starts a run, touches the charter or run tables, or enables the mode; with no rows it is inert.
-- Every table is keyed (profile, generation, ...) so a new run never sees an old run's rows. Rows are inserted only
-- while the run is 'running', so after a Stop nothing new can be written whatever the code above does.

CREATE TABLE IF NOT EXISTS doomsday_strategy_directions (
    profile          TEXT    NOT NULL,
    generation       INTEGER NOT NULL CHECK (generation >= 1),
    direction_id     TEXT    NOT NULL,
    key_hash         BLOB    NOT NULL,
    title            TEXT    NOT NULL,
    state            TEXT    NOT NULL CHECK (state IN ('proposed','active','stalled','finished','pruned')),
    proposer_session TEXT    NOT NULL DEFAULT '',
    session_id       TEXT    NOT NULL DEFAULT '',
    created_epoch    INTEGER NOT NULL DEFAULT 0,
    mac              BLOB    NOT NULL,
    PRIMARY KEY (profile, generation, direction_id),
    UNIQUE (profile, generation, key_hash)
);

CREATE TABLE IF NOT EXISTS doomsday_strategy_epochs (
    profile        TEXT    NOT NULL,
    generation     INTEGER NOT NULL CHECK (generation >= 1),
    epoch_seq      INTEGER NOT NULL CHECK (epoch_seq >= 1),
    trigger        TEXT    NOT NULL CHECK (trigger IN ('timer','direction-finished','direction-stalled','budget-share','new-evidence','finish-parked')),
    started_at     INTEGER NOT NULL,
    judgment_hash  BLOB    NOT NULL DEFAULT x'',
    model_pin      TEXT    NOT NULL DEFAULT '',
    state          TEXT    NOT NULL CHECK (state IN ('open','judged','applied','bad-judgment','abandoned')),
    mac            BLOB    NOT NULL,
    PRIMARY KEY (profile, generation, epoch_seq)
);

CREATE TABLE IF NOT EXISTS doomsday_strategy_judgments (
    profile     TEXT    NOT NULL,
    generation  INTEGER NOT NULL CHECK (generation >= 1),
    epoch_seq   INTEGER NOT NULL CHECK (epoch_seq >= 1),
    body        TEXT    NOT NULL,
    body_hash   BLOB    NOT NULL,
    mac         BLOB    NOT NULL,
    PRIMARY KEY (profile, generation, epoch_seq)
);

CREATE TABLE IF NOT EXISTS doomsday_strategy_novelty (
    novelty_id    INTEGER PRIMARY KEY AUTOINCREMENT,
    profile       TEXT    NOT NULL,
    generation    INTEGER NOT NULL CHECK (generation >= 1),
    epoch_seq     INTEGER NOT NULL DEFAULT 0,
    key_hash      BLOB    NOT NULL,
    direction_id  TEXT    NOT NULL DEFAULT '',
    matched_id    TEXT    NOT NULL DEFAULT '',
    outcome       TEXT    NOT NULL CHECK (outcome IN ('accepted','accepted-unchecked','refused-ledger','refused-direction','refused-corpus')),
    mac           BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_doomsday_strategy_novelty_key ON doomsday_strategy_novelty(profile, generation, key_hash);

-- Nothing is written for a run that is not running (a Stop, any cause, closes every insert path at the database).
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_directions_run_open BEFORE INSERT ON doomsday_strategy_directions
WHEN NOT EXISTS (SELECT 1 FROM doomsday_runs r WHERE r.profile = NEW.profile AND r.generation = NEW.generation AND r.state = 'running')
BEGIN SELECT RAISE(ABORT, 'doomsday strategy rows are written only for a running run'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_epochs_run_open BEFORE INSERT ON doomsday_strategy_epochs
WHEN NOT EXISTS (SELECT 1 FROM doomsday_runs r WHERE r.profile = NEW.profile AND r.generation = NEW.generation AND r.state = 'running')
BEGIN SELECT RAISE(ABORT, 'doomsday strategy rows are written only for a running run'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_judgments_run_open BEFORE INSERT ON doomsday_strategy_judgments
WHEN NOT EXISTS (SELECT 1 FROM doomsday_runs r WHERE r.profile = NEW.profile AND r.generation = NEW.generation AND r.state = 'running')
BEGIN SELECT RAISE(ABORT, 'doomsday strategy rows are written only for a running run'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_novelty_run_open BEFORE INSERT ON doomsday_strategy_novelty
WHEN NOT EXISTS (SELECT 1 FROM doomsday_runs r WHERE r.profile = NEW.profile AND r.generation = NEW.generation AND r.state = 'running')
BEGIN SELECT RAISE(ABORT, 'doomsday strategy rows are written only for a running run'); END;

-- A judgment belongs to an open epoch of the same run (one per epoch by the primary key).
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_judgments_needs_open_epoch BEFORE INSERT ON doomsday_strategy_judgments
WHEN NOT EXISTS (SELECT 1 FROM doomsday_strategy_epochs e WHERE e.profile = NEW.profile AND e.generation = NEW.generation AND e.epoch_seq = NEW.epoch_seq AND e.state = 'open')
BEGIN SELECT RAISE(ABORT, 'doomsday strategy judgment needs an open epoch'); END;

-- Insert-only: judgments and the novelty ledger.
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_judgments_no_update BEFORE UPDATE ON doomsday_strategy_judgments BEGIN SELECT RAISE(ABORT, 'doomsday_strategy_judgments is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_judgments_no_delete BEFORE DELETE ON doomsday_strategy_judgments BEGIN SELECT RAISE(ABORT, 'doomsday_strategy_judgments is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_novelty_no_update BEFORE UPDATE ON doomsday_strategy_novelty BEGIN SELECT RAISE(ABORT, 'doomsday_strategy_novelty is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_novelty_no_delete BEFORE DELETE ON doomsday_strategy_novelty BEGIN SELECT RAISE(ABORT, 'doomsday_strategy_novelty is insert-only'); END;

-- Directions are never deleted; only the state, the one-time session binding and the MAC change, and the state moves
-- forward (proposed -> active|pruned, active -> stalled|finished|pruned, stalled -> finished|pruned).
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_directions_no_delete BEFORE DELETE ON doomsday_strategy_directions BEGIN SELECT RAISE(ABORT, 'doomsday_strategy_directions is never deleted'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_directions_forward_only BEFORE UPDATE ON doomsday_strategy_directions
WHEN NOT (
    NEW.profile = OLD.profile AND NEW.generation = OLD.generation AND NEW.direction_id = OLD.direction_id AND
    NEW.key_hash = OLD.key_hash AND NEW.title = OLD.title AND NEW.proposer_session = OLD.proposer_session AND
    NEW.created_epoch = OLD.created_epoch AND (OLD.session_id = '' OR NEW.session_id = OLD.session_id) AND
    ((OLD.state = 'proposed' AND NEW.state IN ('active','pruned')) OR
     (OLD.state = 'active' AND NEW.state IN ('stalled','finished','pruned')) OR
     (OLD.state = 'stalled' AND NEW.state IN ('finished','pruned')))
)
BEGIN SELECT RAISE(ABORT, 'doomsday strategy direction moves forward only'); END;

-- Epochs are never deleted; only the state, the one-time judgment hash and the MAC change, state forward
-- (open -> judged|bad-judgment|abandoned, judged -> applied|abandoned).
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_epochs_no_delete BEFORE DELETE ON doomsday_strategy_epochs BEGIN SELECT RAISE(ABORT, 'doomsday_strategy_epochs is never deleted'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_strategy_epochs_forward_only BEFORE UPDATE ON doomsday_strategy_epochs
WHEN NOT (
    NEW.profile = OLD.profile AND NEW.generation = OLD.generation AND NEW.epoch_seq = OLD.epoch_seq AND
    NEW.trigger = OLD.trigger AND NEW.started_at = OLD.started_at AND NEW.model_pin = OLD.model_pin AND
    (OLD.judgment_hash = x'' OR NEW.judgment_hash = OLD.judgment_hash) AND
    ((OLD.state = 'open' AND NEW.state IN ('judged','bad-judgment','abandoned')) OR
     (OLD.state = 'judged' AND NEW.state IN ('applied','abandoned')))
)
BEGIN SELECT RAISE(ABORT, 'doomsday strategy epoch moves forward only'); END;
