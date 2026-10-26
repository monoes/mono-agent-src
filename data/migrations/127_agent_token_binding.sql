-- A scoped agent token may be bound to one AI task and the conversation that runs it
-- (`monoagentcli agent-mode token create --task ID --conversation ID`). A route that acts on
-- a task accepts only a token bound to that task; a token with no binding (both columns empty)
-- reaches the routes that name no task and nothing else.
--
-- Additive only: two columns with empty defaults, so every existing token stays unbound.
ALTER TABLE agent_tokens ADD COLUMN bound_conversation TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_tokens ADD COLUMN bound_task TEXT NOT NULL DEFAULT '';
