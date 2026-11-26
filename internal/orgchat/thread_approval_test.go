package orgchat

import (
	"encoding/json"
	"fmt"
	"testing"
)

func bashCall(id string, ts int64) Approval {
	a := Approval{RoleID: "dev", Action: "Bash", TS: ts}
	if id != "" {
		a.RequestID = &id
	}
	return a
}

func busApproval(eventID, requestID, text string, input map[string]interface{}) Event {
	data := map[string]interface{}{"question": text, "action": "Bash", "input": input}
	if requestID != "" {
		data["requestId"] = requestID
	}
	return Event{ID: eventID, Type: "question", From: "dev", Data: data}
}

func listed(id string, ts int64, name string) Approval {
	a := bashCall(id, ts)
	a.Question = "text of " + name
	a.Input = map[string]interface{}{"command": "run " + name, "description": "about " + name}
	return a
}

func approvalCards(items []Item) []Item {
	var out []Item
	for _, it := range items {
		if it.Kind == ItemApproval {
			out = append(out, it)
		}
	}
	return out
}

// card is everything a card says about the call it would release.
func card(it Item) string {
	in, _ := json.Marshal(it.Input)
	return fmt.Sprintf("%s|%s|%s|%s|%s|pending=%v", it.Ref, it.RequestID, it.Text, it.Command, in, it.Pending)
}

func wantCard(ref, id, name string, pending bool) string {
	return fmt.Sprintf(`%s|%s|text of %s|run %s|{"command":"run %s","description":"about %s"}|pending=%v`, ref, id, name, name, name, name, pending)
}

func checkCards(t *testing.T, items []Item, wants ...string) {
	t.Helper()
	got := approvalCards(items)
	if len(got) != len(wants) {
		t.Fatalf("%d approval cards, want %d: %+v", len(got), len(wants), got)
	}
	for i := range wants {
		if card(got[i]) != wants[i] {
			t.Errorf("card %d = %s\nwant     %s", i, card(got[i]), wants[i])
		}
	}
}

// Bus events without request ids: every listed call keeps its own card, with
// text, command, input and ref all from that one call.
func TestThreadNeverMixesCallsWhenBusEventsLackIDs(t *testing.T) {
	events := []Event{
		busApproval("e1", "", "bus one", map[string]interface{}{"command": "run one"}),
		busApproval("e2", "", "bus two", map[string]interface{}{"command": "run two"}),
	}
	for name, ids := range map[string][2]string{"listed with ids": {"apr-1", "apr-2"}, "listed without": {"", ""}} {
		t.Run(name, func(t *testing.T) {
			human := HumanItems{Approvals: []Approval{listed(ids[0], 1, "one"), listed(ids[1], 2, "two")}}
			ref := func(i int) string { return approvalRef(human.Approvals[i]) }
			checkCards(t, BuildThread("acme", "ceo", events, human, 0),
				wantCard(ref(0), ids[0], "one", true), wantCard(ref(1), ids[1], "two", true))
		})
	}
}

// A listed call without a command must not inherit the command of the card it
// takes the place of.
func TestThreadCardTakesNothingFromAnotherCall(t *testing.T) {
	events := []Event{busApproval("e1", "", "bus one", map[string]interface{}{"command": "run other"})}
	other := bashCall("apr-2", 2)
	items := BuildThread("acme", "ceo", events, HumanItems{Approvals: []Approval{other}}, 0)
	got := approvalCards(items)
	if len(got) != 1 || got[0].Ref != "apr-2" || got[0].Command != "" || got[0].Text != "" || got[0].Input != nil {
		t.Fatalf("cards = %+v", got)
	}
}

// More id-less events than listed calls (a repeated call announces itself
// again): the spare card is not left waiting for a decision. A call listed
// beyond the cards gets one of its own.
func TestThreadSpareAndMissingIDLessCards(t *testing.T) {
	one := map[string]interface{}{"command": "run one"}
	events := []Event{busApproval("e1", "", "bus one", one), busApproval("e2", "", "bus one", one)}
	items := BuildThread("acme", "ceo", events, HumanItems{Approvals: []Approval{listed("", 1, "one")}}, 0)
	got := approvalCards(items)
	if len(got) != 2 || !got[0].Pending || got[1].Pending {
		t.Fatalf("cards = %+v", got)
	}
	items = BuildThread("acme", "ceo", events[:1], HumanItems{Approvals: []Approval{listed("", 1, "one"), listed("apr-2", 2, "two")}}, 0)
	checkCards(t, items, wantCard("dev:Bash:1", "", "one", true), wantCard("apr-2", "apr-2", "two", true))
}

// A card carries the call's whole input and, only when there is one, its
// request id: from the bus alone and from the list.
func TestThreadCarriesInputAndRequestID(t *testing.T) {
	input := map[string]interface{}{"command": "run one", "run_in_background": true}
	got := approvalCards(BuildThread("acme", "ceo", []Event{busApproval("e1", "apr-1", "bus one", input)}, HumanItems{}, 0))
	if len(got) != 1 || got[0].RequestID != "apr-1" || got[0].Input["run_in_background"] != true || got[0].Input["command"] != "run one" {
		t.Fatalf("from the bus = %+v", got)
	}
	got = approvalCards(BuildThread("acme", "ceo", []Event{busApproval("e1", "", "bus one", input)}, HumanItems{}, 0))
	if len(got) != 1 || got[0].RequestID != "" || got[0].Ref != "dev:Bash" || !got[0].Pending {
		t.Fatalf("id-less from the bus = %+v", got)
	}
	got = approvalCards(BuildThread("acme", "ceo", nil, HumanItems{Approvals: []Approval{listed("apr-9", 9, "nine")}}, 0))
	checkCards(t, got, wantCard("apr-9", "apr-9", "nine", true))
	raw, _ := json.Marshal(got[0])
	var shape struct {
		RequestID string                 `json:"request_id"`
		Input     map[string]interface{} `json:"input"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil || shape.RequestID != "apr-9" || shape.Input["description"] != "about nine" {
		t.Fatalf("JSON %s", raw)
	}
}
