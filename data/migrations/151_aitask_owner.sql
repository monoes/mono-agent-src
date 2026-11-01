-- Who made a task, for a caller that must recognise its own: a resume gives its submit a private attempt token,
-- and acts on a task of the id it asked for only when the task carries that token. Empty for every other task.
-- Additive only.
ALTER TABLE ai_tasks ADD COLUMN owner TEXT NOT NULL DEFAULT '';
