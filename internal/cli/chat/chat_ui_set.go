package chat

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// Several cards waiting for the user at once are answered as one set (#1803):
// nothing reaches a worker or the next turn until the whole set is sent. The
// cards of a set are the open worker questions of the running turn and the
// agent cards with a button that one turn made. Approvals and mission buttons
// are never part of a set: each decides one thing and is pressed on its own.

// waitingSet maps each card that waits for an answer to the turn it belongs to.
type waitingSet map[string]string

// with is how many cards wait together with the card, itself included; 0 when
// it is not a waiting card.
func (w waitingSet) with(surfaceID string) int {
	group, ok := w[surfaceID]
	if !ok {
		return 0
	}
	n := 0
	for _, g := range w {
		if g == group {
			n++
		}
	}
	return n
}

func waitingCards(store *ai.AIStore, profileID, conversationID string, replay *dynorg.UIReplay) (waitingSet, error) {
	turnID, open, err := openQuestions(store, profileID, conversationID)
	if err != nil {
		return nil, err
	}
	set := waitingSet{}
	for _, id := range replay.Registry.Waiting() {
		s := replay.Registry.Surface(id)
		if s.Prov != a2ui.FromSystem {
			set[id] = replay.Turns[id]
			continue
		}
		ask, isAsk := strings.CutPrefix(id, "ask-")
		if agentID, qid, ok := splitAskID(ask); isAsk && ok && slices.Contains(open[agentID], qid) {
			set[id] = turnID
		}
	}
	return set, nil
}

// agentPress is a press of a set on an agent's card.
type agentPress struct{ author, turn, text string }

// runUISet delivers a set of cards, whole or not at all. Every press is checked
// as a single one is; the workers' answers are then stored in one transaction,
// and the agent cards' presses become one next-turn message.
func runUISet(store *ai.AIStore, profileID, conversationID string, press a2ui.Press, evs []chatevents.Event, replay *dynorg.UIReplay) (*UIActionResult, error) {
	waiting, err := waitingCards(store, profileID, conversationID, replay)
	if err != nil {
		return nil, err
	}
	// A refused set says something only of the cards it was refused for: a card
	// that is gone is stale, one still waiting stays open, and nothing is said
	// of any other (an answered card must not open again).
	record := func(res *UIActionResult) (*UIActionResult, error) {
		for _, a := range press.Actions {
			outcome := res.Outcome
			if outcome != outcomeAccepted {
				switch {
				case replay.Closed[a.SurfaceID]:
					outcome = outcomeStale
				case waiting[a.SurfaceID] != "":
					outcome = outcomeRejected
				default:
					continue
				}
			}
			if _, err := store.RecordUIAction(profileID, conversationID, chatevents.UIActionPayload{
				SurfaceID: a.SurfaceID, Name: a.Name, SourceComponentID: a.SourceComponentID,
				Context: a.Context, Outcome: outcome, Reason: res.Reason,
			}); err != nil {
				return res, err
			}
		}
		return res, nil
	}
	refuse := func(format string, args ...any) (*UIActionResult, error) {
		return record(&UIActionResult{Outcome: outcomeRejected, Reason: "nothing was sent: " + fmt.Sprintf(format, args...)})
	}
	var answers []ai.Answer
	var agentPresses []agentPress
	var asked []a2ui.Action
	sent := map[string]bool{}
	group := ""
	for _, a := range press.Actions {
		surf, err := replay.Registry.CheckAction(a)
		if err != nil {
			outcome := outcomeRejected
			if replay.Closed[a.SurfaceID] {
				outcome = outcomeStale
			}
			return record(&UIActionResult{Outcome: outcome, Reason: "nothing was sent: " + err.Error()})
		}
		prefix, system := prefixOf(a.Name), surf.Prov == a2ui.FromSystem
		switch {
		case system && a.Name != "system.answer":
			return refuse("%q is pressed on its own, not as part of a set", a.Name)
		case !system && prefix != "":
			return refuse("action %q does not belong to a system surface", a.Name)
		case sent[a.SurfaceID]:
			return refuse("card %q is in the set twice", a.SurfaceID)
		}
		turn, ok := waiting[a.SurfaceID]
		if !ok {
			return refuse("card %q is not waiting for an answer any more", a.SurfaceID)
		}
		if group != "" && turn != group {
			return refuse("a set holds the cards of one reply")
		}
		group, sent[a.SurfaceID] = turn, true
		if !system {
			if a.Revision == "" {
				return refuse("card %q: a card in a set says which version of it was answered (revision)", a.SurfaceID)
			}
			if why := changedCard(a, surf, replay); why != "" {
				return refuse("card %q: %s", a.SurfaceID, why)
			}
			agentPresses = append(agentPresses, agentPress{replay.Authors[a.SurfaceID], turn, actionMessage(a, surf, surfaceHistory(evs, a.SurfaceID))})
			continue
		}
		cmd, err := surfaces.Route(a, refOfSurface(evs, a.SurfaceID))
		if err != nil {
			return refuse("%v", err)
		}
		agentID, qid, ok := splitAskID(cmd.ID)
		if !ok {
			return refuse("%q is not a question id", cmd.ID)
		}
		text, err := answerText(surf, cmd.Fields)
		if err != nil {
			return refuse("%v", err)
		}
		answers = append(answers, ai.Answer{AgentID: agentID, QuestionID: qid, Text: text})
		asked = append(asked, a)
	}
	skipped := map[string]bool{}
	for _, id := range press.Skipped {
		s := replay.Registry.Surface(id)
		switch {
		case sent[id]:
			return refuse("card %q is both answered and skipped", id)
		case s == nil || waiting[id] != group:
			return refuse("card %q is not waiting with this set", id)
		case s.Prov == a2ui.FromSystem:
			return refuse("a worker's question cannot be skipped")
		}
		skipped[id] = true
	}
	for id, turn := range waiting {
		if turn == group && !sent[id] && !skipped[id] {
			return refuse("another card is waiting too: answer it and send them together")
		}
	}
	// A press on the card of a worker whose question is in this set goes to it
	// with that answer, as a single press would; the rest go to the next turn.
	var messages []string
press:
	for _, p := range agentPresses {
		for i := range answers {
			if p.author != "" && answers[i].AgentID == p.author && p.turn == group {
				answers[i].Text += "\n\n" + p.text
				continue press
			}
		}
		messages = append(messages, p.text)
	}
	res := &UIActionResult{Outcome: outcomeAccepted, Delivered: "answer"}
	if len(messages) > 0 {
		res.Delivered = "next_turn"
		res.Message = messages[0]
		if len(messages) > 1 {
			res.Message = fmt.Sprintf("The user answered %d cards together.\n\n%s", len(messages), strings.Join(messages, "\n\n"))
		}
	}
	// Before anything is stored or settled. The caller joins its own text with a blank line.
	size := len(res.Message) + press.AppendBytes
	if res.Message != "" && press.AppendBytes > 0 {
		size += 2
	}
	if why := tooLong(size); why != "" {
		return refuse("%s", why)
	}
	if len(answers) > 0 {
		if err := store.AddAnswers(profileID, conversationID, group, answers); err != nil {
			if errors.Is(err, ai.ErrAlreadyAnswered) {
				return refuse("one of the questions already has an answer (%v)", err)
			}
			return nil, err
		}
	}
	conv := uiConv{ID: conversationID, Profile: profileID}
	for _, a := range asked {
		// Best effort, as for a single answer: the conductor closes the card too.
		if msgs, err := surfaces.ResolvedNotice(a.SurfaceID, "Answered", "success"); err == nil {
			_ = recordMessages(store, conv, refOfSurface(evs, a.SurfaceID), msgs)
		}
	}
	return record(res)
}
