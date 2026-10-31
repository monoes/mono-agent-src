-- 150_mission_child_held.sql
-- What the coder host's mission children hold against a mission's budget, as one view, so the worker reservation
-- store (internal/tasks) and the coder host (internal/coderhost) read the same number. The host's check
-- (missionBudgetRefusal) and the reservation store's commitment walk both read it; neither keeps a copy.
--
-- One row per task session that names a lead conversation. held_usd is what the lead's children hold:
--   * the caps of the coder sessions the lead started (their budget_usd), except a child that ended or failed
--     and whose cost is already on the meter (a charge row in coder_mission_events): its cap was lowered to that
--     spend, which spent_usd already contains; and except a LINKED child, one that has a task session of its own
--     below this one (session_spawn makes both): its cap is v_mission_child_linked's, and the commitment walk
--     counts that child once, as the larger of the cap and the task session's own commitment;
--   * plus the carves of the lead's rows that are still spawning (their session does not exist yet);
--   * plus the spend snapshots no charge row answers yet (a deleted child's cost: known, cannot grow).
-- A lost child (lost = 1) holds the whole remainder of the mission, once for the mission however many children
-- are lost: held_usd is then the larger of the sum above and budget_usd - spent_usd (never below 0), never their
-- sum. A child is lost when its conversation is known to its row (child id, else pending id, and the spawn did
-- create one), has no coder session any more and nothing records its cost (no charge, spend snapshot or
-- unresolved record); a row still spawning counts. An unresolved record that no charge row answers counts too.
-- The operator ends the state with `coder sessions mission-children resolve-charge`.
CREATE VIEW IF NOT EXISTS v_mission_child_held AS
SELECT profile_id, session_id, lost,
    CASE WHEN lost = 1 AND budget_usd > 0 THEN MAX(raw_held, MAX(budget_usd - spent_usd, 0)) ELSE raw_held END AS held_usd
FROM (
    SELECT ts.profile_id AS profile_id, ts.id AS session_id, ts.budget_usd AS budget_usd, ts.spent_usd AS spent_usd,
        (SELECT COALESCE(SUM(s.budget_usd), 0) FROM coder_sessions s WHERE s.profile_id = ts.profile_id AND s.parent_conversation_id = ts.conversation_id
            AND NOT EXISTS (SELECT 1 FROM coder_mission_children c WHERE c.profile_id = s.profile_id AND c.child_conversation_id = s.conversation_id
                AND c.session_id = ts.id AND c.state IN ('ended', 'failed')
                AND EXISTS (SELECT 1 FROM coder_mission_events e WHERE e.profile_id = c.profile_id AND e.session_id = c.session_id AND e.kind = 'charge'
                    AND substr(e.note, 1, length(c.child_conversation_id) + 1) = c.child_conversation_id || ' '))
            AND NOT EXISTS (SELECT 1 FROM task_sessions k WHERE k.profile_id = s.profile_id AND k.parent_session_id = ts.id AND k.conversation_id = s.conversation_id))
        + (SELECT COALESCE(SUM(reserved_usd), 0) FROM coder_mission_children WHERE profile_id = ts.profile_id AND lead_conversation_id = ts.conversation_id AND state = 'spawning')
        + (SELECT COALESCE(SUM(CAST(substr(e.note, instr(e.note, ' $') + 2) AS REAL)), 0) FROM coder_mission_events e WHERE e.profile_id = ts.profile_id AND e.session_id = ts.id AND e.kind = 'spend'
            AND NOT EXISTS (SELECT 1 FROM coder_mission_events c WHERE c.profile_id = e.profile_id AND c.session_id = e.session_id AND c.kind = 'charge'
                AND substr(c.note, 1, instr(e.note, ' ')) = substr(e.note, 1, instr(e.note, ' ')))) AS raw_held,
        (EXISTS (SELECT 1 FROM coder_mission_children m WHERE m.profile_id = ts.profile_id AND m.session_id = ts.id
                AND m.state IN ('spawning', 'spawned', 'ended', 'failed', 'gone')
                AND (CASE WHEN m.child_conversation_id <> '' THEN m.child_conversation_id ELSE m.pending_conversation_id END) <> ''
                AND (m.spawned_at <> '' OR m.pending_conversation_id <> '')
                AND NOT EXISTS (SELECT 1 FROM coder_sessions s WHERE s.profile_id = m.profile_id
                    AND s.conversation_id = (CASE WHEN m.child_conversation_id <> '' THEN m.child_conversation_id ELSE m.pending_conversation_id END))
                AND NOT EXISTS (SELECT 1 FROM coder_mission_events x WHERE x.profile_id = m.profile_id AND x.session_id = m.session_id
                    AND x.kind IN ('charge', 'spend', 'unresolved')
                    AND substr(x.note, 1, length(CASE WHEN m.child_conversation_id <> '' THEN m.child_conversation_id ELSE m.pending_conversation_id END) + 1)
                        = (CASE WHEN m.child_conversation_id <> '' THEN m.child_conversation_id ELSE m.pending_conversation_id END) || ' '))
            OR EXISTS (SELECT 1 FROM coder_mission_events u WHERE u.profile_id = ts.profile_id AND u.session_id = ts.id AND u.kind = 'unresolved'
                AND NOT EXISTS (SELECT 1 FROM coder_mission_events c WHERE c.profile_id = u.profile_id AND c.session_id = u.session_id AND c.kind = 'charge'
                    AND substr(c.note, 1, instr(u.note, ' ')) = substr(u.note, 1, instr(u.note, ' '))))) AS lost
    FROM task_sessions ts WHERE ts.conversation_id IS NOT NULL AND ts.conversation_id <> ''
);

-- A linked child: a coder session of the lead that has a task session below the mission with the same conversation.
-- cap_usd is the coder cap (the same exclusion of an ended, charged child as above); the commitment walk takes, for
-- the child task session, the larger of this and the task session's own hold, so the child counts once.
CREATE VIEW IF NOT EXISTS v_mission_child_linked AS
SELECT k.profile_id AS profile_id, k.id AS child_session_id, s.budget_usd AS cap_usd
FROM task_sessions k
JOIN task_sessions ts ON ts.profile_id = k.profile_id AND ts.id = k.parent_session_id AND ts.conversation_id IS NOT NULL AND ts.conversation_id <> ''
JOIN coder_sessions s ON s.profile_id = k.profile_id AND s.conversation_id = k.conversation_id AND s.parent_conversation_id = ts.conversation_id
WHERE k.conversation_id IS NOT NULL AND k.conversation_id <> ''
    AND NOT EXISTS (SELECT 1 FROM coder_mission_children c WHERE c.profile_id = s.profile_id AND c.child_conversation_id = s.conversation_id
        AND c.session_id = ts.id AND c.state IN ('ended', 'failed')
        AND EXISTS (SELECT 1 FROM coder_mission_events e WHERE e.profile_id = c.profile_id AND e.session_id = c.session_id AND e.kind = 'charge'
            AND substr(e.note, 1, length(c.child_conversation_id) + 1) = c.child_conversation_id || ' '));
