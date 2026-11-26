-- 171_task_session_wrapup.sql
-- The wrap-up allowance of a mission that spent its budget (#1751): one row for each turn of the lead admitted under
-- it. hold_micros is what the turn was admitted with and counts until the turn is settled with what it cost
-- (spent_micros), so a turn that died or reported no cost keeps its hold. budget_micros is the session's budget when
-- the turn was admitted: only the turns of budgets at least the current one count, so raising the budget starts a new
-- allowance. Amounts are whole millionths of a dollar. Deleting the session or the profile removes the rows.
CREATE TABLE IF NOT EXISTS task_session_wrapup_turns (
    profile_id    TEXT    NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    session_id    INTEGER NOT NULL REFERENCES task_sessions(id) ON DELETE CASCADE,
    turn_id       TEXT    NOT NULL CHECK (typeof(turn_id) = 'text' AND length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 256),
    budget_micros INTEGER NOT NULL CHECK (typeof(budget_micros) = 'integer' AND budget_micros > 0),
    hold_micros   INTEGER NOT NULL CHECK (typeof(hold_micros) = 'integer' AND hold_micros > 0),
    spent_micros  INTEGER NOT NULL DEFAULT 0 CHECK (typeof(spent_micros) = 'integer' AND spent_micros >= 0),
    settled       INTEGER NOT NULL DEFAULT 0 CHECK (settled IN (0, 1)),
    created_at    TEXT    NOT NULL,
    PRIMARY KEY (session_id, turn_id)
);
