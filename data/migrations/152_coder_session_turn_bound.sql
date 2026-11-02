-- Whether the process recorded as a coder session's current turn was started with peer-credential binding. The launcher records the
-- process (turn_pid, turn_stamp) the moment it exists; a bound launch has no token yet at that moment, so this is what tells recovery to
-- stop it rather than adopt it if the host dies in between. 0 for every session and every ordinary launch. Additive only.
ALTER TABLE coder_sessions ADD COLUMN turn_bound INTEGER NOT NULL DEFAULT 0;
-- An unreleased development head recorded the discriminator as a "bound:" prefix on the stamp; translate such rows (none exist in a released database).
UPDATE coder_sessions SET turn_bound = 1, turn_stamp = substr(turn_stamp, 7) WHERE turn_stamp LIKE 'bound:%';
