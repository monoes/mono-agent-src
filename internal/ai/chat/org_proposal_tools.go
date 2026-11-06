package chat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/cli/org"
	"github.com/monoes/mono-agent/internal/orgproposal"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// UnscopedChatLabel is the label of a proposal made by tools that have no conversation id; the MCP server uses
// "unscoped:mcp" (SetUnscopedLabel). Both are short, printable and never empty.
const UnscopedChatLabel = "unscoped:chat"

// proposeOrg stores a pending org proposal (#1477) for set_org_grant and set_org_autonomy and answers with what the
// card in the org screen is drawn from. It goes through the same planning, bounds and store as `org proposal create`,
// in this process: a child run by the chat would be marked as the chat and add nothing. Nothing is applied here, and
// no tool confirms or declines: that verb is the operator's, reached from the app. args is the JSON of the verb's
// arguments. The tool result is one JSON object; the card reads tool and proposal_id and shows the stored row (its preview and its
// hash come from `org proposal show`), never the text of this result.
func (mt *MonoagentTools) proposeOrg(ctx context.Context, tool, orgName, verb string, args interface{}) (string, error) {
	if mt.db == nil {
		return "", fmt.Errorf("%s: unavailable in this session", tool)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	mt.mu.RLock()
	conversation, files, unscoped := mt.conversationID, mt.hybridStore, mt.unscopedLabel
	mt.mu.RUnlock()
	if conversation = orgproposal.CleanLabel(conversation); conversation == "" {
		// Not in a conversation (the MCP server, a chat that is not journaled): never the empty label, which is the
		// operator's own bucket for `org proposal create` without --conversation, so these proposals can neither count
		// toward its cap nor supersede what the operator has pending.
		if conversation = orgproposal.CleanLabel(unscoped); conversation == "" {
			conversation = UnscopedChatLabel
		}
	}
	p, err := org.CreateProposal(ctx, org.ProposalRequest{
		DB: &storage.Database{DB: mt.db}, ProfileID: mt.ProfileID(), Root: mt.profileRoot(),
		OpenWorkflowStore: func(db *storage.Database) *workflow.HybridWorkflowStore {
			if files != nil {
				return files
			}
			return workflow.NewHybridWorkflowStore(nil, workflow.NewSQLiteWorkflowStore(db.DB))
		},
		Org: orgName, Verb: verb, ArgsJSON: string(raw), Conversation: conversation,
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	return marshalJSON(map[string]interface{}{
		"applied":         false,
		"tool":            tool,
		"proposal_id":     p["id"],
		"org":             p["org"],
		"verb":            p["verb"],
		"status":          p["status"],
		"preview_text":    p["preview_text"],
		"expires_at":      p["expires_at"],
		"created_by":      p["created_by"],
		"conversation_id": p["conversation_id"],
		"note": "Proposal stored; nothing was changed. Tell the user to confirm or decline it on the proposal card in the org screen " +
			"(it expires after 30 minutes). You cannot confirm it.",
	})
}
