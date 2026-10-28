-- 143_coder_budget_requests.sql
-- A mission lead's request for more budget, answered only by the person (a person item of kind
-- budget_request points here by ref_id). The host fills every column from its own numbers: the amounts
-- are read from the session, the reason is cleaned text, nothing is raw model output. One row is one
-- grant at most: status moves from open once, to granted or declined (or void when its mission is gone),
-- and a granted row says how much was added to which session.
CREATE TABLE IF NOT EXISTS coder_budget_requests (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id             TEXT NOT NULL,
    session_id             INTEGER NOT NULL,             -- task_sessions.id of the mission
    lead_conversation_id   TEXT NOT NULL,                -- who asked (the lead)
    target                 TEXT NOT NULL,                -- lead | child
    target_conversation_id TEXT NOT NULL,                -- the session whose budget a grant raises
    task_id                INTEGER NOT NULL DEFAULT 0,   -- the child's task, when target is child
    amount_usd             REAL NOT NULL,                -- what was asked
    budget_usd             REAL NOT NULL,                -- the target's budget when asked
    spent_usd              REAL NOT NULL,                -- what the target had spent when asked
    reason                 TEXT NOT NULL DEFAULT '',     -- cleaned, capped
    status                 TEXT NOT NULL DEFAULT 'open', -- open | granted | declined | void
    granted_usd            REAL NOT NULL DEFAULT 0,
    created_at             TEXT NOT NULL,
    answered_at            TEXT NOT NULL DEFAULT ''
);
-- One open request per target.
CREATE UNIQUE INDEX IF NOT EXISTS idx_coder_budget_requests_open ON coder_budget_requests(profile_id, target_conversation_id) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS idx_coder_budget_requests_session ON coder_budget_requests(profile_id, session_id);
