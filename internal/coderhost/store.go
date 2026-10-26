// Package coderhost is the persistent side of coder sessions: the state of a
// coder conversation and the commands waiting for it live in the database, and
// the host process (`coder host run`) carries them out, so a session outlives
// the app and the terminal that sent to it. This package holds the store and
// the host loop; starting a turn process is the Launcher's job.
package coderhost

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// State is where a session stands. A paused session starts no new turn; one
// that was paused while a turn ran keeps that turn (TurnPID is set) until it ends.
type State string

const (
	StateIdle        State = "idle"
	StateRunning     State = "running"
	StateWaitingUser State = "waiting_user"
	StatePaused      State = "paused"
	StateStopped     State = "stopped"
)

// Kind is what a command asks the host to do.
type Kind string

const (
	KindSend   Kind = "send"
	KindStop   Kind = "stop"
	KindPause  Kind = "pause"
	KindResume Kind = "resume"
)

// Command statuses.
const (
	CmdPending = "pending"
	CmdDone    = "done"
	CmdFailed  = "failed"
)

// Bounds on what one caller can queue.
const (
	MaxMessageBytes   = 256 << 10
	MaxPendingPerChat = 50
	// MaxPendingMaster is how many master-made sends may wait for one child; the rest of
	// MaxPendingPerChat stays for a person's.
	MaxPendingMaster = 5
	// MaxPendingMasterStops is how many stops masters may have waiting for one child.
	MaxPendingMasterStops = 2
	MaxAttachments        = 10
	pendingBatch          = 200
	// MaxSendAge is how long a queued send stays valid.
	MaxSendAge = 6 * time.Hour
	// MaxCheckinAge is how long a check-in the host queued for itself stays valid: its digest is a
	// picture of a moment, so one that waited longer is dropped and the next pass takes a new one.
	MaxCheckinAge      = 15 * time.Minute
	timeLayout         = time.RFC3339Nano
	maxTurnIDLen       = 128
	maxConversationLen = 128
)

// ErrQueueFull is returned by Enqueue when a conversation already has
// MaxPendingPerChat commands waiting.
var ErrQueueFull = errors.New("coder host: too many commands are already waiting for this conversation")

// Command is one request to the host.
type Command struct {
	ID             int64    `json:"id"`
	ProfileID      string   `json:"profileId"`
	ConversationID string   `json:"conversationId"`
	Kind           Kind     `json:"kind"`
	TurnID         string   `json:"turnId,omitempty"`
	Message        string   `json:"message,omitempty"`
	Attachments    []string `json:"attachments,omitempty"`
	Status         string   `json:"status"`
	Error          string   `json:"error,omitempty"`
	CreatedAt      string   `json:"createdAt"`
	UpdatedAt      string   `json:"updatedAt"`
	// WantsMission says the person asked for this send to run as a mission. It is a
	// marker, never a confirmation: it only makes a missing or expired confirmation
	// record fail the command instead of running it as a plain turn.
	WantsMission bool `json:"wantsMission,omitempty"`
	// Issuer is the master (conversation id) that queued the command; "" for a person or the
	// host. The host runs such a command only while the target is still a child of the issuer.
	Issuer string `json:"issuer,omitempty"`
}

// Session is a coder conversation as the host tracks it.
type Session struct {
	ProfileID      string `json:"profileId"`
	ConversationID string `json:"conversationId"`
	State          State  `json:"state"`
	Role           string `json:"role"`
	Parent         string `json:"parentConversationId,omitempty"`
	Unattended     bool   `json:"unattended"`
	TurnID         string `json:"turnId,omitempty"`
	TurnPID        int    `json:"turnPid,omitempty"`
	TurnStamp      string `json:"-"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// Store is the coder_sessions and coder_session_commands tables.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore wraps an open, migrated database.
func NewStore(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

func (s *Store) stamp() string { return s.now().UTC().Format(timeLayout) }

// Enqueue validates and queues a command. The host carries it out when the
// session allows it; a send to a busy or paused session waits.
func (s *Store) Enqueue(c Command) (Command, error) {
	switch c.Kind {
	case KindSend:
		if c.Message == "" {
			return c, errors.New("coder host: a send needs a message")
		}
		if len(c.Message) > MaxMessageBytes {
			return c, fmt.Errorf("coder host: the message is %d bytes; the limit is %d", len(c.Message), MaxMessageBytes)
		}
		if c.TurnID == "" || len(c.TurnID) > maxTurnIDLen {
			return c, errors.New("coder host: a send needs a turn id of at most 128 characters")
		}
		if len(c.Attachments) > MaxAttachments {
			return c, fmt.Errorf("coder host: at most %d attachments", MaxAttachments)
		}
	case KindStop, KindPause, KindResume:
	default:
		return c, fmt.Errorf("coder host: unknown command %q", c.Kind)
	}
	if c.ProfileID == "" || c.ConversationID == "" || len(c.ConversationID) > maxConversationLen {
		return c, errors.New("coder host: a command needs a profile and a conversation")
	}
	att, err := json.Marshal(append([]string{}, c.Attachments...))
	if err != nil {
		return c, err
	}
	// Sends and control commands are capped apart: a chat full of queued sends
	// must still take the stop or resume that would get it moving.
	var waiting int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND status = 'pending' AND (kind = 'send') = ?`,
		c.ProfileID, c.ConversationID, c.Kind == KindSend).Scan(&waiting); err != nil {
		return c, err
	}
	if waiting >= MaxPendingPerChat {
		return c, ErrQueueFull
	}
	// Issuer is the authority for "a master made this", not the turn id: an issuer-tagged send can never
	// be a mission, and a master- turn id is the master tools' alone (a caller with no issuer cannot use it).
	if c.Issuer == "" && strings.HasPrefix(c.TurnID, MasterTurnPrefix) {
		return c, errors.New("coder host: turn ids starting with " + MasterTurnPrefix + " belong to the master tools")
	}
	if c.WantsMission && (c.Kind != KindSend || HostMade(c.TurnID) || c.Issuer != "") {
		return c, errors.New("coder host: only a person's send can ask for a mission")
	}
	mark := ""
	if c.WantsMission {
		mark = wantsMissionMark // lives in the error column while the command is pending
	}
	now := s.stamp()
	// Everything a master queues for a child (sends, pause, resume; not stop, which always has room) shares MaxPendingMaster
	// slots, taken by the INSERT itself so concurrent callers cannot pass the cap together; the rest
	// of MaxPendingPerChat stays for a person's.
	// A master's stops are few: one pending stop per issuer (a repeat is a no-op) and at most
	// MaxPendingMasterStops for a child, so a master cannot fill the person's slots; taken by the INSERT itself.
	masterStop := c.Kind == KindStop && c.Issuer != ""
	masterMade := (c.Issuer != "" && c.Kind != KindStop) || (c.Kind == KindSend && strings.HasPrefix(c.TurnID, MasterTurnPrefix))
	res, err := s.db.Exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, error, created_at, updated_at, issuer)
		SELECT ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?
		WHERE (? = 0 OR (SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND status = 'pending'
			AND ((issuer != '' AND kind != 'stop') OR (kind = 'send' AND substr(turn_id, 1, ?) = ?))) < ?)
		AND (? = 0 OR (
			NOT EXISTS (SELECT 1 FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'stop' AND status = 'pending' AND issuer = ?)
			AND (SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'stop' AND status = 'pending' AND issuer != '') < ?))`,
		c.ProfileID, c.ConversationID, string(c.Kind), c.TurnID, c.Message, string(att), mark, now, now, c.Issuer,
		boolInt(masterMade), c.ProfileID, c.ConversationID, len(MasterTurnPrefix), MasterTurnPrefix, MaxPendingMaster,
		boolInt(masterStop), c.ProfileID, c.ConversationID, c.Issuer, c.ProfileID, c.ConversationID, MaxPendingMasterStops)
	if err != nil {
		return c, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if masterStop { // one pending stop from the same issuer makes a new one a no-op
			var id int64
			if s.db.QueryRow(`SELECT id FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'stop' AND status = 'pending' AND issuer = ? ORDER BY id LIMIT 1`,
				c.ProfileID, c.ConversationID, c.Issuer).Scan(&id) == nil {
				if got, ok, err := s.GetCommand(id); err == nil && ok {
					return got, nil
				}
			}
		}
		return c, ErrQueueFull
	}
	c.ID, _ = res.LastInsertId()
	c.Status, c.CreatedAt, c.UpdatedAt = CmdPending, now, now
	return c, nil
}

// Pending returns the commands still waiting, in the order they were queued.
// Sends to a chat that is paused or has a turn are left out, so a pile of
// blocked sends cannot push a later resume or stop (or another chat's send) out
// of the window; they are picked up on a later pass, once the chat is free.
func (s *Store) Pending() ([]Command, error) {
	rows, err := s.db.Query(`SELECT id, profile_id, conversation_id, kind, turn_id, message, attachments, status, error, created_at, updated_at, issuer
		FROM coder_session_commands c WHERE status = 'pending'
		AND (kind != 'send' OR NOT EXISTS (SELECT 1 FROM coder_sessions s
			WHERE s.profile_id = c.profile_id AND s.conversation_id = c.conversation_id AND (s.state = 'paused' OR s.turn_pid > 0 OR s.turn_id != '')))
		ORDER BY id LIMIT ?`, pendingBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Command
	for rows.Next() {
		var c Command
		var kind, att string
		if err := rows.Scan(&c.ID, &c.ProfileID, &c.ConversationID, &kind, &c.TurnID, &c.Message, &att, &c.Status, &c.Error, &c.CreatedAt, &c.UpdatedAt, &c.Issuer); err != nil {
			return nil, err
		}
		c.Kind = Kind(kind)
		_ = json.Unmarshal([]byte(att), &c.Attachments)
		c.readMark()
		out = append(out, c)
	}
	return out, rows.Err()
}

// CancelPendingSends fails the sends still waiting for a chat that were queued
// before command beforeID (a stop), so a stopped session does not start the
// next queued turn. It returns how many it cancelled.
func (s *Store) CancelPendingSends(profileID, conversationID string, beforeID int64, reason string) (int, error) {
	ids := s.pendingMissionIDs(`profile_id = ? AND conversation_id = ? AND id < ?`, profileID, conversationID, beforeID)
	res, err := s.db.Exec(`UPDATE coder_session_commands SET status = 'failed', error = ?, updated_at = ?
		WHERE profile_id = ? AND conversation_id = ? AND kind = 'send' AND status = 'pending' AND id < ?`,
		reason, s.stamp(), profileID, conversationID, beforeID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	s.revokeCommands(ids)
	return int(n), nil
}

// CancelChildMasterSends fails the master-made sends still waiting for the children of a master
// (it was stopped), with the reason.
func (s *Store) CancelChildMasterSends(profileID, master, reason string) error {
	_, err := s.db.Exec(`UPDATE coder_session_commands SET status = 'failed', error = ?, updated_at = ?
		WHERE profile_id = ? AND kind = 'send' AND status = 'pending' AND issuer = ?`,
		reason, s.stamp(), profileID, master)
	return err
}

// ExpireSends fails the sends that have waited longer than MaxSendAge, so a
// message queued while no host ran does not start a full-access turn days later.
func (s *Store) ExpireSends() error {
	ids := s.pendingMissionIDs(`created_at < ?`, s.now().Add(-MaxSendAge).UTC().Format(timeLayout))
	_, err := s.db.Exec(`UPDATE coder_session_commands SET status = 'failed', error = 'expired before the host could run it', updated_at = ?
		WHERE kind = 'send' AND status = 'pending' AND (created_at < ? OR (substr(turn_id, 1, 8) = 'checkin-' AND created_at < ?) OR (issuer != '' AND created_at < ?))`,
		s.stamp(), s.now().Add(-MaxSendAge).UTC().Format(timeLayout), s.now().Add(-MaxCheckinAge).UTC().Format(timeLayout), s.now().Add(-MaxCheckinAge).UTC().Format(timeLayout))
	s.revokeCommands(ids)
	return err
}

// GetCommand returns one command.
func (s *Store) GetCommand(id int64) (Command, bool, error) {
	rows, err := s.db.Query(`SELECT id, profile_id, conversation_id, kind, turn_id, message, attachments, status, error, created_at, updated_at, issuer
		FROM coder_session_commands WHERE id = ?`, id)
	if err != nil {
		return Command{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Command{}, false, rows.Err()
	}
	var c Command
	var kind, att string
	if err := rows.Scan(&c.ID, &c.ProfileID, &c.ConversationID, &kind, &c.TurnID, &c.Message, &att, &c.Status, &c.Error, &c.CreatedAt, &c.UpdatedAt, &c.Issuer); err != nil {
		return Command{}, false, err
	}
	c.Kind = Kind(kind)
	_ = json.Unmarshal([]byte(att), &c.Attachments)
	c.readMark()
	return c, true, nil
}

// SetCommand records how a command ended.
func (s *Store) SetCommand(id int64, status, errText string) error {
	_, err := s.db.Exec(`UPDATE coder_session_commands SET status = ?, error = ?, updated_at = ? WHERE id = ?`, status, errText, s.stamp(), id)
	return err
}

const sessionCols = `profile_id, conversation_id, state, role, parent_conversation_id, unattended, turn_id, turn_pid, turn_stamp, created_at, updated_at`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var x Session
	var state string
	var un int
	err := sc.Scan(&x.ProfileID, &x.ConversationID, &state, &x.Role, &x.Parent, &un, &x.TurnID, &x.TurnPID, &x.TurnStamp, &x.CreatedAt, &x.UpdatedAt)
	x.State, x.Unattended = State(state), un != 0
	return x, err
}

// GetSession returns a session; ok is false when the host has none yet.
func (s *Store) GetSession(profileID, conversationID string) (Session, bool, error) {
	x, err := scanSession(s.db.QueryRow(`SELECT `+sessionCols+` FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	return x, err == nil, err
}

// EnsureSession returns the session, creating an idle one first.
func (s *Store) EnsureSession(profileID, conversationID string) (Session, error) {
	now := s.stamp()
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO coder_sessions (profile_id, conversation_id, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		profileID, conversationID, now, now); err != nil {
		return Session{}, err
	}
	x, _, err := s.GetSession(profileID, conversationID)
	return x, err
}

// ConversationExists says whether the conversation a command or session names is still there.
func (s *Store) ConversationExists(profileID, conversationID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`, conversationID, profileID).Scan(&n)
	return n > 0, err
}

// SweepOrphans removes the sessions and waiting commands whose conversation is gone. Deleting a
// conversation does this itself, in its transaction; this catches a row the host re-created from
// a command it had read just before the delete. It returns how many rows it removed.
func (s *Store) SweepOrphans() (int, error) {
	total := 0
	// The records of the mission sends that go are revoked once they are gone.
	defer s.revokeCommands(s.pendingMissionIDs(`NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = coder_session_commands.conversation_id AND c.profile_id = coder_session_commands.profile_id)`))
	for _, q := range []string{
		`DELETE FROM coder_session_commands WHERE status = 'pending' AND NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = coder_session_commands.conversation_id AND c.profile_id = coder_session_commands.profile_id)`,
		`DELETE FROM coder_sessions WHERE NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = coder_sessions.conversation_id AND c.profile_id = coder_sessions.profile_id)`,
		`DELETE FROM coder_session_decisions WHERE NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = coder_session_decisions.conversation_id AND c.profile_id = coder_session_decisions.profile_id)`,
		`DELETE FROM coder_session_questions WHERE NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = coder_session_questions.conversation_id AND c.profile_id = coder_session_questions.profile_id)`,
		`DELETE FROM coder_session_turn_caps WHERE NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = coder_session_turn_caps.conversation_id AND c.profile_id = coder_session_turn_caps.profile_id)`,
		`DELETE FROM coder_session_host_turns WHERE NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = coder_session_host_turns.conversation_id AND c.profile_id = coder_session_host_turns.profile_id)`,
		`DELETE FROM coder_session_tokens WHERE NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = coder_session_tokens.conversation_id AND c.profile_id = coder_session_tokens.profile_id)`,
		`DELETE FROM coder_session_spawns WHERE NOT EXISTS (SELECT 1 FROM ai_chat_conversations c WHERE c.id = coder_session_spawns.parent AND c.profile_id = coder_session_spawns.profile_id)`,
	} {
		res, err := s.db.Exec(q)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// SaveSession writes a session's state and current turn.
func (s *Store) SaveSession(x Session) error {
	if x.TurnPID < 0 || x.TurnPID == 1 {
		return fmt.Errorf("coder host: %d is not a turn's pid", x.TurnPID)
	}
	_, err := s.db.Exec(`UPDATE coder_sessions SET state = ?, turn_id = ?, turn_pid = ?, turn_stamp = ?, updated_at = ? WHERE profile_id = ? AND conversation_id = ?`,
		string(x.State), x.TurnID, x.TurnPID, x.TurnStamp, s.stamp(), x.ProfileID, x.ConversationID)
	return err
}

// ListSessions returns the sessions of a profile ("" for all), most recently
// changed first.
func (s *Store) ListSessions(profileID string) ([]Session, error) {
	q, args := `SELECT `+sessionCols+` FROM coder_sessions`, []any{}
	if profileID != "" {
		q, args = q+` WHERE profile_id = ?`, append(args, profileID)
	}
	rows, err := s.db.Query(q+` ORDER BY updated_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ConversationInfo is a conversation's folder and access level as the coder
// conversation row has them. ok is false when there is no such conversation. An
// access the row does not carry is read from its mode: a coder conversation
// without one is the full level.
func (s *Store) ConversationInfo(profileID, conversationID string) (cwd, access string, ok bool, err error) {
	var mode string
	e := s.db.QueryRow(`SELECT cwd, access, mode FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`, conversationID, profileID).Scan(&cwd, &access, &mode)
	if errors.Is(e, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if e != nil {
		return "", "", false, e
	}
	switch access {
	case "scoped", "coding", "workspace", "full":
	default:
		access = "scoped"
		if mode == "coder" {
			access = "full"
		}
	}
	return cwd, access, true, nil
}

// SetRole links a session into the master/child structure: "solo" (no links),
// "master" (may have children) or "child" of a master. It refuses what the design
// forbids: a child needs a master of the same profile that allows children, a
// master has no parent, no session is its own parent, the master's max_children
// holds, and a session that has children (stopped ones count) cannot become
// something else. The reasons come from reading first; the change itself is one
// statement that re-checks the rules, so two callers cannot both slip through.
func (s *Store) SetRole(profileID, conversationID, role, parent string) error {
	if _, err := s.EnsureSession(profileID, conversationID); err != nil {
		return err
	}
	var children int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE profile_id = ? AND parent_conversation_id = ?`, profileID, conversationID).Scan(&children); err != nil {
		return err
	}
	switch role {
	case "solo", "master":
		if parent != "" {
			return fmt.Errorf("a %s session has no parent", role)
		}
		if role == "solo" && children > 0 {
			return errors.New("this session has child sessions; move them first")
		}
		res, err := s.db.Exec(`UPDATE coder_sessions SET role = ?, parent_conversation_id = '', updated_at = ?
			WHERE profile_id = ? AND conversation_id = ?
			AND (? = 'master' OR NOT EXISTS (SELECT 1 FROM coder_sessions k WHERE k.profile_id = ? AND k.parent_conversation_id = ?))`,
			role, s.stamp(), profileID, conversationID, role, profileID, conversationID)
		return s.applied(res, err, "this session has child sessions; move them first")
	case "child":
		if parent == "" || parent == conversationID {
			return errors.New("a child needs another session as its master")
		}
		if children > 0 {
			return errors.New("this session has child sessions, so it cannot be a child itself")
		}
		p, ok, err := s.GetSession(profileID, parent)
		if err != nil {
			return err
		}
		if !ok || p.Role != "master" || p.State == StateStopped {
			return errors.New("the master must be a running master session of this profile")
		}
		ps, err := s.GetSettings(profileID, parent)
		if err != nil {
			return err
		}
		if ps.MaxDepth < 2 {
			return errors.New("the master's max_depth does not allow children")
		}
		res, err := s.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = ?, updated_at = ?
			WHERE profile_id = ? AND conversation_id = ?
			AND NOT EXISTS (SELECT 1 FROM coder_sessions k WHERE k.profile_id = ? AND k.parent_conversation_id = ?)
			AND (SELECT COUNT(*) FROM coder_sessions sib WHERE sib.profile_id = ? AND sib.parent_conversation_id = ? AND sib.conversation_id != ? AND sib.state != 'stopped') < ?
			AND EXISTS (SELECT 1 FROM coder_sessions m WHERE m.profile_id = ? AND m.conversation_id = ? AND m.role = 'master' AND m.parent_conversation_id = '' AND m.state != 'stopped')`,
			parent, s.stamp(), profileID, conversationID,
			profileID, conversationID,
			profileID, parent, conversationID, ps.MaxChildren,
			profileID, parent)
		return s.applied(res, err, fmt.Sprintf("the master cannot take another child (max_children is %d) or changed meanwhile", ps.MaxChildren))
	}
	return fmt.Errorf("role is solo, master or child, not %q", role)
}

// applied turns "no row changed" into the refusal reason.
func (s *Store) applied(res sql.Result, err error, reason string) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New(reason)
	}
	return nil
}

// Checkin is what the scheduler remembers about a master.
type Checkin struct {
	At       string // when it last looked ("" = never)
	Snapshot string // its children as it saw them then
}

// GetCheckin returns a session's check-in memory.
func (s *Store) GetCheckin(profileID, conversationID string) (Checkin, error) {
	var c Checkin
	err := s.db.QueryRow(`SELECT checkin_at, checkin_snapshot FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID).Scan(&c.At, &c.Snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return Checkin{}, nil
	}
	return c, err
}

// SaveCheckin records that the scheduler looked now, and what it saw.
func (s *Store) SaveCheckin(profileID, conversationID string, c Checkin) error {
	_, err := s.db.Exec(`UPDATE coder_sessions SET checkin_at = ?, checkin_snapshot = ? WHERE profile_id = ? AND conversation_id = ?`, c.At, c.Snapshot, profileID, conversationID)
	return err
}

// TurnActivity is the time of a running turn's newest journal event, or when the
// turn began if it has none yet; the zero time when there is no such turn.
func (s *Store) TurnActivity(profileID, conversationID, turnID string) time.Time {
	var at, began sql.NullString
	_ = s.db.QueryRow(`SELECT MAX(created_at) FROM ai_chat_events WHERE profile_id = ? AND conversation_id = ? AND turn_id = ?`, profileID, conversationID, turnID).Scan(&at)
	_ = s.db.QueryRow(`SELECT created_at FROM ai_chat_turns WHERE profile_id = ? AND conversation_id = ? AND id = ?`, profileID, conversationID, turnID).Scan(&began)
	var newest time.Time
	for _, v := range []sql.NullString{at, began} {
		if t, err := time.Parse(time.RFC3339Nano, v.String); v.Valid && err == nil && t.After(newest) {
			newest = t
		}
	}
	return newest
}

// LastTurn is a conversation's newest turn: its id and status ("" when it has none).
func (s *Store) LastTurn(profileID, conversationID string) (id, status string) {
	_ = s.db.QueryRow(`SELECT id, status FROM ai_chat_turns WHERE profile_id = ? AND conversation_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, profileID, conversationID).Scan(&id, &status)
	return id, status
}

// PendingFor counts the commands still waiting for a conversation.
func (s *Store) PendingFor(profileID, conversationID string) int {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND status = 'pending'`, profileID, conversationID).Scan(&n); err != nil {
		return 1 << 30 // unreadable counts as busy: nothing is queued on a guess
	}
	return n
}

// CheckinStreak is how many of a conversation's newest turns in a row were
// scheduled check-ins (no message from a person between them), and when the
// newest turn of any kind ended or was last touched; failed says whether the
// newest check-in turn did not complete.
func (s *Store) CheckinStreak(profileID, conversationID string) (streak int, lastTurnAt time.Time, lastFailed bool, err error) {
	rows, err := s.db.Query(`SELECT id, status, updated_at FROM ai_chat_turns WHERE profile_id = ? AND conversation_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 600`, profileID, conversationID)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	defer rows.Close()
	marked := s.hostTurnSet(profileID, conversationID)
	first := true
	for rows.Next() {
		var id, status, upd string
		if err := rows.Scan(&id, &status, &upd); err != nil {
			return 0, time.Time{}, false, err
		}
		if first {
			lastTurnAt, _ = time.Parse(time.RFC3339Nano, upd)
		}
		if !HostMade(id) && !marked[id] {
			break
		}
		if first {
			lastFailed = status == "failed" || status == "interrupted"
		}
		first = false
		streak++
	}
	return streak, lastTurnAt, lastFailed, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// MarkHostTurn records that a turn started from a send a master queued, whatever its turn id.
func (s *Store) MarkHostTurn(profileID, conversationID, turnID string) {
	_, _ = s.db.Exec(`INSERT OR IGNORE INTO coder_session_host_turns (profile_id, conversation_id, turn_id) VALUES (?, ?, ?)`, profileID, conversationID, turnID)
}

func (s *Store) hostTurnSet(profileID, conversationID string) map[string]bool {
	out := map[string]bool{}
	rows, err := s.db.Query(`SELECT turn_id FROM coder_session_host_turns WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out[id] = true
		}
	}
	return out
}
