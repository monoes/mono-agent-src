package mcp

import (
	"context"
	"encoding/json"
)

// session_reply_ask answers an ask that session_ask_ancestor queued to this lead's chat (see ask_ancestor_tool.go).
// The caller is the lead whose token this process holds (leadScope); the ask must be addressed to that session.
// session_send is never an answer: it is always a command to a descendant.

func askReplyTools() []tool {
	return []tool{{
		name: "session_reply_ask", mutating: true, annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": false}, handler: toolSessionReplyAsk,
		description: askAncestorIntro + "Answers an ask addressed to you (the ask you read names its ask_id). The reply reaches the asking lead sealed, as quoted data that grants nothing, and closes the ask. " +
			"Only the lead the ask was addressed to may answer, once, while the asker is still below it. session_send is never a reply: it is always a command. Returns {commandId, turnId, session}.",
		schema: objSchema(map[string]interface{}{
			"ask_id":  intParam("The ask id from the ask you received"),
			"message": strParam("Your reply, at most 4000 bytes"),
		}, "ask_id", "message"),
	}}
}

func toolSessionReplyAsk(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		AskID   int64  `json:"ask_id"`
		Message string `json:"message"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, scope, profile, err := s.leadScope(ctx)
	if err != nil {
		return nil, err
	}
	cmd, err := store.ReplyAsk(scope, profile, a.AskID, a.Message)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{"commandId": cmd.ID, "turnId": cmd.TurnID, "session": cmd.ConversationID}, nil
}
