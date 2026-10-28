-- 141_coder_mission_child_generation.sql
-- A reservation of a mission task's child session gets an identity of its own: a generation number taken from
-- one counter that only goes up, so it is never reused (not by a later reservation of the same task, not by a
-- row that is deleted and made again). The callbacks of a spawn (note the conversation, link it, release or
-- fail the reservation) are fenced on it, so an attempt that is still unwinding cannot touch a newer one.
-- 'attempts' stays what it was: the count of chargeable attempts, which a refused spawn gives back.
--
-- Additive only: one column with an empty default (0 = no reservation yet) and one single-row counter table.
ALTER TABLE coder_mission_children ADD COLUMN generation INTEGER NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS coder_mission_generation (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    n  INTEGER NOT NULL
);
INSERT OR IGNORE INTO coder_mission_generation (id, n) VALUES (1, 0);
