// Package aimemory keeps conversation memory for AI steps: the messages of
// each chat session, per profile, in the profile database (table
// chat_memory_messages, migration 081). agent.ask reads a window of a
// session before a turn and appends the turn after it; the agent.memory node
// reads, inserts and deletes messages.
package aimemory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Roles of a stored message.
const (
	RoleHuman  = "human"
	RoleAI     = "ai"
	RoleSystem = "system"
)

const timeLayout = "2006-01-02T15:04:05.000Z"

// Message is one stored message of a session.
type Message struct {
	Seq       int64  `json:"seq"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

// NewMessage is a message to store.
type NewMessage struct {
	Role    string
	Content string
}

// Session summarises one stored session.
type Session struct {
	SessionKey string `json:"session_key"`
	Messages   int    `json:"messages"`
	LastAt     string `json:"last_at"`
}

// NormalizeRole maps the role names in use (human/user, ai/assistant,
// system) to a stored role, case-insensitively.
func NormalizeRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "human", "user":
		return RoleHuman, nil
	case "ai", "assistant":
		return RoleAI, nil
	case "system":
		return RoleSystem, nil
	}
	return "", fmt.Errorf("unknown message role %q (use human, ai or system)", role)
}

// check refuses a call without a database, a profile or a session: memory
// always belongs to a named profile (there is no implicit "default").
func check(db *sql.DB, profile, session string) error {
	if db == nil {
		return fmt.Errorf("chat memory needs the profile database")
	}
	if profile == "" {
		return fmt.Errorf("chat memory: no profile given")
	}
	if strings.TrimSpace(session) == "" {
		return fmt.Errorf("chat memory: the session key is empty")
	}
	return nil
}

// execer is what an insert needs: a *sql.DB or a *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// insert appends one message. The sequence number is computed inside the
// INSERT statement, which SQLite runs under its write lock, so concurrent
// appends to one session get distinct, increasing numbers.
func insert(ctx context.Context, x execer, profile, session string, m NewMessage) error {
	role, err := NormalizeRole(m.Role)
	if err != nil {
		return err
	}
	_, err = x.ExecContext(ctx, `INSERT INTO chat_memory_messages (profile_id, session_key, seq, role, content, created_at)
		SELECT ?, ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?
		FROM chat_memory_messages WHERE profile_id = ? AND session_key = ?`,
		profile, session, role, m.Content, time.Now().UTC().Format(timeLayout), profile, session)
	if err != nil {
		return fmt.Errorf("chat memory: append: %w", err)
	}
	return nil
}

// Append adds one message to the end of a session.
func Append(ctx context.Context, db *sql.DB, profile, session, role, content string) error {
	return AppendMany(ctx, db, profile, session, []NewMessage{{Role: role, Content: content}})
}

// AppendMany adds messages to the end of a session, in order, all or none.
func AppendMany(ctx context.Context, db *sql.DB, profile, session string, msgs []NewMessage) error {
	return write(ctx, db, profile, session, false, msgs)
}

// Replace clears a session and stores msgs in its place, all or nothing.
func Replace(ctx context.Context, db *sql.DB, profile, session string, msgs []NewMessage) error {
	return write(ctx, db, profile, session, true, msgs)
}

func write(ctx context.Context, db *sql.DB, profile, session string, clear bool, msgs []NewMessage) error {
	if err := check(db, profile, session); err != nil {
		return err
	}
	for _, m := range msgs {
		if _, err := NormalizeRole(m.Role); err != nil {
			return err
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("chat memory: %w", err)
	}
	defer tx.Rollback()
	if clear {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chat_memory_messages WHERE profile_id = ? AND session_key = ?`, profile, session); err != nil {
			return fmt.Errorf("chat memory: clear: %w", err)
		}
	}
	for _, m := range msgs {
		if err := insert(ctx, tx, profile, session, m); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("chat memory: %w", err)
	}
	return nil
}

// Recent returns the last n messages of a session, oldest first; n <= 0
// returns them all.
func Recent(ctx context.Context, db *sql.DB, profile, session string, n int) ([]Message, error) {
	if err := check(db, profile, session); err != nil {
		return nil, err
	}
	q := `SELECT seq, role, content, created_at FROM chat_memory_messages
		WHERE profile_id = ? AND session_key = ? ORDER BY seq DESC`
	args := []interface{}{profile, session}
	if n > 0 {
		q += ` LIMIT ?`
		args = append(args, n)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("chat memory: read: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Seq, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("chat memory: read: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat memory: read: %w", err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Delete removes the last n messages of a session (all of them when it has
// fewer), and returns how many it removed. n <= 0 removes nothing.
func Delete(ctx context.Context, db *sql.DB, profile, session string, n int) (int64, error) {
	if err := check(db, profile, session); err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, nil
	}
	res, err := db.ExecContext(ctx, `DELETE FROM chat_memory_messages WHERE profile_id = ? AND session_key = ? AND seq IN
		(SELECT seq FROM chat_memory_messages WHERE profile_id = ? AND session_key = ? ORDER BY seq DESC LIMIT ?)`,
		profile, session, profile, session, n)
	if err != nil {
		return 0, fmt.Errorf("chat memory: delete: %w", err)
	}
	return res.RowsAffected()
}

// Clear removes every message of a session and returns how many it removed.
func Clear(ctx context.Context, db *sql.DB, profile, session string) (int64, error) {
	if err := check(db, profile, session); err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM chat_memory_messages WHERE profile_id = ? AND session_key = ?`, profile, session)
	if err != nil {
		return 0, fmt.Errorf("chat memory: clear: %w", err)
	}
	return res.RowsAffected()
}

// Sessions lists a profile's sessions, most recently written first.
func Sessions(ctx context.Context, db *sql.DB, profile string) ([]Session, error) {
	if db == nil {
		return nil, fmt.Errorf("chat memory needs the profile database")
	}
	if profile == "" {
		return nil, fmt.Errorf("chat memory: no profile given")
	}
	rows, err := db.QueryContext(ctx, `SELECT session_key, COUNT(*), MAX(created_at) FROM chat_memory_messages
		WHERE profile_id = ? GROUP BY session_key ORDER BY MAX(created_at) DESC, session_key`, profile)
	if err != nil {
		return nil, fmt.Errorf("chat memory: sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.SessionKey, &s.Messages, &s.LastAt); err != nil {
			return nil, fmt.Errorf("chat memory: sessions: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// sessionLocks serialises read-turn-append sequences on one session within
// this process (see LockSession).
var sessionLocks = struct {
	sync.Mutex
	m map[string]*sessionLock
}{m: map[string]*sessionLock{}}

type sessionLock struct {
	mu   sync.Mutex
	refs int
}

// LockSession holds a session until the returned func is called, so that a
// caller's read of the history, its turn and its append are not interleaved
// with another caller's on the same session.
func LockSession(profile, session string) (unlock func()) {
	key := profile + "\x00" + session
	sessionLocks.Lock()
	l := sessionLocks.m[key]
	if l == nil {
		l = &sessionLock{}
		sessionLocks.m[key] = l
	}
	l.refs++
	sessionLocks.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		sessionLocks.Lock()
		if l.refs--; l.refs == 0 {
			delete(sessionLocks.m, key)
		}
		sessionLocks.Unlock()
	}
}
