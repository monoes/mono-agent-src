-- 132_coder_command_wants_mission.sql
-- Whether a pending send was asked to run as a mission. This is not authority:
-- it only tells the host to fail the command when its signed record is missing
-- or expired. It replaces a marker that used to be kept in the error column.
-- Additive; the backfill moves any in-flight marker row to the column.
ALTER TABLE coder_session_commands ADD COLUMN wants_mission INTEGER NOT NULL DEFAULT 0 CHECK (wants_mission IN (0, 1));
UPDATE coder_session_commands SET wants_mission = 1, error = '' WHERE error = 'wants-mission';
