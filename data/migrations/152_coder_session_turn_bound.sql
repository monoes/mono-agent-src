-- Whether the process recorded as a coder session's current turn was started with peer-credential binding. The launcher records the
-- process (turn_pid, turn_stamp) the moment it exists; a bound launch has no token yet at that moment, so this is what tells recovery to
-- stop it rather than adopt it if the host dies in between. 0 for every session and every ordinary launch. Additive only.
ALTER TABLE coder_sessions ADD COLUMN turn_bound INTEGER NOT NULL DEFAULT 0;
