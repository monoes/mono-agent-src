package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai"
)

// answer runs system.answer: it hands the card's answers to the worker that
// asked, through the same AddAnswer the text answer of `chat turn answer`
// uses. One question gives its answer as it is; several give each question
// followed by its answer, one block per question, separated by a blank line.
func (e *sysEnv) answer(ctx context.Context, a a2ui.Action, surf *a2ui.Surface) error {
	cmd, ref, conv, err := e.route(ctx, a)
	if err != nil {
		return err
	}
	agentID, qid, ok := splitAskID(cmd.ID)
	if !ok {
		return fmt.Errorf("%q is not a question id", cmd.ID)
	}
	text, err := answerText(surf, cmd.Fields)
	if err != nil {
		return err
	}
	store, _, closeDB, err := openChatHistory(e.cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	turnID, open, err := openQuestionOf(store, conv.Profile, conv.ID, agentID)
	if err != nil {
		return err
	}
	if open != qid {
		return fmt.Errorf("question %s of %s is not open any more", qid, agentID)
	}
	if err := store.AddAnswer(conv.Profile, conv.ID, turnID, agentID, qid, text); err != nil {
		if errors.Is(err, ai.ErrAlreadyAnswered) {
			return fmt.Errorf("question %s was already answered", qid)
		}
		return err
	}
	e.finish(conv, ref, a.SurfaceID, "Answered", "success")
	return nil
}

// splitAskID splits "<agent>.<qid>" at its last dot.
func splitAskID(id string) (agentID, qid string, ok bool) {
	i := strings.LastIndex(id, ".")
	if i <= 0 || i == len(id)-1 {
		return "", "", false
	}
	return id[:i], id[i+1:], true
}

// answerText is the text a worker gets for its card's fields. Every question
// of the card must be answered: part of a set is never sent.
func answerText(surf *a2ui.Surface, fields map[string]string) (string, error) {
	var blocks, answers []string
	for i := 0; i < surfaces.MaxAskQuestions; i++ {
		key := surfaces.AnswerKey(i)
		q, has := surf.Components["q-"+key]
		if !has {
			break
		}
		ans := strings.TrimSpace(fields[key])
		if ans == "" {
			return "", errors.New("answer every question before sending")
		}
		qt, _ := q["text"].(string)
		blocks, answers = append(blocks, qt+"\n"+ans), append(answers, ans)
	}
	switch len(answers) {
	case 0:
		return "", errors.New("answer every question before sending")
	case 1:
		return answers[0], nil
	}
	return strings.Join(blocks, "\n\n"), nil
}

// mission runs the buttons of the mission cards. stop and close run `task
// session stop|close <id>`, the commands the app's mission views call. edit has
// no command: it asks the caller to send the user message "I want to change the
// plan of mission #<id>." as the conversation's next turn (the plan stays as it
// is and the card stays). A plan is approved in the app, which also starts the
// mission's lead.
func (e *sysEnv) mission(ctx context.Context, a a2ui.Action, _ *a2ui.Surface) error {
	cmd, ref, conv, err := e.route(ctx, a)
	if err != nil {
		return err
	}
	if cmd.Kind == "mission.edit" {
		conv.out.Message = "I want to change the plan of mission #" + cmd.ID + "."
		return nil
	}
	verb := strings.TrimPrefix(cmd.Kind, "mission.")
	if _, err := e.run(ctx, "task", "session", verb, cmd.ID); err != nil {
		return err
	}
	switch cmd.Kind {
	case "mission.stop":
		e.finish(conv, ref, a.SurfaceID, "Mission stopped by you", "neutral")
	case "mission.close":
		e.finish(conv, ref, a.SurfaceID, "Mission closed by you", "success")
	}
	return nil
}
