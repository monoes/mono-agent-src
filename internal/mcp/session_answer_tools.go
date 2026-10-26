package mcp

import (
	"context"
	"encoding/json"
)

func sessionAnswerTools() []tool {
	return []tool{{
		name: "session_answer", mutating: true, handler: toolSessionAnswer,
		annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": false},
		description: coderSessionIntro + "Answers a question that one of your child sessions parked, when the policy names you (the master) for that class of gap. " +
			"The answer reaches the child as a host-made note: quoted data that answers that question only, counted like a check-in, and it never confirms a mission, " +
			"approves a plan or an action, or changes access, a budget or a setting. A question that is not one of your direct child's, or that the policy leaves to the user, is refused. " +
			"Returns {questionId, commandId, session}.",
		schema: objSchema(map[string]interface{}{
			"session":     sessionTarget(),
			"question_id": map[string]interface{}{"type": "integer", "description": "The parked question's id, from the child's parked questions"},
			"answer":      strParam("Your answer (at most 4000 bytes)"),
		}, "session", "question_id", "answer"),
	}}
}

func toolSessionAnswer(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Session    string `json:"session"`
		QuestionID int64  `json:"question_id"`
		Answer     string `json:"answer"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, c, err := s.sessionCaller(ctx)
	if err != nil {
		return nil, err
	}
	q, cmd, err := store.AnswerAsMaster(c, a.Session, a.QuestionID, a.Answer)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{"questionId": q.ID, "commandId": cmd.ID, "session": q.ConversationID}, nil
}
