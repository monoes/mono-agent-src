-- Open-ended ("doomsday") mode resource governor: slot accounting, the bounded durable queue and derived governor state.
-- Additive only; nothing here starts a run, and with no rows the mode is off. No table holds configuration: every cap is
-- read from the charter row of the run (written insert-only by migration 153), so no row of these tables can raise one.
-- The triggers are a backstop under the Go code: the caps, the queue bounds and the forward-only states hold even for a
-- writer that bypasses the governor package.

CREATE TABLE IF NOT EXISTS doomsday_slots (
    slot_id      INTEGER PRIMARY KEY AUTOINCREMENT,
    profile      TEXT    NOT NULL,
    generation   INTEGER NOT NULL,
    kind         TEXT    NOT NULL CHECK (kind IN ('session','process')),
    tree         TEXT    NOT NULL CHECK (tree <> ''),
    member_id    TEXT    NOT NULL CHECK (member_id <> ''),
    created_at   INTEGER NOT NULL,
    released_at  INTEGER
);
-- At most one live slot of a kind per member: a session holds one session slot and one process slot, a tool process one process slot.
CREATE UNIQUE INDEX IF NOT EXISTS idx_doomsday_slots_live_member ON doomsday_slots(profile, generation, member_id, kind) WHERE released_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_doomsday_slots_live_tree ON doomsday_slots(profile, generation, kind, tree) WHERE released_at IS NULL;

CREATE TABLE IF NOT EXISTS doomsday_queue (
    qid         INTEGER PRIMARY KEY AUTOINCREMENT,
    profile     TEXT    NOT NULL,
    generation  INTEGER NOT NULL,
    lead        TEXT    NOT NULL CHECK (lead <> ''),
    class       TEXT    NOT NULL CHECK (class IN ('boss','normal')),
    bytes       INTEGER NOT NULL CHECK (bytes > 0),
    payload     TEXT    NOT NULL,
    payload_sha BLOB    NOT NULL CHECK (length(payload_sha) = 32),
    state       TEXT    NOT NULL CHECK (state IN ('queued','admitted','cancelled')),
    enq_at      INTEGER NOT NULL,
    mac         BLOB    NOT NULL CHECK (length(mac) > 0)
);
CREATE INDEX IF NOT EXISTS idx_doomsday_queue_pending ON doomsday_queue(profile, generation, class, lead, qid) WHERE state = 'queued';

-- Derived state only (backoff windows, recovery counters). Losing it restarts the governor at the safe minimum.
CREATE TABLE IF NOT EXISTS doomsday_governor_state (
    profile     TEXT    NOT NULL,
    generation  INTEGER NOT NULL,
    key         TEXT    NOT NULL CHECK (length(key) BETWEEN 1 AND 64),
    value       TEXT    NOT NULL CHECK (length(value) <= 4096),
    PRIMARY KEY (profile, generation, key)
);

-- Slots: reserved only while the run is running; never deleted; a release is the only change; caps are the charter's.
CREATE TRIGGER IF NOT EXISTS doomsday_slots_need_running BEFORE INSERT ON doomsday_slots
WHEN NEW.released_at IS NOT NULL OR NOT EXISTS (SELECT 1 FROM doomsday_runs r WHERE r.profile = NEW.profile AND r.generation = NEW.generation AND r.state = 'running')
BEGIN SELECT RAISE(ABORT, 'doomsday slot needs a running run and is born live'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_slots_process_cap BEFORE INSERT ON doomsday_slots
WHEN NEW.kind = 'process' AND (SELECT count(*) FROM doomsday_slots s WHERE s.profile = NEW.profile AND s.generation = NEW.generation AND s.kind = 'process' AND s.released_at IS NULL)
     >= COALESCE((SELECT CAST(json_extract(c.body, '$.limits.max_concurrent') AS INTEGER) FROM doomsday_charters c WHERE c.profile = NEW.profile AND c.generation = NEW.generation), 0)
BEGIN SELECT RAISE(ABORT, 'doomsday max_concurrent reached'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_slots_session_cap BEFORE INSERT ON doomsday_slots
WHEN NEW.kind = 'session' AND (SELECT count(*) FROM doomsday_slots s WHERE s.profile = NEW.profile AND s.generation = NEW.generation AND s.kind = 'session' AND s.tree = NEW.tree AND s.released_at IS NULL)
     >= COALESCE((SELECT CAST(json_extract(c.body, '$.limits.live_per_tree') AS INTEGER) FROM doomsday_charters c WHERE c.profile = NEW.profile AND c.generation = NEW.generation), 0)
BEGIN SELECT RAISE(ABORT, 'doomsday live_per_tree reached'); END;
-- A session slot is never alone: the same member must hold a live process slot.
CREATE TRIGGER IF NOT EXISTS doomsday_slots_session_needs_process BEFORE INSERT ON doomsday_slots
WHEN NEW.kind = 'session' AND NOT EXISTS (SELECT 1 FROM doomsday_slots s WHERE s.profile = NEW.profile AND s.generation = NEW.generation AND s.member_id = NEW.member_id AND s.kind = 'process' AND s.released_at IS NULL)
BEGIN SELECT RAISE(ABORT, 'doomsday session slot needs the member process slot'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_slots_no_delete BEFORE DELETE ON doomsday_slots BEGIN SELECT RAISE(ABORT, 'doomsday_slots is never deleted'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_slots_release_only BEFORE UPDATE ON doomsday_slots
WHEN NOT (OLD.released_at IS NULL AND NEW.released_at IS NOT NULL AND NEW.slot_id = OLD.slot_id AND NEW.profile = OLD.profile AND NEW.generation = OLD.generation
          AND NEW.kind = OLD.kind AND NEW.tree = OLD.tree AND NEW.member_id = OLD.member_id AND NEW.created_at = OLD.created_at)
BEGIN SELECT RAISE(ABORT, 'doomsday slot can only be released'); END;

-- Queue: born queued, only while the run is running, inside the charter's row and byte bounds (defaults 500 rows, 8 MiB
-- when a charter names none); the byte count is the payload's own size. States move forward only, a terminal row drops its payload.
CREATE TRIGGER IF NOT EXISTS doomsday_queue_need_running BEFORE INSERT ON doomsday_queue
WHEN NEW.state <> 'queued' OR NOT EXISTS (SELECT 1 FROM doomsday_runs r WHERE r.profile = NEW.profile AND r.generation = NEW.generation AND r.state = 'running')
BEGIN SELECT RAISE(ABORT, 'doomsday queue row needs a running run and is born queued'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_queue_bytes_are_payload BEFORE INSERT ON doomsday_queue
WHEN NEW.bytes <> length(CAST(NEW.payload AS BLOB))
BEGIN SELECT RAISE(ABORT, 'doomsday queue bytes must be the payload size'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_queue_row_bound BEFORE INSERT ON doomsday_queue
WHEN (SELECT count(*) FROM doomsday_queue q WHERE q.profile = NEW.profile AND q.generation = NEW.generation AND q.state = 'queued')
     >= COALESCE((SELECT CAST(json_extract(c.body, '$.limits.queue_max_rows') AS INTEGER) FROM doomsday_charters c WHERE c.profile = NEW.profile AND c.generation = NEW.generation), 500)
BEGIN SELECT RAISE(ABORT, 'doomsday queue is full (rows)'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_queue_byte_bound BEFORE INSERT ON doomsday_queue
WHEN (SELECT COALESCE(sum(q.bytes), 0) FROM doomsday_queue q WHERE q.profile = NEW.profile AND q.generation = NEW.generation AND q.state = 'queued') + NEW.bytes
     > COALESCE((SELECT CAST(json_extract(c.body, '$.limits.queue_max_bytes') AS INTEGER) FROM doomsday_charters c WHERE c.profile = NEW.profile AND c.generation = NEW.generation), 8388608)
BEGIN SELECT RAISE(ABORT, 'doomsday queue is full (bytes)'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_queue_no_delete BEFORE DELETE ON doomsday_queue BEGIN SELECT RAISE(ABORT, 'doomsday_queue is never deleted'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_queue_forward_only BEFORE UPDATE ON doomsday_queue
WHEN NOT (OLD.state = 'queued' AND NEW.state IN ('admitted','cancelled') AND NEW.qid = OLD.qid AND NEW.profile = OLD.profile AND NEW.generation = OLD.generation
          AND NEW.lead = OLD.lead AND NEW.class = OLD.class AND NEW.bytes = OLD.bytes AND NEW.enq_at = OLD.enq_at AND NEW.payload_sha = OLD.payload_sha
          AND (NEW.payload = OLD.payload OR NEW.payload = ''))
BEGIN SELECT RAISE(ABORT, 'doomsday queue state moves forward once, from queued'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_queue_admit_needs_running BEFORE UPDATE ON doomsday_queue
WHEN NEW.state = 'admitted' AND NOT EXISTS (SELECT 1 FROM doomsday_runs r WHERE r.profile = NEW.profile AND r.generation = NEW.generation AND r.state = 'running')
BEGIN SELECT RAISE(ABORT, 'doomsday queue admission needs a running run'); END;
