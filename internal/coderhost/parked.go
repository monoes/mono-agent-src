package coderhost

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Parked questions: a gap that nobody present can settle is written down, the
// session goes to waiting_user, and the other sessions carry on. The question is
// answered by the one the policy names (Decide), and the answer reaches the
// session as a queued send whose turn id starts with AnswerPrefix: like a
// check-in it is host-made, counts toward the check-in limit and can never carry
// a mission confirmation (HostMade).

// AnswerPrefix starts the turn id of a send the host queues to hand an answer
// (or an applied default) to a session.
const AnswerPrefix = "answer-"

// Bounds.
const (
	// ParkTimeout is how long a question waits for its answerer before its
	// timeout fallback applies.
	ParkTimeout           = 30 * time.Minute
	MaxOpenQuestions      = 20
	maxQuestionText       = 2000
	maxOptions            = 10
	maxOptionText         = 500
	MaxAnswerBytes        = maxQuotedAnswer // what is quoted to the session: a longer answer would be cut
	questionStatusOpen    = "open"
	questionStatusAnswer  = "answered"
	questionStatusDefault = "defaulted"
)

// CallerScope says who is answering. The operator's is OperatorScope; a master's is made from
// its verified session token (AnswerAsMaster), never from an argument.
type CallerScope interface {
	Role() string           // WhoUser or WhoMaster
	ConversationID() string // the master's own conversation; "" for the operator
}

// OperatorScope is the person at the operator CLI.
type OperatorScope struct{}

// Role is WhoUser.
func (OperatorScope) Role() string { return WhoUser }

// ConversationID is empty: the operator is no conversation.
func (OperatorScope) ConversationID() string { return "" }

// Question is a parked question.
type Question struct {
	ID             int64    `json:"id"`
	ProfileID      string   `json:"profileId"`
	ConversationID string   `json:"conversationId"`
	Class          string   `json:"class"`
	Question       string   `json:"question"`
	Options        []string `json:"options"`
	Default        string   `json:"default,omitempty"`
	Reversible     bool     `json:"reversible"`
	Status         string   `json:"status"` // open | answered | defaulted
	Answer         string   `json:"answer,omitempty"`
	AnsweredBy     string   `json:"answeredBy,omitempty"`
	CommandID      int64    `json:"commandId,omitempty"`
	CreatedAt      string   `json:"createdAt"`
	AnsweredAt     string   `json:"answeredAt,omitempty"`
	// Answerer is who the policy names now; it is filled by ListQuestions and Park, not stored.
	Answerer string `json:"answerer,omitempty"`
}

// Errors.
var (
	// ErrModelDecides: the policy lets the model settle this gap, so there is
	// nothing to park; the caller decides and writes it to the ledger.
	ErrModelDecides = errors.New("coder host: the policy lets the model decide this gap; nothing to park")
	ErrTooManyOpen  = errors.New("coder host: too many questions are already parked for this conversation")
	ErrNoQuestion   = errors.New("coder host: no such question")
	ErrNotOpen      = errors.New("coder host: this question was already answered")
	ErrNotAnswerer  = errors.New("coder host: the policy names someone else to answer this question")
	// ErrWaitsForPerson: the session has had its host-made turns in a row; a master's answer, like a
	// check-in, would start another, so the session waits for a person (the operator can still answer).
	ErrWaitsForPerson = errors.New("coder host: the session has had its host-made turns in a row and waits for a person")
	// ErrAnswerKeepsFailing: the notes carrying this question's answer failed MaxAnswerReopens times.
	ErrAnswerKeepsFailing = errors.New("coder host: the notes answering this question keep failing; it is left for a person")
	// ErrMasterStopped: the master that would answer is stopped or gone.
	ErrMasterStopped = errors.New("coder host: the session's master is stopped or gone, so it cannot answer")
	// ErrStopped: a stopped session starts no turn, so an answer to it would be dropped.
	ErrStopped        = errors.New("coder host: the session is stopped, so it cannot take an answer; resume it with a message first")
	ErrNoConversation = errors.New("coder host: the conversation no longer exists")
)

const questionCols = `id, profile_id, conversation_id, class, question, options, default_answer, reversible, status, answer, answered_by, command_id, created_at, answered_at`

func scanQuestion(sc interface{ Scan(...any) error }) (Question, error) {
	var q Question
	var opts string
	var rev int
	err := sc.Scan(&q.ID, &q.ProfileID, &q.ConversationID, &q.Class, &q.Question, &opts, &q.Default, &rev, &q.Status, &q.Answer, &q.AnsweredBy, &q.CommandID, &q.CreatedAt, &q.AnsweredAt)
	q.Reversible = rev != 0
	q.Options = []string{}
	_ = json.Unmarshal([]byte(opts), &q.Options)
	return q, err
}

// hasMaster says whether the session is a child with a parent to ask.
func (s *Store) hasMaster(profileID, conversationID string) (parent string, ok bool) {
	x, found, err := s.GetSession(profileID, conversationID)
	if err != nil || !found || x.Role != "child" || x.Parent == "" {
		return "", false
	}
	return x.Parent, true
}

// answererNow is who may answer a parked question under the settings as they are
// now. A question already parked never goes to the model by way of an answer:
// when the policy now says model, a master answers it (or the user, without one).
func (s *Store) answererNow(q Question) (string, error) {
	settings, err := s.GetSettings(q.ProfileID, q.ConversationID)
	if err != nil {
		return "", err
	}
	_, has := s.hasMaster(q.ProfileID, q.ConversationID)
	who := Decide(settings, q.Class, q.Reversible, has).Answerer
	if who == WhoModel {
		who = WhoMaster
		if !has {
			who = WhoUser
		}
	}
	return who, nil
}

// Park writes a question nobody present can settle and puts the session into
// waiting_user (an idle session; a running one gets there when its turn ends,
// a paused or stopped one keeps its state). It returns ErrModelDecides when
// the policy lets the model settle this class, since then nobody needs to be
// asked. def is what the host applies after ParkTimeout, only where the
// policy's fallback is take_default.
//
// TRUST: class and reversible come from the caller and the policy trusts them. A model-facing
// caller goes through ParkOwn (producer.go), which validates the class and never lets a hard
// class be reversible.
func (s *Store) Park(profileID, conversationID, class, question string, options []string, def string, reversible bool) (Question, error) {
	if profileID == "" || conversationID == "" || len(conversationID) > maxConversationLen {
		return Question{}, errors.New("coder host: a question needs a profile and a conversation")
	}
	if !oneOf(class, GapClasses) {
		return Question{}, fmt.Errorf("coder host: %q is not a class of gap", class)
	}
	if strings.TrimSpace(question) == "" {
		return Question{}, errors.New("coder host: a question needs a text")
	}
	if ok, err := s.ConversationExists(profileID, conversationID); err != nil || !ok {
		if err != nil {
			return Question{}, err
		}
		return Question{}, ErrNoConversation // never bring a session back for a deleted conversation
	}
	settings, err := s.GetSettings(profileID, conversationID)
	if err != nil {
		return Question{}, err
	}
	_, has := s.hasMaster(profileID, conversationID)
	if Decide(settings, class, reversible, has).Answerer == WhoModel {
		return Question{}, ErrModelDecides
	}
	if s.openCount(profileID, conversationID) >= MaxOpenQuestions {
		return Question{}, ErrTooManyOpen
	}
	opts := []string{}
	for _, o := range options {
		if len(opts) == maxOptions {
			break
		}
		opts = append(opts, trimTo(o, maxOptionText))
	}
	raw, _ := json.Marshal(opts)
	if _, err := s.EnsureSession(profileID, conversationID); err != nil {
		return Question{}, err
	}
	rev := 0
	if reversible {
		rev = 1
	}
	now := s.stamp()
	res, err := s.db.Exec(`INSERT INTO coder_session_questions (profile_id, conversation_id, class, question, options, default_answer, reversible, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'open', ?)`, profileID, conversationID, class, trimTo(question, maxQuestionText), string(raw), trimTo(def, maxQuestionText), rev, now)
	if err != nil {
		return Question{}, err
	}
	id, _ := res.LastInsertId()
	// Only an idle session moves: a running turn settles into waiting_user itself, and
	// pause and stop keep their meaning.
	if _, err := s.db.Exec(`UPDATE coder_sessions SET state = 'waiting_user', updated_at = ? WHERE profile_id = ? AND conversation_id = ? AND state = 'idle'`, now, profileID, conversationID); err != nil {
		return Question{}, err
	}
	s.pruneClosedQuestions(profileID, conversationID)
	return s.GetQuestion(profileID, id)
}

func (s *Store) openCount(profileID, conversationID string) int {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_questions WHERE profile_id = ? AND conversation_id = ? AND status = 'open'`, profileID, conversationID).Scan(&n); err != nil {
		return 1 << 30
	}
	return n
}

// OpenParked counts a conversation's questions still waiting for an answer.
func (s *Store) OpenParked(profileID, conversationID string) int {
	return s.openCount(profileID, conversationID)
}

// GetQuestion returns one question of a profile.
func (s *Store) GetQuestion(profileID string, id int64) (Question, error) {
	q, err := scanQuestion(s.db.QueryRow(`SELECT `+questionCols+` FROM coder_session_questions WHERE profile_id = ? AND id = ?`, profileID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Question{}, ErrNoQuestion
	}
	if err != nil {
		return Question{}, err
	}
	q.Answerer, _ = s.answererNow(q)
	return q, nil
}

// ListQuestions returns a profile's questions, newest first; conversationID ""
// means every conversation, and onlyOpen leaves out the answered ones.
func (s *Store) ListQuestions(profileID, conversationID string, onlyOpen bool) ([]Question, error) {
	q, args := `SELECT `+questionCols+` FROM coder_session_questions WHERE profile_id = ?`, []any{profileID}
	if conversationID != "" {
		q, args = q+` AND conversation_id = ?`, append(args, conversationID)
	}
	if onlyOpen {
		q += ` AND status = 'open'`
	}
	rows, err := s.db.Query(q+` ORDER BY id DESC LIMIT ?`, append(args, listLimitMax)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Question{}
	for rows.Next() {
		x, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Status == questionStatusOpen {
			out[i].Answerer, _ = s.answererNow(out[i])
		}
	}
	return out, nil
}

// Answer settles an open question. It is allowed only for the answerer the
// policy names now (and for a master, only the session's own master); the
// answer is queued to the session as a send with an AnswerPrefix turn id. It
// carries no authority beyond text: it cannot change access, a budget, a
// setting, or pass a send gate, and it can never be a mission confirmation.
func (s *Store) Answer(profileID string, id int64, answer string, scope CallerScope) (Question, Command, error) {
	if scope == nil {
		return Question{}, Command{}, ErrNotAnswerer
	}
	if strings.TrimSpace(answer) == "" || len(answer) > MaxAnswerBytes {
		return Question{}, Command{}, fmt.Errorf("coder host: an answer is 1 to %d bytes", MaxAnswerBytes)
	}
	q, err := s.GetQuestion(profileID, id)
	if err != nil {
		return q, Command{}, err
	}
	if q.Status != questionStatusOpen {
		return q, Command{}, ErrNotOpen
	}
	who, err := s.answererNow(q)
	if err != nil {
		return q, Command{}, err
	}
	if scope.Role() != who {
		return q, Command{}, fmt.Errorf("%w (the policy names the %s)", ErrNotAnswerer, who)
	}
	if who == WhoMaster {
		if parent, ok := s.hasMaster(q.ProfileID, q.ConversationID); !ok || scope.ConversationID() != parent {
			return q, Command{}, ErrNotAnswerer
		}
	}
	if ok, err := s.ConversationExists(q.ProfileID, q.ConversationID); err != nil || !ok {
		if err != nil {
			return q, Command{}, err
		}
		return q, Command{}, ErrNoConversation
	}
	if x, found, err := s.GetSession(q.ProfileID, q.ConversationID); err != nil {
		return q, Command{}, err
	} else if found && x.State == StateStopped {
		return q, Command{}, ErrStopped
	}
	if who == WhoMaster {
		settings, err := s.GetSettings(q.ProfileID, q.ConversationID)
		if err != nil {
			return q, Command{}, err
		}
		if streak, _, _, err := s.CheckinStreak(q.ProfileID, q.ConversationID); err != nil {
			return q, Command{}, err
		} else if streak+s.pendingGatedAnswers(q.ProfileID, q.ConversationID) >= settings.CheckinLimit {
			return q, Command{}, ErrWaitsForPerson
		}
		if n, _ := s.reopenCount(q.ID); n >= MaxAnswerReopens {
			return q, Command{}, ErrAnswerKeepsFailing
		}
		if n, _ := s.expiredReopenCount(q.ID); n >= MaxExpiredReopens {
			return q, Command{}, ErrAnswerKeepsFailing
		}
		// A paused master's note is held by the host until it resumes; a stopped one's is cancelled.
		if m, ok, err := s.GetSession(q.ProfileID, scope.ConversationID()); err != nil {
			return q, Command{}, err
		} else if !ok || m.State == StateStopped {
			return q, Command{}, ErrMasterStopped
		}
	}
	by := scope.Role()
	if by == WhoMaster {
		by += ":" + scope.ConversationID() // the master's own conversation, as verified above
	}
	if err := s.claimQuestion(q, questionStatusAnswer, answer, by); err != nil {
		return q, Command{}, err
	}
	q.Status, q.Answer, q.AnsweredBy = questionStatusAnswer, answer, by
	cmd, err := s.deliver(q)
	if err != nil {
		if cmd.ID == 0 { // nothing was queued: the question stays open (a queued note whose id was not recorded is redelivered, at least once)
			s.reopenQuestion(q.ID)
		}
		return q, Command{}, err
	}
	s.releaseWaiting(q.ProfileID, q.ConversationID)
	s.pruneClosedQuestions(q.ProfileID, q.ConversationID)
	q.CommandID = cmd.ID
	return q, cmd, nil
}

// claimQuestion closes an open question atomically, so two answers (or an answer
// and a default) cannot both win.
func (s *Store) claimQuestion(q Question, status, answer, by string) error {
	res, err := s.db.Exec(`UPDATE coder_session_questions SET status = ?, answer = ?, answered_by = ?, answered_at = ? WHERE id = ? AND status = 'open'`,
		status, trimTo(answer, MaxAnswerBytes), by, s.stamp(), q.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotOpen
	}
	return nil
}

func (s *Store) reopenCount(id int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT reopen_count FROM coder_session_questions WHERE id = ?`, id).Scan(&n)
	return n, err
}

func (s *Store) expiredReopenCount(id int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT expired_reopens FROM coder_session_questions WHERE id = ?`, id).Scan(&n)
	return n, err
}

func (s *Store) reopenQuestion(id int64) {
	_, _ = s.db.Exec(`UPDATE coder_session_questions SET status = 'open', answer = '', answered_by = '', answered_at = '' WHERE id = ? AND status != 'open' AND command_id = 0`, id)
}

// releaseWaiting returns a waiting_user session to idle once no question is open.
func (s *Store) releaseWaiting(profileID, conversationID string) {
	if s.openCount(profileID, conversationID) > 0 {
		return
	}
	_, _ = s.db.Exec(`UPDATE coder_sessions SET state = 'idle', updated_at = ? WHERE profile_id = ? AND conversation_id = ? AND state = 'waiting_user'`, s.stamp(), profileID, conversationID)
}
