package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The upward ASK channel (docs/mastermind/plans/2026-10-28-mission-research-directions.md, sections 3, 5, 8).
// Approval stays with the parent only. When a lead wants wider context it may ask a PROPER ANCESTOR above
// its parent (grandparent by default, the root, or one named by session id); the parent is reached with the
// existing messaging. A normal ask reaches the ancestor lead's chat as sealed, untrusted peer content that
// carries no authority; the reply (session_reply_ask of the ancestor, see ask_reply_tool.go) reaches the asker sealed
// too. A critical ask is not delivered to any model: it becomes a person item that only the person answers.
// The caller is the lead of a mission, identified by LeadScope (verified session token, no task binding);
// the tool names no caller, and the ancestor is found by the parent_session_id chain read from the database.

// Importance of an ask.
const (
	AskNormal   = "normal"
	AskCritical = "critical"
)

// Ask states.
const (
	askOpen     = "open"
	askAnswered = "answered"
	askExpired  = "expired"
	askVoid     = "void"
)

// ItemAncestorQuestion is a critical ask that only the person answers. ref_id is the coder_session_asks id.
// It is open while the ask is open and the asker's chat is not stopped; no timeout, default, master or model
// answers it, and the autonomy matrix does not apply to it.
const ItemAncestorQuestion = "ancestor_question"

// ClassAncestorQuestion is the ledger class of an ask. It is deliberately not in GapClasses: no preset or
// per-class override can route it, and no model can record it through session_record.
const ClassAncestorQuestion = "ancestor_question"

// Bounds.
const (
	MaxAskBytes          = 4096
	MaxOpenAsks          = 3
	MaxOpenAskBytes      = 2 * MaxAskBytes
	MaxOpenAsksPerTarget = 6
	AskInterval          = time.Minute
	AskTTL               = 6 * time.Hour // a normal ask nobody answered in this time stops counting
)

// ErrAskRate: a rate or size limit of the channel holds. It wraps ErrLimit.
var ErrAskRate = fmt.Errorf("%w: ask limit", ErrLimit)

// errNotAncestor is the one answer for every target that is not a proper ancestor above the parent.
var errNotAncestor = fmt.Errorf("%w: that is not an ancestor of your session above your parent", ErrNotYours)

// AskRequest is what the lead gives. Level is "" or "grandparent", "root", or an ancestor session id.
type AskRequest struct {
	Question   string
	Level      string
	Importance string // "" = normal
}

// Asked is the result of an ask.
type Asked struct {
	AskID           int64  `json:"askId"`
	Importance      string `json:"importance"`
	AncestorSession int64  `json:"ancestorSession"`
	AncestorTitle   string `json:"ancestorTitle"`
	Delivery        string `json:"delivery"` // lead | person
	CommandID       int64  `json:"commandId,omitempty"`
}

// Ask is a stored ask, for the person's view.
type Ask struct {
	ID              int64  `json:"id"`
	ProfileID       string `json:"profileId"`
	AskerSession    int64  `json:"askerSession"`
	AskerConv       string `json:"askerConversationId"`
	AskerTitle      string `json:"askerTitle"`
	AncestorSession int64  `json:"ancestorSession"`
	AncestorConv    string `json:"ancestorConversationId,omitempty"`
	AncestorTitle   string `json:"ancestorTitle"`
	Level           string `json:"level"`
	Importance      string `json:"importance"`
	Question        string `json:"question"`
	Status          string `json:"status"`
	CreatedAt       string `json:"createdAt"`
}

type treeNode struct {
	id       int64
	conv     string
	title    string
	state    string
	archived bool
}

// ancestorsOf lists the sessions above sid in the profile, nearest (the parent) first. A cycle or a chain
// longer than maxTreeWalk ends the walk with an error.
func (s *Store) ancestorsOf(profileID string, sid int64) ([]treeNode, error) {
	var out []treeNode
	seen := map[int64]bool{sid: true}
	cur := sid
	for i := 0; i < maxTreeWalk; i++ {
		var parent sql.NullInt64
		err := s.db.QueryRow(`SELECT parent_session_id FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, cur).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && parent.Int64 == 0) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if seen[parent.Int64] {
			return nil, fmt.Errorf("%w: the session tree has a cycle", ErrInvalid)
		}
		seen[parent.Int64] = true
		var n treeNode
		var conv sql.NullString
		var archived string
		err = s.db.QueryRow(`SELECT id, conversation_id, title, state, archived_at FROM task_sessions WHERE profile_id = ? AND id = ?`,
			profileID, parent.Int64).Scan(&n.id, &conv, &n.title, &n.state, &archived)
		if errors.Is(err, sql.ErrNoRows) {
			return out, nil // a parent of another profile or a missing one ends the chain
		}
		if err != nil {
			return nil, err
		}
		n.conv, n.archived = conv.String, archived != ""
		out = append(out, n)
		cur = n.id
	}
	return nil, fmt.Errorf("%w: the session tree is deeper than %d levels", ErrInvalid, maxTreeWalk)
}

// pickAncestor chooses the target among the ancestors. The nearest (the parent) is never a target, however
// it is named: asking the parent is the existing messaging.
func pickAncestor(anc []treeNode, level string) (treeNode, error) {
	if len(anc) == 0 {
		return treeNode{}, fmt.Errorf("%w: your session has no ancestor: ask the person with session_park", ErrInvalid)
	}
	errParent := fmt.Errorf("%w: that is your parent: write to it with the existing messaging; this channel reaches the sessions above it", ErrInvalid)
	switch strings.TrimSpace(level) {
	case "", "grandparent":
		if len(anc) < 2 {
			return treeNode{}, fmt.Errorf("%w: your session has no grandparent: ask your parent, or park the question for the person", ErrInvalid)
		}
		return anc[1], nil
	case "root":
		if len(anc) == 1 {
			return treeNode{}, errParent
		}
		return anc[len(anc)-1], nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(level), 10, 64)
	if err != nil || id <= 0 {
		return treeNode{}, fmt.Errorf("%w: level is grandparent, root or the id of an ancestor session", ErrInvalid)
	}
	for i, n := range anc {
		if n.id == id {
			if i == 0 {
				return treeNode{}, errParent
			}
			return n, nil
		}
	}
	return treeNode{}, errNotAncestor
}

// AskAncestor is session_ask_ancestor. sc must be the LeadScope of a verified lead with no task binding.
func (s *Store) AskAncestor(sc CallerScope, profileID string, req AskRequest) (Asked, error) {
	sid, err := s.LeadMission(sc, profileID)
	if err != nil {
		return Asked{}, err
	}
	l, _, err := s.leadOf(sc, profileID, sid)
	if err != nil {
		return Asked{}, err
	}
	imp := req.Importance
	if imp == "" {
		imp = AskNormal
	}
	if imp != AskNormal && imp != AskCritical {
		return Asked{}, fmt.Errorf("%w: importance is normal or critical", ErrInvalid)
	}
	if len(req.Question) > MaxAskBytes {
		return Asked{}, fmt.Errorf("%w: the question is %d bytes; the limit is %d", ErrInvalid, len(req.Question), MaxAskBytes)
	}
	question := cleanText(req.Question, MaxAskBytes)
	if question == "" {
		return Asked{}, fmt.Errorf("%w: an ask needs a question", ErrInvalid)
	}
	anc, err := s.ancestorsOf(profileID, sid)
	if err != nil {
		return Asked{}, err
	}
	target, err := pickAncestor(anc, req.Level)
	if err != nil {
		return Asked{}, err
	}
	if target.archived || (target.state != "draft" && target.state != "running") {
		return Asked{}, fmt.Errorf("%w: that ancestor session is %s and takes no question", ErrInvalid, map[bool]string{true: "archived", false: target.state}[target.archived])
	}
	askerConv := l.Caller.ConversationID
	if imp == AskNormal {
		if target.conv == "" || target.conv == askerConv {
			return Asked{}, fmt.Errorf("%w: that ancestor session has no other lead chat to read the question: mark it critical to reach the person", ErrInvalid)
		}
		if ok, err := s.ConversationExists(profileID, target.conv); err != nil || !ok || s.sessionStopped(profileID, target.conv) {
			return Asked{}, fmt.Errorf("%w: that ancestor's chat is not there to read the question: mark it critical to reach the person", ErrInvalid)
		}
	}
	level := strings.TrimSpace(req.Level)
	if level == "" {
		level = "grandparent"
	}
	askRow, err := s.insertAsk(profileID, sid, askerConv, target, level, imp, question, len(req.Question))
	if err != nil {
		return Asked{}, err
	}
	out := Asked{AskID: askRow, Importance: imp, AncestorSession: target.id, AncestorTitle: cleanText(target.title, 80)}
	if imp == AskCritical {
		out.Delivery = "person"
		if err := addPersonItem(s.db, profileID, askerConv, ItemAncestorQuestion, "", askRow, s.stamp()); err != nil {
			s.dropAsk(askRow)
			return Asked{}, err
		}
		s.recordAsk(profileID, askerConv, target, level, imp, len(req.Question), "raised a person item; no model reads it")
		return out, nil
	}
	out.Delivery = "lead"
	asker := s.titleOf(profileID, sid)
	cmd, err := s.Enqueue(Command{ProfileID: profileID, ConversationID: target.conv, Kind: KindSend, TurnID: askTurnID(askFromAsker),
		Message: askText(askRow, askerConv, asker, level, question)})
	if err != nil {
		s.dropAsk(askRow)
		return Asked{}, err
	}
	out.CommandID = cmd.ID
	_, _ = s.db.Exec(`UPDATE coder_session_asks SET command_id = ? WHERE id = ?`, cmd.ID, askRow)
	s.recordAsk(profileID, askerConv, target, level, imp, len(req.Question), "queued to the ancestor lead as sealed untrusted content")
	return out, nil
}

// insertAsk writes the row and enforces the limits in the INSERT itself, so concurrent asks cannot pass them
// together. A refusal names the limit that held.
func (s *Store) insertAsk(profileID string, asker int64, askerConv string, target treeNode, level, imp, question string, size int) (int64, error) {
	now := s.now()
	s.expireAsks(profileID, now)
	res, err := s.db.Exec(`INSERT INTO coder_session_asks (profile_id, asker_session, asker_conv, ancestor_session, ancestor_conv, level, importance, question, bytes, status, created_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?
		WHERE (SELECT COUNT(*) FROM coder_session_asks WHERE profile_id = ? AND asker_conv = ? AND status = 'open') < ?
		AND (SELECT COALESCE(SUM(bytes), 0) FROM coder_session_asks WHERE profile_id = ? AND asker_conv = ? AND status = 'open') + ? <= ?
		AND NOT EXISTS (SELECT 1 FROM coder_session_asks WHERE profile_id = ? AND asker_conv = ? AND created_at >= ?)
		AND (? != 'normal' OR (SELECT COUNT(*) FROM coder_session_asks WHERE profile_id = ? AND ancestor_conv = ? AND importance = 'normal' AND status = 'open') < ?)`,
		profileID, asker, askerConv, target.id, target.conv, level, imp, question, size, s.stamp(),
		profileID, askerConv, MaxOpenAsks,
		profileID, askerConv, size, MaxOpenAskBytes,
		profileID, askerConv, now.Add(-AskInterval).UTC().Format(timeLayout),
		imp, profileID, target.conv, MaxOpenAsksPerTarget)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, s.whyAskRefused(profileID, askerConv, target, size, now)
	}
	return res.LastInsertId()
}

func (s *Store) whyAskRefused(profileID, askerConv string, target treeNode, size int, now time.Time) error {
	var open, bytes int
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(bytes), 0) FROM coder_session_asks WHERE profile_id = ? AND asker_conv = ? AND status = 'open'`, profileID, askerConv).Scan(&open, &bytes)
	switch {
	case open >= MaxOpenAsks:
		return fmt.Errorf("%w: you have %d open asks (the limit is %d): wait for a reply", ErrAskRate, open, MaxOpenAsks)
	case bytes+size > MaxOpenAskBytes:
		return fmt.Errorf("%w: your open asks hold %d bytes; with this one they would pass %d", ErrAskRate, bytes, MaxOpenAskBytes)
	}
	var recent int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_asks WHERE profile_id = ? AND asker_conv = ? AND created_at >= ?`,
		profileID, askerConv, now.Add(-AskInterval).UTC().Format(timeLayout)).Scan(&recent)
	if recent > 0 {
		return fmt.Errorf("%w: one ask a minute: wait and ask again", ErrAskRate)
	}
	return fmt.Errorf("%w: that ancestor already has %d open asks from below: ask later, or mark it critical", ErrAskRate, MaxOpenAsksPerTarget)
}

// expireAsks closes normal asks nobody answered within AskTTL, so a lead that never replies does not hold
// the asker's slots for ever. A critical ask waits for the person and never expires.
func (s *Store) expireAsks(profileID string, now time.Time) {
	_, _ = s.db.Exec(`UPDATE coder_session_asks SET status = ? WHERE profile_id = ? AND status = 'open' AND importance = 'normal' AND created_at < ?`,
		askExpired, profileID, now.Add(-AskTTL).UTC().Format(timeLayout))
}

func (s *Store) dropAsk(id int64) {
	_, _ = s.db.Exec(`DELETE FROM coder_session_asks WHERE id = ?`, id)
}

func (s *Store) titleOf(profileID string, sid int64) string {
	var t string
	_ = s.db.QueryRow(`SELECT title FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, sid).Scan(&t)
	return t
}

// recordAsk writes the ask in the decisions ledger of the asker's chat: who, level, importance, size and
// what was decided. It never holds the question text.
func (s *Store) recordAsk(profileID, askerConv string, target treeNode, level, imp string, size int, decision string) {
	_, _ = s.Record(LedgerEntry{ProfileID: profileID, ConversationID: askerConv, Class: ClassAncestorQuestion, MadeBy: WhoModel,
		Question: fmt.Sprintf("ask ancestor session #%d (%s) level=%s importance=%s size=%d bytes", target.id, cleanText(target.title, 80), level, imp, size),
		Choice:   decision})
}

// GetAsk reads one ask, for the person's view of an item.
func (s *Store) GetAsk(profileID string, id int64) (Ask, error) {
	var a Ask
	var conv sql.NullString
	err := s.db.QueryRow(`SELECT id, profile_id, asker_session, asker_conv, ancestor_session, ancestor_conv, level, importance, question, status, created_at
		FROM coder_session_asks WHERE profile_id = ? AND id = ?`, profileID, id).
		Scan(&a.ID, &a.ProfileID, &a.AskerSession, &a.AskerConv, &a.AncestorSession, &conv, &a.Level, &a.Importance, &a.Question, &a.Status, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, fmt.Errorf("%w: no such ask", ErrNoQuestion)
	}
	if err != nil {
		return a, err
	}
	a.AncestorConv = conv.String
	a.AskerTitle, a.AncestorTitle = cleanText(s.titleOf(profileID, a.AskerSession), 80), cleanText(s.titleOf(profileID, a.AncestorSession), 80)
	return a, nil
}
