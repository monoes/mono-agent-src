package chat

import (
	"slices"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// maxCardEventBytes bounds the event payloads one `chat history turns --cards
// --events` returns: the newest card turns first, older ones left out.
var maxCardEventBytes = 4 << 20

// cardTurnRecord is one card turn with its surface and press events.
type cardTurnRecord struct {
	ai.TurnRecord
	Events []chatevents.Record `json:"events"`
}

type chatCardTurnPage struct {
	Items      []cardTurnRecord `json:"items"`
	NextCursor string           `json:"next_cursor"`
}

// cardTurnsWithEvents joins the card turns (newest first), those in skip left
// out, with their events, read in one query.
func cardTurnsWithEvents(store *ai.AIStore, profileID, conversationID string, turns []ai.Turn, skip []string) (chatCardTurnPage, error) {
	page := chatCardTurnPage{Items: []cardTurnRecord{}}
	have := map[string]bool{}
	for _, id := range skip {
		have[id] = true
	}
	all, err := store.UIEvents(profileID, conversationID)
	if err != nil {
		return page, err
	}
	byTurn := map[string][]chatevents.Record{}
	written := map[string]int{} // by the order the events were journaled in
	for i, ev := range all {
		byTurn[ev.TurnID] = append(byTurn[ev.TurnID], ev.Record())
		written[ev.TurnID] = i
	}
	// Turn times are whole seconds: the journal says which of one second came last.
	turns = slices.Clone(turns)
	slices.SortStableFunc(turns, func(a, b ai.Turn) int { return written[b.ID] - written[a.ID] })
	size := 0
	for _, t := range turns {
		if have[t.ID] {
			continue
		}
		for _, r := range byTurn[t.ID] {
			size += len(r.Payload)
		}
		if size > maxCardEventBytes && len(page.Items) > 0 {
			break
		}
		page.Items = append(page.Items, cardTurnRecord{TurnRecord: t.Record(), Events: append([]chatevents.Record{}, byTurn[t.ID]...)})
	}
	return page, nil
}
