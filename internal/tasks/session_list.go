package tasks

import "context"

// SessionSkip names a session row ListSessionsChecked left out because it could not be read whole.
type SessionSkip struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"conversation_id,omitempty"`
	Reason         string `json:"reason"`
}

// ListSessions is ListSessionsChecked without the report of the rows it left out.
func (s *Store) ListSessions(ctx context.Context, profileID string, f SessionFilter, actor Actor) ([]Session, error) {
	list, _, err := s.ListSessionsChecked(ctx, profileID, f, actor)
	return list, err
}
