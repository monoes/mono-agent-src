package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// Several questions pending at once are answered together (#1803).

// liveTurn starts the running turn the questions of these tests belong to.
func liveTurn(t *testing.T, r *sysRig) {
	t.Helper()
	if _, _, err := r.store.CreateTurn(r.conv.ID, "default", "live", "i", "go"); err != nil {
		t.Fatal(err)
	}
}

func (r *sysRig) appendLive(t *testing.T, typ chatevents.EventType, p any) {
	t.Helper()
	if _, err := r.store.AppendEvent("default", r.conv.ID, "live", typ, p); err != nil {
		t.Fatal(err)
	}
}

// askLive journals a worker's open question and its card in the running turn, as the conductor does.
func askLive(t *testing.T, r *sysRig, agent string, qs ...string) {
	t.Helper()
	r.appendLive(t, chatevents.EventAgentMessage, chatevents.AgentMessagePayload{AgentID: agent, Direction: "question", QuestionID: "q1", From: agent, To: "user", Text: strings.Join(qs, " ")})
	questions := make([]surfaces.Question, len(qs))
	for i, q := range qs {
		questions[i] = surfaces.Question{Text: q}
	}
	msgs, err := surfaces.Ask(agent+".q1", questions)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		r.appendLive(t, chatevents.EventUISurface, chatevents.UISurfacePayload{SurfaceID: "ask-" + agent + ".q1", Provenance: "system",
			Ref: &chatevents.UIRef{Kind: surfaces.RefAsk, ID: agent + ".q1"}, Message: m})
	}
}

// agentCard journals a lead's card with one button in the running turn.
func agentCard(t *testing.T, r *sysRig, id string) {
	t.Helper()
	body := `{"version":"1.0","createSurface":{"surfaceId":"` + id + `","components":[` +
		`{"id":"root","component":"Column","children":["b"]},{"id":"bl","component":"Text","text":"Pick ` + id + `"},` +
		`{"id":"b","component":"Button","child":"bl","action":{"event":{"name":"pick_` + id + `","context":{}}}}]}}`
	r.appendLive(t, chatevents.EventUISurface, chatevents.UISurfacePayload{SurfaceID: id, Provenance: "agent", Message: json.RawMessage(body)})
}

func answerBody(agent string, answers ...string) map[string]any {
	ctx := map[string]any{"questionId": agent + ".q1"}
	for i, a := range answers {
		ctx[surfaces.AnswerKey(i)] = a
	}
	return map[string]any{"name": "system.answer", "surfaceId": "ask-" + agent + ".q1", "sourceComponentId": "btn-continue", "context": ctx}
}

func cardBody(id string) map[string]any {
	return map[string]any{"name": "pick_" + id, "surfaceId": id, "sourceComponentId": "b", "context": map[string]any{}}
}

func (r *sysRig) pressOne(t *testing.T, body map[string]any) UIActionResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"version": "1.0", "action": body})
	res, err := r.uiRig.press(t, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (r *sysRig) pressSet(t *testing.T, skipped []string, bodies ...map[string]any) UIActionResult {
	t.Helper()
	env := map[string]any{"version": "1.0", "actions": bodies}
	if len(skipped) > 0 {
		env["skipped"] = skipped
	}
	raw, _ := json.Marshal(env)
	res, err := r.uiRig.press(t, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (r *sysRig) answerOf(t *testing.T, agent string) (string, bool) {
	t.Helper()
	text, ok, err := r.store.TakeAnswer("default", "live", agent, "q1")
	if err != nil {
		t.Fatal(err)
	}
	return text, ok
}

func TestOneOfSeveralOpenQuestionsIsNotSentAlone(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	askLive(t, r, "w1", "Which region?")
	askLive(t, r, "w2", "Which database?")
	res := r.pressOne(t, answerBody("w1", "eu"))
	if res.Outcome != "rejected" || !strings.Contains(res.Reason, "together") {
		t.Fatalf("a single answer went out while another question waits: %+v", res)
	}
	if _, ok := r.answerOf(t, "w1"); ok {
		t.Fatal("the worker was handed a partial set")
	}
}

func TestASetAnswersEveryWaitingWorkerAtOnce(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	askLive(t, r, "w1", "Which region?")
	askLive(t, r, "w2", "Which database?", "Which version?")
	res := r.pressSet(t, nil, answerBody("w1", "eu"), answerBody("w2", "postgres", "17"))
	if res.Outcome != "accepted" || res.Delivered != "answer" || res.Message != "" {
		t.Fatalf("%+v", res)
	}
	if got, _ := r.answerOf(t, "w1"); got != "eu" {
		t.Fatalf("w1 got %q", got)
	}
	if got, _ := r.answerOf(t, "w2"); got != "Which database?\npostgres\n\nWhich version?\n17" {
		t.Fatalf("w2 got %q", got)
	}
	for _, id := range []string{"ask-w1.q1", "ask-w2.q1"} {
		if hasButton(r.surfaceNow(t, id)) {
			t.Fatalf("%s kept its button", id)
		}
	}
}

// Nothing of a set is delivered unless all of it can be.
func TestASetIsAllOrNothing(t *testing.T) {
	hilBody := map[string]any{"name": "hil.approve", "surfaceId": "hil-482", "sourceComponentId": "btn-approve", "context": map[string]any{"id": "482"}}
	for name, c := range map[string]struct {
		bodies  []map[string]any
		skipped []string
		reason  string
	}{
		"a question left out":    {[]map[string]any{answerBody("w1", "eu")}, nil, "waiting"},
		"a question skipped":     {[]map[string]any{answerBody("w1", "eu")}, []string{"ask-w2.q1"}, "skipped"},
		"an empty answer":        {[]map[string]any{answerBody("w1", "eu"), answerBody("w2", "pg", " ")}, nil, "every question"},
		"one question twice":     {[]map[string]any{answerBody("w1", "eu"), answerBody("w1", "us"), answerBody("w2", "pg", "17")}, nil, "twice"},
		"a card that is not up":  {[]map[string]any{answerBody("w1", "eu"), answerBody("w2", "pg", "17"), answerBody("w9", "x")}, nil, "not live"},
		"an approval in the set": {[]map[string]any{answerBody("w1", "eu"), answerBody("w2", "pg", "17"), hilBody}, nil, "on its own"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSysRig(t)
			liveTurn(t, r)
			askLive(t, r, "w1", "Which region?")
			askLive(t, r, "w2", "Which database?", "Which version?")
			r.showHIL(t)
			res := r.pressSet(t, c.skipped, c.bodies...)
			if res.Outcome == "accepted" || !strings.Contains(res.Reason, c.reason) {
				t.Fatalf("%+v", res)
			}
			for _, a := range []string{"w1", "w2"} {
				if text, ok := r.answerOf(t, a); ok {
					t.Fatalf("%s was handed %q out of a refused set", a, text)
				}
			}
			if r.cli.called("hil", "approve") != 0 {
				t.Fatal("a set ran an approval")
			}
		})
	}
}

// A question that timed out leaves the set: sending the old set sends nothing,
// and the one still open is answered on its own.
func TestATimedOutQuestionLeavesTheSet(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	askLive(t, r, "w1", "Which region?")
	askLive(t, r, "w2", "Which database?")
	r.appendLive(t, chatevents.EventAgentMessage, chatevents.AgentMessagePayload{AgentID: "w2", Direction: "followup", QuestionID: "q1", From: "system", To: "w2", Text: "No answer"})
	res := r.pressSet(t, nil, answerBody("w1", "eu"), answerBody("w2", "pg"))
	if res.Outcome == "accepted" {
		t.Fatalf("%+v", res)
	}
	if _, ok := r.answerOf(t, "w1"); ok {
		t.Fatal("part of a refused set was delivered")
	}
	if res := r.pressOne(t, answerBody("w1", "eu")); res.Outcome != "accepted" {
		t.Fatalf("%+v", res)
	}
}

// An answer already stored for one question of the set stores none of the others.
func TestASetWithAnAnsweredQuestionStoresNothing(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	askLive(t, r, "w1", "Which region?")
	askLive(t, r, "w2", "Which database?")
	if err := r.store.AddAnswer("default", r.conv.ID, "live", "w2", "q1", "from the terminal"); err != nil {
		t.Fatal(err)
	}
	res := r.pressSet(t, nil, answerBody("w1", "eu"), answerBody("w2", "pg"))
	if res.Outcome == "accepted" || !strings.Contains(res.Reason, "already") {
		t.Fatalf("%+v", res)
	}
	if _, ok := r.answerOf(t, "w1"); ok {
		t.Fatal("w1 was answered out of a refused set")
	}
}

func TestOneOfSeveralAgentCardsIsNotSentAlone(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	agentCard(t, r, "region")
	agentCard(t, r, "db")
	res := r.pressOne(t, cardBody("region"))
	if res.Outcome != "rejected" || res.Message != "" || !strings.Contains(res.Reason, "together") {
		t.Fatalf("%+v", res)
	}
}

func TestAgentCardsGoOutAsOneMessage(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	agentCard(t, r, "region")
	agentCard(t, r, "db")
	res := r.pressSet(t, nil, cardBody("region"), cardBody("db"))
	if res.Outcome != "accepted" || res.Delivered != "next_turn" {
		t.Fatalf("%+v", res)
	}
	for _, want := range []string{"2 cards", "pick_region", "pick_db"} {
		if !strings.Contains(res.Message, want) {
			t.Fatalf("message lacks %q:\n%s", want, res.Message)
		}
	}
	evs, err := r.store.UIEvents("default", r.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for _, ev := range evs {
		var a chatevents.UIActionPayload
		if ev.Type == chatevents.EventUIAction && json.Unmarshal(ev.Payload, &a) == nil && a.Outcome == "accepted" {
			accepted++
		}
	}
	if accepted != 2 {
		t.Fatalf("%d presses journaled accepted", accepted)
	}
}

// The user may leave an agent's card out, by saying so; it stays and is then on its own.
func TestASkippedAgentCardStaysOpen(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	agentCard(t, r, "region")
	agentCard(t, r, "db")
	res := r.pressSet(t, []string{"db"}, cardBody("region"))
	if res.Outcome != "accepted" || strings.Contains(res.Message, "pick_db") {
		t.Fatalf("%+v", res)
	}
	if res := r.pressOne(t, cardBody("db")); res.Outcome != "accepted" || !strings.Contains(res.Message, "pick_db") {
		t.Fatalf("%+v", res)
	}
}

// A worker waiting on its question gets the press on its own card with the answer.
func TestAPressOnAWaitingWorkersCardGoesWithItsAnswer(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	askLive(t, r, "w1", "Which region?")
	body := `{"version":"1.0","createSurface":{"surfaceId":"db","components":[{"id":"root","component":"Column","children":["b"]},` +
		`{"id":"bl","component":"Text","text":"Pick"},{"id":"b","component":"Button","child":"bl","action":{"event":{"name":"pick_db","context":{}}}}]}}`
	r.appendLive(t, chatevents.EventUISurface, chatevents.UISurfacePayload{SurfaceID: "db", Provenance: "agent", AgentID: "w1", Message: json.RawMessage(body)})
	res := r.pressSet(t, nil, answerBody("w1", "eu"), cardBody("db"))
	if res.Outcome != "accepted" || res.Delivered != "answer" || res.Message != "" {
		t.Fatalf("%+v", res)
	}
	if got, _ := r.answerOf(t, "w1"); !strings.HasPrefix(got, "eu\n\n") || !strings.Contains(got, "pick_db") {
		t.Fatalf("w1 got %q", got)
	}
}

// A button nothing refers to is never drawn, so it holds up no other card.
func TestAnUndrawnButtonIsNotWaitedFor(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	agentCard(t, r, "region")
	body := `{"version":"1.0","createSurface":{"surfaceId":"ghost","components":[{"id":"root","component":"Text","text":"x"},` +
		`{"id":"bl","component":"Text","text":"Pick"},{"id":"b","component":"Button","child":"bl","action":{"event":{"name":"ghost","context":{}}}}]}}`
	r.appendLive(t, chatevents.EventUISurface, chatevents.UISurfacePayload{SurfaceID: "ghost", Provenance: "agent", Message: json.RawMessage(body)})
	if res := r.pressOne(t, cardBody("region")); res.Outcome != "accepted" {
		t.Fatalf("%+v", res)
	}
}

func TestASetEnvelopeIsStrict(t *testing.T) {
	for _, raw := range []string{
		`{"version":"1.0","actions":[]}`,
		`{"version":"1.0","actions":[{"name":"a"}]}`,
		`{"version":"1.0","actions":"x"}`,
		`{"version":"1.0","actions":[],"extra":1}`,
		`{"version":"1.0","action":{},"actions":[]}`,
	} {
		if _, err := a2ui.ParsePress([]byte(raw), a2ui.DefaultLimits); err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
}
