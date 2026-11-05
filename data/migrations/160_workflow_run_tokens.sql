-- 160_workflow_run_tokens.sql
-- Positive, unforgeable approval for the children of a workflow run (#1427). The engine records, per EXECUTION, whether
-- the run was approved (its definition the operator's, not called by an unapproved workflow, not started by an AI) and
-- hands the processes its nodes start only a random token. A gate that must tell an approved workflow's child from an
-- unapproved one looks the token up here: no row, an unapproved row, an ended or expired row, or a database that
-- cannot be read all mean HELD, so a child that drops or forges the token fails closed.
--
-- Only a hash of the token is stored (the token itself lives in the children's environment and nowhere else).
-- approved is fixed at insert and cannot change; a row can only be ENDED (ended_at set once, when the execution
-- stops). Deleting a row is allowed: it can only turn a token dead, which is the closed side. A same-user shell that
-- can write this database can insert its own approved row; that is the limit the operator package documents.
CREATE TABLE IF NOT EXISTS workflow_run_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   TEXT NOT NULL UNIQUE CHECK (typeof(token_hash) = 'text' AND length(token_hash) = 64 AND token_hash NOT GLOB '*[^0-9a-f]*'),
    execution_id TEXT NOT NULL CHECK (typeof(execution_id) = 'text' AND length(CAST(execution_id AS BLOB)) BETWEEN 1 AND 256 AND instr(execution_id, char(0)) = 0),
    approved     INTEGER NOT NULL CHECK (approved IN (0, 1)),
    created_at   TEXT NOT NULL CHECK (typeof(created_at) = 'text' AND length(CAST(created_at AS BLOB)) = 20 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z'),
    expires_at   TEXT NOT NULL CHECK (typeof(expires_at) = 'text' AND length(CAST(expires_at AS BLOB)) = 20 AND expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z'),
    ended_at     TEXT CHECK (ended_at IS NULL OR (typeof(ended_at) = 'text' AND length(CAST(ended_at AS BLOB)) = 20 AND ended_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z')),
    CHECK (id > 0)
);
CREATE INDEX IF NOT EXISTS idx_workflow_run_tokens_execution ON workflow_run_tokens(execution_id);

-- The only change a row accepts: ended_at, once.
CREATE TRIGGER IF NOT EXISTS workflow_run_tokens_end_only BEFORE UPDATE ON workflow_run_tokens
WHEN NOT (OLD.ended_at IS NULL AND NEW.ended_at IS NOT NULL AND NEW.id = OLD.id AND NEW.token_hash = OLD.token_hash
          AND NEW.execution_id = OLD.execution_id AND NEW.approved = OLD.approved AND NEW.created_at = OLD.created_at
          AND NEW.expires_at = OLD.expires_at)
BEGIN SELECT RAISE(ABORT, 'workflow_run_tokens: a row can only be ended'); END;
