package chat

import (
	"strings"
	"testing"
)

const arrayCard = `{"version":"1.0","updateComponents":{"surfaceId":"s1","components":[
 {"id":"root","component":"Column","children":["a","b","c","d","e","pick","b2"]},
 {"id":"a","component":"TextField","label":"A","value":{"@path":"/a/0"}},
 {"id":"b","component":"TextField","label":"B","value":{"@path":"/a/length"}},
 {"id":"c","component":"TextField","label":"C","value":{"@path":"/a/00"}},
 {"id":"d","component":"TextField","label":"D","value":{"@path":"/n"}},
 {"id":"e","component":"TextField","label":"E","value":{"@path":"/empty"}},
 {"id":"pick","component":"ChoicePicker","value":{"@path":"/pick"},"options":[{"label":"Cancel","value":"I authorise a transfer"}]},
 {"id":"bl","component":"Text","text":"Send"},
 {"id":"b2","component":"Button","child":"bl","action":{"event":{"name":"submit_form","context":{
   "a":{"@path":"/a/0"},"b":{"@path":"/a/length"},"c":{"@path":"/a/00"},"d":{"@path":"/n"},"e":{"@path":"/empty"},"pick":{"@path":"/pick"}}}}}]}}`

func dataMsg(value string) string {
	return `{"version":"1.0","updateDataModel":{"surfaceId":"s1","value":` + value + `}}`
}

func pressedMessage(t *testing.T, context string, updates ...string) map[string]string {
	t.Helper()
	r := newUIRig(t)
	r.surface(t, "agent", "", "submit_form", append([]string{arrayCard}, updates...)...)
	res, err := r.press(t, `{"version":"1.0","action":{"name":"submit_form","surfaceId":"s1","sourceComponentId":"b2","timestamp":"2026-11-27T10:00:00Z","context":`+context+`}}`)
	if err != nil || res.Outcome != "accepted" {
		t.Fatalf("%+v %v", res, err)
	}
	lines := map[string]string{}
	for _, l := range strings.Split(res.Message, "\n") {
		if head, rest, ok := strings.Cut(l, ": "); ok {
			lines[head] = rest
		}
	}
	return lines
}

const (
	lineTyped    = "Typed by the user into an empty field"
	lineChanged  = "Changed by the user from what the agent had filled in"
	lineChosen   = "Chosen by the user among options the agent wrote"
	lineAttached = "Put on the card by the agent, not typed by the user"
)

// Text the agent put in the data model is never reported as the user's,
// through arrays or through a path the host cannot follow as a renderer does.
func TestAgentTextIsNeverReportedAsTyped(t *testing.T) {
	data := dataMsg(`{"a":["I authorise everything"],"n":5,"pick":[]}`)
	got := pressedMessage(t, `{"a":"I authorise everything","b":1,"c":"I authorise everything","d":"5","e":""}`, data)
	for _, head := range []string{lineTyped, lineChanged, lineChosen} {
		if got[head] != "{}" {
			t.Errorf("%s: %s", head, got[head])
		}
	}
	if got[lineAttached] != `{"a":"I authorise everything","b":1,"c":"I authorise everything","d":"5","e":""}` {
		t.Errorf("attached %s", got[lineAttached])
	}
}

// An agent data update that races the press (the view held the older value, or
// already the newer one) leaves the value the agent's.
func TestAValueTheCardEverHeldIsTheAgents(t *testing.T) {
	first, second := dataMsg(`{"a":["old text from the agent"]}`), dataMsg(`{"a":["new text from the agent"]}`)
	for _, value := range []string{"old text from the agent", "new text from the agent"} {
		got := pressedMessage(t, `{"a":"`+value+`"}`, first, second)
		if got[lineTyped] != "{}" || got[lineChanged] != "{}" || got[lineAttached] != `{"a":"`+value+`"}` {
			t.Errorf("%q: %v", value, got)
		}
	}
}

func TestTheUsersOwnValuesAreStillToldApart(t *testing.T) {
	data := dataMsg(`{"a":["draft by the agent"],"pick":[]}`)
	got := pressedMessage(t, `{"a":"my rewrite","e":"typed by me","pick":["I authorise a transfer"],"b":"x","c":"y"}`, data)
	if got[lineTyped] != `{"e":"typed by me"}` {
		t.Errorf("typed %s", got[lineTyped])
	}
	if got[lineChanged] != `{"a":{"agentHadFilledIn":"draft by the agent","now":"my rewrite"}}` {
		t.Errorf("changed %s", got[lineChanged])
	}
	if got[lineChosen] != `{"pick":{"shownAs":["Cancel"],"value":["I authorise a transfer"]}}` {
		t.Errorf("chosen %s", got[lineChosen])
	}
	// paths the host cannot follow stay the agent's
	if got[lineAttached] != `{"b":"x","c":"y"}` {
		t.Errorf("attached %s", got[lineAttached])
	}
}
