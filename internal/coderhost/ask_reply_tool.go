package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// errNotAddressee is the one refusal for an ask id that does not exist, is not a normal ask, or is addressed to
// another lead: the caller cannot tell these apart, so it cannot probe other leads' asks.
var errNotAddressee = fmt.Errorf("%w: that ask is not addressed to your session", ErrNotYours)

// ReplyAsk is session_reply_ask: the ancestor lead named by the ask answers it. sc must be the LeadScope of a
// verified lead with no task binding. The reply is delivered sealed (turn id "ask-r-") and closes the ask; it
// is the only way a lead answers an ask, and session_send is never turned into one. Every failure refuses and
// leaves the ask as it was: an ask is voided only when it is read that the asker is no longer below the lead.
func (s *Store) ReplyAsk(sc CallerScope, profileID string, askID int64, message string) (Command, error) {
	sid, err := s.LeadMission(sc, profileID)
	if err != nil {
		return Command{}, err
	}
	l, _, err := s.leadOf(sc, profileID, sid)
	if err != nil {
		return Command{}, err
	}
	conv := l.Caller.ConversationID
	var askerSession, ancestorSession int64
	var askerConv, ancestorConv, importance, status, question, createdAt string
	var ancConv sql.NullString
	err = s.db.QueryRow(`SELECT asker_session, asker_conv, ancestor_session, ancestor_conv, importance, status, question, created_at FROM coder_session_asks
		WHERE profile_id = ? AND id = ?`, profileID, askID).Scan(&askerSession, &askerConv, &ancestorSession, &ancConv, &importance, &status, &question, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Command{}, errNotAddressee
	}
	if err != nil {
		return Command{}, fmt.Errorf("read the ask: %w", err)
	}
	ancestorConv = ancConv.String
	if importance != AskNormal || ancestorConv == "" || ancestorConv != conv {
		return Command{}, errNotAddressee
	}
	if s.askStale(importance, status, createdAt) {
		_, _ = s.db.Exec(`UPDATE coder_session_asks SET status = ? WHERE id = ? AND status = 'open'`, askExpired, askID)
		status = askExpired
	}
	if status != askOpen {
		return Command{}, fmt.Errorf("%w: that ask is %s", ErrNotOpen, status)
	}
	if strings.TrimSpace(message) == "" || len(message) > MaxAnswerBytes {
		return Command{}, fmt.Errorf("%w: a reply is 1 to %d bytes", ErrInvalid, MaxAnswerBytes)
	}
	if s.beforeAskReplyTx != nil {
		s.beforeAskReplyTx()
	}
	cmd, err := s.closeAndDeliver(profileID, askID, askerConv, question, "an ancestor lead", message, "lead:"+conv, askFromLead,
		func(q querier) (bool, error) { return stillAbove(q, profileID, askerSession, ancestorSession, conv) })
	return cmd, err
}
