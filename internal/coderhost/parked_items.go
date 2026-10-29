package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// Person items: a request of a session that only the person answers, and that no timeout, default,
// master or model ever answers. An item links to the row it is about by (kind, ref_id); it is made
// when the row is made (a hard-class question always has one) and has no status of its own. Whether
// it is OPEN, that is whether the person has to act now, is derived each time from the row and the
// session as they are now, so a change of role, a stopped master or a stopped session can neither
// leave an unanswerable item counted nor an answerable one unseen. A new kind adds an entry to
// itemKinds (how its open items are found) and its own answer path.
//
// The item carries no text. What the person is shown comes from the row it points at, which the
// host has validated and cleaned.

// Kinds of person item.
const ItemParkedQuestion = "parked_question"

// MaxPersonItemsPerHour is how many hard parks a session's own model may make an hour, whatever its
// master, so a model cannot inflate what "needs you" shows.
const MaxPersonItemsPerHour = 3

// ErrPersonItemRate: the session has parked as many hard questions in the last hour as it may.
var ErrPersonItemRate = fmt.Errorf("%w: a session may park at most %d risky questions an hour", ErrLimit, MaxPersonItemsPerHour)

type itemKind struct {
	// open lists this kind's items of a profile that the person has to act on now.
	open func(s *Store, profileID string) ([]PersonItem, error)
}

var itemKinds = map[string]itemKind{
	ItemParkedQuestion: {open: (*Store).openParkedQuestionItems},
	ItemBudgetRequest:  {open: (*Store).openBudgetItems},
}

// PersonItem is a request waiting for the person.
type PersonItem struct {
	ID             int64  `json:"id"`
	ProfileID      string `json:"profileId"`
	ConversationID string `json:"conversationId"`
	Kind           string `json:"kind"`
	RefID          int64  `json:"refId"`
	CreatedAt      string `json:"createdAt"`
}

// addPersonItem links a hard-class question to the person. It is idempotent per (kind, ref).
func (s *Store) addPersonItem(profileID, conversationID, kind, class string, ref int64) error {
	return addPersonItem(s.db, profileID, conversationID, kind, class, ref, s.stamp())
}

// execer is a database or a transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func addPersonItem(x execer, profileID, conversationID, kind, class string, ref int64, stamp string) error {
	if _, ok := itemKinds[kind]; !ok {
		return fmt.Errorf("coder host: %q is not a kind of item", kind)
	}
	if kind == ItemParkedQuestion && !HardClass(class) {
		return errors.New("coder host: only a risky_action or missing_credential question is a person item")
	}
	_, err := x.Exec(`INSERT OR IGNORE INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		profileID, conversationID, kind, ref, stamp)
	return err
}

// hasPersonItem says whether the question is a person item: then no default is ever taken for it.
func (s *Store) hasPersonItem(profileID string, questionID int64) bool {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_person_items WHERE profile_id = ? AND kind = ? AND ref_id = ?`,
		profileID, ItemParkedQuestion, questionID).Scan(&n); err != nil {
		return true // unreadable counts as an item: the safe direction
	}
	return n > 0
}

// OpenPersonItems lists the profile's items that the person has to act on now, oldest first.
func (s *Store) OpenPersonItems(profileID string) ([]PersonItem, error) {
	var out []PersonItem
	for _, k := range itemKinds {
		items, err := k.open(s, profileID)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// openParkedQuestionItems: the question is open, the person is who has to answer it now (see
// personOwns) and its session is not stopped (a stopped session takes no answer).
func (s *Store) openParkedQuestionItems(profileID string) ([]PersonItem, error) {
	rows, err := s.db.Query(`SELECT i.id, i.conversation_id, i.ref_id, i.created_at FROM coder_session_person_items i
		JOIN coder_session_questions q ON q.id = i.ref_id AND q.profile_id = i.profile_id
		WHERE i.profile_id = ? AND i.kind = ? AND q.status = 'open' ORDER BY i.id LIMIT ?`, profileID, ItemParkedQuestion, listLimitMax)
	if err != nil {
		return nil, err
	}
	var cand []PersonItem
	for rows.Next() {
		it := PersonItem{ProfileID: profileID, Kind: ItemParkedQuestion}
		if err := rows.Scan(&it.ID, &it.ConversationID, &it.RefID, &it.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		cand = append(cand, it)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []PersonItem
	for _, it := range cand {
		q, err := s.GetQuestion(profileID, it.RefID)
		if err != nil { // unreadable: show it, the safe direction
			if !s.sessionStopped(profileID, it.ConversationID) {
				out = append(out, it)
			}
			continue
		}
		if s.personOwns(q) && !s.sessionStopped(profileID, it.ConversationID) {
			out = append(out, it)
		}
	}
	return out, nil
}

// dropStaleItems removes a conversation's parked-question items whose question is gone.
func (s *Store) dropStaleItems(profileID, conversationID string) {
	_, _ = s.db.Exec(`DELETE FROM coder_session_person_items WHERE profile_id = ? AND conversation_id = ? AND kind = ?
		AND ref_id NOT IN (SELECT id FROM coder_session_questions WHERE profile_id = ?)`, profileID, conversationID, ItemParkedQuestion, profileID)
}

// personOwns says whether the person has to answer q now. A risky or credential question waits for
// the person: its answerer is the user, or it is the master's but the master cannot take it (paused,
// at the check-in limit, or the notes carrying its answer keep failing), so nobody else could.
func (s *Store) personOwns(q Question) bool {
	if q.Answerer == WhoUser {
		return true
	}
	return q.Answerer == WhoMaster && HardClass(q.Class) && s.masterBlocked(q)
}

// masterBlocked says whether q's master cannot answer it now. Anything unreadable counts as blocked:
// the safe direction is that the person is told.
func (s *Store) masterBlocked(q Question) bool {
	parent, ok := s.hasLiveMaster(q.ProfileID, q.ConversationID)
	if !ok {
		return true
	}
	if m, found, err := s.GetSession(q.ProfileID, parent); err != nil || !found || m.State == StatePaused {
		return true
	}
	settings, err := s.GetSettings(q.ProfileID, q.ConversationID)
	if err != nil {
		return true
	}
	streak, _, _, err := s.CheckinStreak(q.ProfileID, q.ConversationID)
	if err != nil || streak+s.pendingGatedAnswers(q.ProfileID, q.ConversationID) >= settings.CheckinLimit {
		return true
	}
	if n, err := s.reopenCount(q.ID); err != nil || n >= MaxAnswerReopens {
		return true
	}
	if n, err := s.expiredReopenCount(q.ID); err != nil || n >= MaxExpiredReopens {
		return true
	}
	return s.keepsFailing(q, s.now())
}
