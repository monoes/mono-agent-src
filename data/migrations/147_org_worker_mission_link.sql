-- 147_org_worker_mission_link.sql
-- A stored dynamic-org worker remembers the mission task it worked, so that the operator can retire it
-- (`task session retire-worker`): without the link nothing says which stored worker belongs to which task,
-- because a mission worker's name is derived from its session and task, not from the worker's id.
-- 0 = not a mission worker, or stored before this migration. mission_claims is the claim number the worker started
-- under (how many times the task had been taken when it was): a retirement made at claim N retires the workers with
-- a number of N or less, so a worker that takes the task later is not retired and an earlier one stays retired.
--
-- Additive only: three columns with an empty default.
ALTER TABLE ai_chat_org_workers ADD COLUMN mission_session INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ai_chat_org_workers ADD COLUMN mission_task    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ai_chat_org_workers ADD COLUMN mission_claims  INTEGER NOT NULL DEFAULT 0;
