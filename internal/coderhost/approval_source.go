package coderhost

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The tasks store applies the approval rule (internal/tasks/session_approval.go); this file is what
// it reads from the host: the operator's settings of the mission (the root session's chat) and the
// person items of giant sessions. Store implements tasks.ApprovalSource.

var _ tasks.ApprovalSource = (*Store)(nil)

// ItemPlanApproval is a draft session that only the person approves (giant work). ref_id is the
// task_sessions id. It is open while that session is still a draft.
const ItemPlanApproval = "plan_approval"

// ApprovalPolicy reads the operator-only approval settings of the chat that leads a mission's root
// session. A chat with no stored settings has the defaults, which are off.
func (s *Store) ApprovalPolicy(_ context.Context, profileID, rootConversation string) (tasks.ApprovalPolicy, error) {
	if rootConversation == "" {
		return tasks.ApprovalPolicy{}, fmt.Errorf("coder host: a mission root linked to no chat has no approval settings")
	}
	set, err := s.GetSettings(profileID, rootConversation)
	if err != nil {
		return tasks.ApprovalPolicy{}, err
	}
	return tasks.ApprovalPolicy{
		AgentLead:    set.AgentApproval == "lead",
		CeilingUSD:   set.ApprovalCeilingUSD,
		WorkingDepth: set.MaxDepth,
	}, nil
}

// RaisePersonItem asks the person to approve a giant session. It waits with no timeout: it has no
// question row, so no default, master or model can answer it. It is idempotent per session.
func (s *Store) RaisePersonItem(_ context.Context, profileID, conversation string, session int64) error {
	if conversation == "" || session <= 0 {
		return fmt.Errorf("coder host: a plan approval item needs a chat and a session")
	}
	return addPersonItem(s.db, profileID, conversation, ItemPlanApproval, "", session, s.stamp())
}

// PersonItemRaised says whether a person item asks for the session's approval.
func (s *Store) PersonItemRaised(ctx context.Context, profileID string, session int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM coder_session_person_items WHERE profile_id = ? AND kind = ? AND ref_id = ?`,
		profileID, ItemPlanApproval, session).Scan(&n)
	return n > 0, err
}

// openPlanApprovalItems: the session is still a draft and its chat is not stopped.
func (s *Store) openPlanApprovalItems(profileID string) ([]PersonItem, error) {
	rows, err := s.db.Query(`SELECT i.id, i.conversation_id, i.ref_id, i.created_at FROM coder_session_person_items i
		JOIN task_sessions t ON t.id = i.ref_id AND t.profile_id = i.profile_id
		WHERE i.profile_id = ? AND i.kind = ? AND t.state = 'draft' AND t.archived_at = '' ORDER BY i.id LIMIT ?`,
		profileID, ItemPlanApproval, listLimitMax)
	if err != nil {
		return nil, err
	}
	var cand []PersonItem
	for rows.Next() {
		it := PersonItem{ProfileID: profileID, Kind: ItemPlanApproval}
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
		if !s.sessionStopped(profileID, it.ConversationID) {
			out = append(out, it)
		}
	}
	return out, nil
}
