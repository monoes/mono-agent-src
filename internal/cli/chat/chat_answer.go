package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// newChatTurnAnswerCmd answers a dynamic-org worker's question (#256): the
// running turn picks the answer up, journals it, and hands it to the
// worker.
func newChatTurnAnswerCmd(cfg *Deps) *cobra.Command {
	var agentID, questionID, text string
	var alone bool
	cmd := &cobra.Command{
		Use:   "answer <conversation-id> <turn-id> --agent <id> --question <id> --text <answer>",
		Short: "Answer a question a dynamic-org worker asked the user",
		Long: "A worker in a dynamic-org coder chat can ask the user a question (agent.message with direction " +
			"\"question\" and a questionId in the turn's events). This records the answer; the running turn " +
			"passes it to the worker within a second and journals it as that worker's follow-up from the user. " +
			"Only an open question of a turn that is still running can be answered, and only once: a question " +
			"that timed out or whose worker was stopped is closed.\n\n" +
			"When other cards wait with it in the turn (more questions, or an agent's card with a button), they are " +
			"answered together, as one set through `chat ui action`; this command then refuses unless --alone says " +
			"to answer only this question.",
		Example: `  monoagentcli chat turn answer c1 t1 --agent w1 --question q1 --text "Use Postgres"`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			conversationID, turnID := args[0], args[1]
			text = strings.TrimSpace(text)
			if agentID == "" || questionID == "" || text == "" {
				return clicore.InvalidInput("--agent, --question and --text are required")
			}
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			if err := checkOpenQuestion(store, profileID, conversationID, turnID, agentID, questionID); err != nil {
				return err
			}
			if !alone {
				if err := refuseOneOfSet(store, profileID, conversationID, agentID, questionID); err != nil {
					return err
				}
			}
			if err := store.AddAnswer(profileID, conversationID, turnID, agentID, questionID, text); err != nil {
				if errors.Is(err, ai.ErrAlreadyAnswered) {
					return clicore.InvalidInput("question %s of %s already has an answer", questionID, agentID)
				}
				return err
			}
			if cfg.JSONOutput() {
				return clicore.PrintJSON(map[string]any{"answered": true, "conversation_id": conversationID, "turn_id": turnID, "agent_id": agentID, "question_id": questionID})
			}
			fmt.Printf("answered %s's question %s\n", agentID, questionID)
			return nil
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "The worker that asked (e.g. w1)")
	cmd.Flags().StringVar(&questionID, "question", "", "The question's id (e.g. q1)")
	cmd.Flags().StringVar(&text, "text", "", "The answer")
	cmd.Flags().BoolVar(&alone, "alone", false, "Answer only this question although other cards wait with it")
	return cmd
}

// refuseOneOfSet refuses a question whose card waits with others of its turn
// (#1803). A question without a card is in no set.
func refuseOneOfSet(store *ai.AIStore, profileID, conversationID, agentID, questionID string) error {
	evs, err := store.UIEvents(profileID, conversationID)
	if err != nil {
		return err
	}
	waiting, err := waitingCards(store, profileID, conversationID, dynorg.RebuildRegistry(evs))
	if err != nil {
		return err
	}
	if n := waiting.with(surfaces.AskSurfaceID(agentID + "." + questionID)); n > 1 {
		return clicore.InvalidInput("%d cards wait together in this turn: send them as one set with `chat ui action`, "+
			"or pass --alone to answer only this question", n)
	}
	return nil
}

// checkOpenQuestion refuses an answer unless the turn is still running and
// its journal has that worker's question and nothing has closed it yet
// (the user's answer, or a timeout or stop, both followups with its id).
func checkOpenQuestion(store *ai.AIStore, profileID, conversationID, turnID, agentID, questionID string) error {
	turn, err := store.GetTurn(turnID, profileID)
	if err != nil {
		return chatStoreErr(err)
	}
	if turn.ConversationID != conversationID {
		return clicore.InvalidInput("turn %s belongs to another conversation", turnID)
	}
	if turn.Status != "active" {
		return clicore.InvalidInput("turn %s has finished (%s); its workers can't take answers any more", turnID, turn.Status)
	}
	asked, answered := false, false
	var after int64
	for {
		evs, err := store.GetEvents(conversationID, turnID, profileID, after, 500)
		if err != nil {
			return err
		}
		for _, ev := range evs {
			after = ev.Seq
			if ev.Type != chatevents.EventAgentMessage {
				continue
			}
			var m chatevents.AgentMessagePayload
			if json.Unmarshal(ev.Payload, &m) != nil || m.AgentID != agentID || m.QuestionID != questionID {
				continue
			}
			switch {
			case m.Direction == "question":
				asked = true
			case m.Direction == "followup" && (m.From == "user" || m.From == "system"):
				answered = true
			}
		}
		if len(evs) < 500 {
			break
		}
	}
	switch {
	case !asked:
		return clicore.InvalidInput("%s asked no question %s in turn %s", agentID, questionID, turnID)
	case answered:
		return clicore.InvalidInput("question %s of %s is closed (answered, timed out, or its worker stopped)", questionID, agentID)
	}
	return nil
}
