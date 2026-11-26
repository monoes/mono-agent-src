-- 172_person_messages_profile.sql
-- A message belongs to its person's profile. Nothing kept
-- person_messages.profile_id equal to it (the column defaults to 'default',
-- and migration 099 stamped empty values without looking at the person), so
-- one message could be listed by person in one profile and by its own column
-- in another.
--
-- Repair: a message whose person has a profile takes that profile. No row is
-- deleted and no other column changes; the value replaced is kept in
-- person_message_profile_repairs. Left exactly as stored: a message whose
-- person no longer exists (it is never given to some other profile) and a
-- message whose person has no profile of its own (there is none to take).
--
-- Triggers keep it so: a new message with an empty profile takes its
-- person's, one naming another profile is refused, as is a change of a
-- message's profile or person that would break the match, and a person that
-- changes profile takes its messages along. A later migration that rebuilds
-- people or person_messages must drop these three triggers first and create
-- them again afterwards.
--
-- Idempotent: a second run finds nothing to repair.

CREATE TABLE IF NOT EXISTS person_message_profile_repairs (
    message_id  TEXT NOT NULL,
    person_id   TEXT NOT NULL,
    was_profile TEXT,
    now_profile TEXT NOT NULL,
    repaired_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

INSERT INTO person_message_profile_repairs (message_id, person_id, was_profile, now_profile)
SELECT pm.id, pm.person_id, pm.profile_id, p.profile_id
FROM person_messages pm
JOIN people p ON p.id = pm.person_id
WHERE COALESCE(p.profile_id, '') <> '' AND pm.profile_id IS NOT p.profile_id;

UPDATE person_messages
SET profile_id = (SELECT p.profile_id FROM people p WHERE p.id = person_messages.person_id)
WHERE EXISTS (SELECT 1 FROM people p
    WHERE p.id = person_messages.person_id
      AND COALESCE(p.profile_id, '') <> ''
      AND p.profile_id IS NOT person_messages.profile_id);

CREATE TRIGGER IF NOT EXISTS person_messages_profile_on_insert
AFTER INSERT ON person_messages
WHEN EXISTS (SELECT 1 FROM people p
    WHERE p.id = NEW.person_id AND COALESCE(p.profile_id, '') <> '' AND p.profile_id IS NOT NEW.profile_id)
BEGIN
    SELECT RAISE(ABORT, 'person_messages: a message must be in its person''s profile')
    WHERE COALESCE(NEW.profile_id, '') <> '';
    UPDATE person_messages
    SET profile_id = (SELECT p.profile_id FROM people p WHERE p.id = NEW.person_id)
    WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS person_messages_profile_on_update
AFTER UPDATE OF profile_id, person_id ON person_messages
WHEN EXISTS (SELECT 1 FROM people p
    WHERE p.id = NEW.person_id AND COALESCE(p.profile_id, '') <> '' AND p.profile_id IS NOT NEW.profile_id)
BEGIN
    SELECT RAISE(ABORT, 'person_messages: a message must be in its person''s profile');
END;

CREATE TRIGGER IF NOT EXISTS people_profile_moves_messages
AFTER UPDATE OF profile_id ON people
WHEN COALESCE(NEW.profile_id, '') <> '' AND NEW.profile_id IS NOT OLD.profile_id
BEGIN
    UPDATE person_messages SET profile_id = NEW.profile_id
    WHERE person_id = NEW.id AND profile_id IS NOT NEW.profile_id;
END;

-- Queries that name a profile together with a card or a person must stay on
-- the card's or the person's rows: without these two, SQLite (which has no
-- statistics here) picks one of the profile indexes below and walks every
-- message of the profile.
CREATE INDEX IF NOT EXISTS idx_person_messages_hil_ref_profile
    ON person_messages(hil_ref, profile_id);
CREATE INDEX IF NOT EXISTS idx_person_messages_person_profile
    ON person_messages(person_id, profile_id, created_at DESC);

-- A profile's messages in one status, newest first (the drafts list and count).
CREATE INDEX IF NOT EXISTS idx_person_messages_profile_status
    ON person_messages(profile_id, status, created_at DESC);

-- A profile's messages newest first, whatever form sent_at was stored in: the
-- expression is the one internal/storage's listing orders by
-- (messageInstantOrder), so paging reads in index order. Text that does not
-- start with a digit is not read as a time: SQLite refuses an index whose
-- date function meets 'now'.
CREATE INDEX IF NOT EXISTS idx_person_messages_profile_instant
    ON person_messages(profile_id, COALESCE(
        (CASE typeof(sent_at) WHEN 'integer' THEN sent_at/86400.0+2440587.5 WHEN 'real' THEN sent_at/86400.0+2440587.5 WHEN 'text' THEN julianday(CASE WHEN sent_at NOT GLOB '[0-9]*' THEN NULL WHEN instr(substr(NULLIF(sent_at, ''),12),' ')>0 THEN substr(NULLIF(sent_at, ''),1,10+instr(substr(NULLIF(sent_at, ''),12),' ')) ELSE NULLIF(sent_at, '') END) END),
        (CASE typeof(created_at) WHEN 'integer' THEN created_at/86400.0+2440587.5 WHEN 'real' THEN created_at/86400.0+2440587.5 WHEN 'text' THEN julianday(CASE WHEN created_at NOT GLOB '[0-9]*' THEN NULL WHEN instr(substr(NULLIF(created_at, ''),12),' ')>0 THEN substr(NULLIF(created_at, ''),1,10+instr(substr(NULLIF(created_at, ''),12),' ')) ELSE NULLIF(created_at, '') END) END)
    ) DESC);
