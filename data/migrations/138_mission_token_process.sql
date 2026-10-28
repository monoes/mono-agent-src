-- A mission token may name the process its worker or verifier runs as (pid and start stamp, signed),
-- so a process outside that process's tree that merely read the token is refused. Empty (0, '', '')
-- is a token that names none, which keeps its old behaviour.
--
-- Additive only: columns with empty defaults, so every existing token stays as it was.
ALTER TABLE agent_tokens ADD COLUMN mission_pid INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_tokens ADD COLUMN mission_stamp TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_tokens ADD COLUMN mission_mac TEXT NOT NULL DEFAULT '';
