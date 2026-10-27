package coderhost

import (
	"errors"
	"fmt"
)

// Person items: a request of a session that only the person answers, and that no timeout, default,
// master or model ever answers. An item links to the row it is about by (kind, ref_id) and has no
// status of its own: it is open exactly while that row is open, so answering the row (through any
// surface) closes the item and the answer is delivered once, by that kind's own path. A new kind
// adds an entry to itemKinds (what "open" means for it, how it is answered) and nothing else.
//
// The item carries no text. What the person is shown comes from the row it points at, which the
// host has validated and cleaned.

// Kinds of person item.
const ItemParkedQuestion = "parked_question"

// ErrUnknownItem: no such item, or of another profile.
var ErrUnknownItem = errors.New("coder host: no such item")

type itemKind struct {
	// openSQL selects the ids of this kind's open rows of a profile (? = profile id).
	openSQL string
	// answer settles the row ref with the person's answer by the kind's own delivery path.
	answer func(s *Store, profileID string, ref int64, answer string) (Command, error)
}

var itemKinds = map[string]itemKind{
	ItemParkedQuestion: {
		openSQL: `SELECT id FROM coder_session_questions WHERE profile_id = ? AND status = 'open'`,
		answer: func(s *Store, profileID string, ref int64, answer string) (Command, error) {
			_, cmd, err := s.Answer(profileID, ref, answer, OperatorScope{})
			return cmd, err
		},
	},
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

// addPersonItem links a request to the person. It is idempotent per (kind, ref).
func (s *Store) addPersonItem(profileID, conversationID, kind string, ref int64) error {
	if _, ok := itemKinds[kind]; !ok {
		return fmt.Errorf("coder host: %q is not a kind of item", kind)
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		profileID, conversationID, kind, ref, s.stamp())
	return err
}

// OpenPersonItems lists the profile's items that are still open.
func (s *Store) OpenPersonItems(profileID string) ([]PersonItem, error) {
	var out []PersonItem
	for kind, k := range itemKinds {
		rows, err := s.db.Query(`SELECT id, profile_id, conversation_id, kind, ref_id, created_at FROM coder_session_person_items
			WHERE profile_id = ? AND kind = ? AND ref_id IN (`+k.openSQL+`) ORDER BY id LIMIT ?`, profileID, kind, profileID, listLimitMax)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var it PersonItem
			if err := rows.Scan(&it.ID, &it.ProfileID, &it.ConversationID, &it.Kind, &it.RefID, &it.CreatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, it)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// AnswerItem is the person's answer to an item: it dispatches by kind to that kind's own path, so
// the answer is queued once, as the same host-made sealed note, whichever surface it came from.
// Only the person answers (there is no scope argument to widen that).
func (s *Store) AnswerItem(profileID string, id int64, answer string) (PersonItem, Command, error) {
	var it PersonItem
	err := s.db.QueryRow(`SELECT id, profile_id, conversation_id, kind, ref_id, created_at FROM coder_session_person_items WHERE profile_id = ? AND id = ?`,
		profileID, id).Scan(&it.ID, &it.ProfileID, &it.ConversationID, &it.Kind, &it.RefID, &it.CreatedAt)
	if err != nil {
		return it, Command{}, ErrUnknownItem
	}
	k, ok := itemKinds[it.Kind]
	if !ok {
		return it, Command{}, fmt.Errorf("coder host: %q is not a kind of item", it.Kind)
	}
	cmd, err := k.answer(s, profileID, it.RefID, answer)
	return it, cmd, err
}

// dropStaleItems removes a conversation's parked-question items whose question is gone.
func (s *Store) dropStaleItems(profileID, conversationID string) {
	_, _ = s.db.Exec(`DELETE FROM coder_session_person_items WHERE profile_id = ? AND conversation_id = ? AND kind = ?
		AND ref_id NOT IN (SELECT id FROM coder_session_questions WHERE profile_id = ?)`, profileID, conversationID, ItemParkedQuestion, profileID)
}
