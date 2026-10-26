package coderhost

import (
	"errors"
	"fmt"
)

// A master answers a question parked by one of its sessions. The caller is the
// Caller of a verified session token, so the master's identity is the conversation
// the token names, never an argument.

// tokenMasterScope is the CallerScope of a verified session token: it speaks for
// the master whose token it is, and for no one else.
type tokenMasterScope struct{ conv string }

// Role is WhoMaster.
func (tokenMasterScope) Role() string { return WhoMaster }

// ConversationID is the master's own conversation, from the verified token.
func (t tokenMasterScope) ConversationID() string { return t.conv }

// AnswerAsMaster answers the open question id of the session conv. It succeeds only for a caller
// that Answer would accept as the session's master: conv must be a direct child of the caller, the question
// must be conv's, and the policy must name the master for that class. A question that does not
// exist, belongs to another profile or session, or is not under the caller (a peer, an ancestor,
// or a higher ancestor of the child) is refused with the same ErrNotYours text, so the answer is no way to
// find out which questions exist elsewhere. What Answer does for a master holds unchanged: the
// note is host-made (answer-m-, issuer = the master), counted in the check-in streak, gated at the
// limit, sealed as data, and can never carry a mission, an approval or a plan approval.
func (s *Store) AnswerAsMaster(c Caller, conv string, id int64, answer string) (Question, Command, error) {
	if c.ProfileID == "" || c.ConversationID == "" {
		return Question{}, Command{}, ErrSessionToken
	}
	refused := fmt.Errorf("%w: question %d of %s", ErrNotYours, id, conv)
	if !s.IsDescendant(c.ProfileID, c.ConversationID, conv) {
		return Question{}, Command{}, refused
	}
	if parent, ok := s.hasMaster(c.ProfileID, conv); !ok || parent != c.ConversationID {
		return Question{}, Command{}, refused // an ancestor above the direct master reads the same as a stranger
	}
	q, err := s.GetQuestion(c.ProfileID, id)
	if err != nil {
		if errors.Is(err, ErrNoQuestion) {
			return Question{}, Command{}, refused
		}
		return Question{}, Command{}, err
	}
	if q.ConversationID != conv {
		return Question{}, Command{}, refused
	}
	return s.Answer(c.ProfileID, id, answer, tokenMasterScope{conv: c.ConversationID})
}
