-- 143_coder_budget_requests.sql
-- A mission lead's request for more budget, answered only by the person (a person item of kind
-- budget_request points here by ref_id). The host fills every column from its own numbers: the amounts
-- are read from the session, the reason is cleaned text, nothing is raw model output. One row is one
-- grant at most: status moves from open once, to granted or declined (or void when what it was filed
-- against changed), and a granted row says how much was added to which session.
CREATE TABLE IF NOT EXISTS coder_budget_requests (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id             TEXT NOT NULL,
    session_id             INTEGER NOT NULL,             -- task_sessions.id of the mission
    lead_conversation_id   TEXT NOT NULL,                -- who asked (the lead)
    target                 TEXT NOT NULL CHECK (target IN ('lead', 'child')),
    target_conversation_id TEXT NOT NULL,                -- the session whose budget a grant raises
    task_id                INTEGER NOT NULL DEFAULT 0,   -- the child's task, when target is child
    amount_usd             REAL NOT NULL,                -- what was asked
    budget_usd             REAL NOT NULL,                -- the target's budget when asked
    spent_usd              REAL NOT NULL,                -- what the target had spent when asked
    reason                 TEXT NOT NULL DEFAULT '',     -- cleaned, capped
    status                 TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'granted', 'declined', 'void')),
    granted_usd            REAL NOT NULL DEFAULT 0,
    created_at             TEXT NOT NULL,
    answered_at            TEXT NOT NULL DEFAULT ''
);
-- One open request per target.
CREATE UNIQUE INDEX IF NOT EXISTS idx_coder_budget_requests_open ON coder_budget_requests(profile_id, target_conversation_id) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS idx_coder_budget_requests_session ON coder_budget_requests(profile_id, session_id);

-- What the person saw is what a grant raises: the identity of a request never changes, and a request that left
-- open never comes back.
CREATE TRIGGER IF NOT EXISTS trg_coder_budget_requests_pinned
BEFORE UPDATE OF profile_id, session_id, lead_conversation_id, target, target_conversation_id, task_id, amount_usd, budget_usd, spent_usd, reason, created_at
ON coder_budget_requests
BEGIN
    SELECT RAISE(ABORT, 'a budget request cannot be edited');
END;
CREATE TRIGGER IF NOT EXISTS trg_coder_budget_requests_once
BEFORE UPDATE OF status ON coder_budget_requests
WHEN OLD.status <> 'open'
BEGIN
    SELECT RAISE(ABORT, 'a budget request that was answered or voided stays closed');
END;

-- Stale means closed for good: the moment what a request was filed against changes, it is void, so numbers that
-- move and come back, or a mission that ends and returns, never make it answerable again. (Pausing a mission or
-- session is routine and only hides the request while it lasts.)
CREATE TRIGGER IF NOT EXISTS trg_coder_budget_requests_session_moved
AFTER UPDATE OF budget_usd, state ON coder_sessions
BEGIN
    UPDATE coder_budget_requests SET status = 'void', answered_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
     WHERE status = 'open' AND profile_id = NEW.profile_id
       AND ((target_conversation_id = NEW.conversation_id AND (budget_usd IS NOT NEW.budget_usd OR NEW.state = 'stopped'))
            OR (lead_conversation_id = NEW.conversation_id AND NEW.state = 'stopped'));
END;
CREATE TRIGGER IF NOT EXISTS trg_coder_budget_requests_session_gone
AFTER DELETE ON coder_sessions
BEGIN
    UPDATE coder_budget_requests SET status = 'void', answered_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
     WHERE status = 'open' AND profile_id = OLD.profile_id AND (target_conversation_id = OLD.conversation_id OR lead_conversation_id = OLD.conversation_id);
END;
CREATE TRIGGER IF NOT EXISTS trg_coder_budget_requests_mission_ended
AFTER UPDATE OF state, archived_at ON task_sessions
WHEN NEW.archived_at <> '' OR NEW.state IN ('done', 'stopped', 'failed')
BEGIN
    UPDATE coder_budget_requests SET status = 'void', answered_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
     WHERE status = 'open' AND profile_id = NEW.profile_id AND session_id = NEW.id;
END;
CREATE TRIGGER IF NOT EXISTS trg_coder_budget_requests_mission_gone
AFTER DELETE ON task_sessions
BEGIN
    UPDATE coder_budget_requests SET status = 'void', answered_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
     WHERE status = 'open' AND profile_id = OLD.profile_id AND session_id = OLD.id;
END;
CREATE TRIGGER IF NOT EXISTS trg_coder_budget_requests_child_moved
AFTER UPDATE OF state, child_conversation_id ON coder_mission_children
BEGIN
    UPDATE coder_budget_requests SET status = 'void', answered_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
     WHERE status = 'open' AND target = 'child' AND profile_id = NEW.profile_id AND session_id = NEW.session_id AND task_id = NEW.task_id
       AND (NEW.state <> 'spawned' OR target_conversation_id <> NEW.child_conversation_id);
END;
CREATE TRIGGER IF NOT EXISTS trg_coder_budget_requests_child_gone
AFTER DELETE ON coder_mission_children
BEGIN
    UPDATE coder_budget_requests SET status = 'void', answered_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
     WHERE status = 'open' AND target = 'child' AND profile_id = OLD.profile_id AND session_id = OLD.session_id AND task_id = OLD.task_id;
END;
