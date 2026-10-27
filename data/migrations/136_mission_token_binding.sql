-- A scoped agent token may carry a mission role: the task session, task, role and name of the
-- worker or verifier process a conductor started, and the run that minted it. Such a token is
-- what the task commands and tools read to learn what a process may act on; it is not accepted
-- by the HTTP API, and the HTTP API's tokens carry none of this.
--
-- Additive only: columns with empty defaults, so every existing token stays as it was.
ALTER TABLE agent_tokens ADD COLUMN mission_session INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_tokens ADD COLUMN mission_task INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_tokens ADD COLUMN mission_role TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_tokens ADD COLUMN mission_actor TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_tokens ADD COLUMN mission_owner TEXT NOT NULL DEFAULT '';
