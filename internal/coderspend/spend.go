// Package coderspend computes what a coder conversation has cost and writes a deleted mission child's spend down.
// It is a leaf package so that both the coder host (which charges a mission from it) and the conversation delete in
// internal/ai (which must record it before the usage events go) use the one implementation and the one format.
package coderspend

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
)

// Querier is the read side of *sql.DB, *sql.Tx and the delete's connection.
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Kind is the audit row ("coder_mission_events.kind") that holds a deleted child's spend until the host charges it.
const Kind = "spend"

// Note is the text of that row: "<conversation> $<usd>".
func Note(conversationID string, usd float64) string {
	return fmt.Sprintf("%s $%.6f", conversationID, usd)
}

// RunningCap is the cap of the turn the session is running now (0 when none, or none capped).
func RunningCap(q Querier, profileID, conversationID string) (turn string, capUSD float64) {
	_ = q.QueryRow(`SELECT x.turn_id, COALESCE(k.cap_usd, 0) FROM coder_sessions x
		LEFT JOIN coder_session_turn_caps k ON k.profile_id = x.profile_id AND k.conversation_id = x.conversation_id AND k.turn_id = x.turn_id
		WHERE x.profile_id = ? AND x.conversation_id = ? AND x.turn_id != ''`, profileID, conversationID).Scan(&turn, &capUSD)
	return turn, capUSD
}

func validCost(p *float64) bool { return p != nil && *p >= 0 && !math.IsNaN(*p) && !math.IsInf(*p, 0) }

// Spent is what the conversation's turns have cost, read through q (a transaction, when the number must agree
// with the rows read beside it). See the budget notes in internal/coderhost/budget.go for the rules.
func Spent(q Querier, profileID, conversationID string) (float64, error) {
	caps := map[string]float64{}
	rows, err := q.Query(`SELECT turn_id, cap_usd FROM coder_session_turn_caps WHERE profile_id = ? AND conversation_id = ? AND started = 1`, profileID, conversationID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var t string
		var c float64
		if err := rows.Scan(&t, &c); err != nil {
			rows.Close()
			return 0, err
		}
		caps[t] = c
	}
	rows.Close()

	lead := map[string]float64{}       // turn -> its largest reported total
	workers := map[[2]string]float64{} // (turn, worker) -> its largest finished cost
	ev, err := q.Query(`SELECT turn_id, type, payload FROM ai_chat_events WHERE profile_id = ? AND conversation_id = ? AND type IN ('usage.updated', 'agent.finished')`, profileID, conversationID)
	if err != nil {
		return 0, err
	}
	for ev.Next() {
		var turn, typ, raw string
		if err := ev.Scan(&turn, &typ, &raw); err != nil {
			ev.Close()
			return 0, err
		}
		var p struct {
			AgentID string   `json:"agentId"`
			CostUSD *float64 `json:"costUsd"`
		}
		if json.Unmarshal([]byte(raw), &p) != nil || !validCost(p.CostUSD) {
			continue // not numeric, negative: not a cost
		}
		if typ == "usage.updated" {
			if cur, seen := lead[turn]; p.AgentID == "" && (!seen || *p.CostUSD > cur) {
				lead[turn] = *p.CostUSD
			}
		} else if k := [2]string{turn, p.AgentID}; true {
			if cur, seen := workers[k]; !seen || *p.CostUSD > cur {
				workers[k] = *p.CostUSD
			}
		}
	}
	ev.Close()
	running, _ := RunningCap(q, profileID, conversationID)
	total := 0.0
	workerSum := map[string]float64{} // per turn
	for k, v := range workers {
		workerSum[k[0]] += v
	}
	for turn, c := range caps {
		if v, reported := lead[turn]; reported {
			total += v + workerSum[turn]
		} else if turn != running {
			total += max(c, workerSum[turn]) // no usage event at all: the whole cap, or its workers if they cost more
		} else {
			total += workerSum[turn]
		}
		delete(lead, turn)
		delete(workerSum, turn)
	}
	for _, v := range lead {
		total += v // turns that ran before the session had a budget
	}
	for _, v := range workerSum {
		total += v
	}
	return total, nil
}

// Snapshot records the spend of a mission child's conversation, to be called inside the transaction that deletes
// the conversation (conn is that transaction's connection), before its usage events and turn caps are removed.
// It does nothing for a conversation that is no mission child's, or whose spend is already charged or recorded,
// so running it twice writes one row. An error aborts the delete.
func Snapshot(ctx context.Context, conn *sql.Conn, profileID, conversationID string) error {
	var sid, tid int64
	err := conn.QueryRowContext(ctx, `SELECT session_id, task_id FROM coder_mission_children WHERE profile_id = ? AND child_conversation_id = ?`,
		profileID, conversationID).Scan(&sid, &tid)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM coder_mission_events WHERE profile_id = ? AND session_id = ? AND kind IN ('charge', 'spend')
		AND substr(note, 1, ?) = ?`, profileID, sid, len(conversationID)+1, conversationID+" ").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	spent, err := Spent(connQuerier{ctx, conn}, profileID, conversationID)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO coder_mission_events (profile_id, session_id, task_id, kind, who, note, at)
		VALUES (?, ?, ?, ?, 'host', ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`, profileID, sid, tid, Kind, Note(conversationID, spent))
	return err
}

// connQuerier lets the spend computation read through the delete's connection.
type connQuerier struct {
	ctx  context.Context
	conn *sql.Conn
}

func (c connQuerier) Query(q string, a ...any) (*sql.Rows, error) {
	return c.conn.QueryContext(c.ctx, q, a...)
}
func (c connQuerier) QueryRow(q string, a ...any) *sql.Row {
	return c.conn.QueryRowContext(c.ctx, q, a...)
}
