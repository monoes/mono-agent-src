-- Open-ended ("doomsday") mode: the record that a profile has ever opened a run. Additive only; with no rows the mode was never run
-- on the install. One row per profile, written in the transaction that enters the profile's first run, never changed, never removed
-- and never replaced (a BEFORE INSERT trigger runs ahead of every conflict rule, so INSERT OR REPLACE is refused too). It
-- outlives the run's state and, unlike the run directory, does not depend on files a same-user process can delete.

CREATE TABLE IF NOT EXISTS doomsday_run_seen (
    profile           TEXT    NOT NULL PRIMARY KEY,
    first_generation  INTEGER NOT NULL CHECK (first_generation >= 1),
    first_at          INTEGER NOT NULL
);

-- Installs that opened a run before this migration: their runs are never deleted, so the table is filled from them.
INSERT INTO doomsday_run_seen(profile, first_generation, first_at)
SELECT r.profile, MIN(r.generation), 0 FROM doomsday_runs r
WHERE NOT EXISTS (SELECT 1 FROM doomsday_run_seen s WHERE s.profile = r.profile) GROUP BY r.profile;

CREATE TRIGGER IF NOT EXISTS doomsday_run_seen_no_update BEFORE UPDATE ON doomsday_run_seen BEGIN SELECT RAISE(ABORT, 'doomsday_run_seen is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_run_seen_no_delete BEFORE DELETE ON doomsday_run_seen BEGIN SELECT RAISE(ABORT, 'doomsday_run_seen is insert-only'); END;
CREATE TRIGGER IF NOT EXISTS doomsday_run_seen_no_replace BEFORE INSERT ON doomsday_run_seen
WHEN EXISTS (SELECT 1 FROM doomsday_run_seen s WHERE s.profile = NEW.profile)
BEGIN SELECT RAISE(ABORT, 'doomsday_run_seen row exists: rows are never replaced'); END;
