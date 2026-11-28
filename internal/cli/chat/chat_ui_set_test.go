package chat

import (
	"encoding/json"
	"fmt"
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

// revision names the version of a card a renderer shows: the turn and seq of the
// last journal event that drew it.
func (r *sysRig) revision(t *testing.T, id string) string {
	t.Helper()
	evs, err := r.store.UIEvents("default", r.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	rev := ""
	for _, ev := range evs {
		var p chatevents.UISurfacePayload
		if ev.Type == chatevents.EventUISurface && json.Unmarshal(ev.Payload, &p) == nil && p.SurfaceID == id {
			rev = fmt.Sprintf("%s:%d", ev.TurnID, ev.Seq)
		}
	}
	return rev
}

// pressSet sends a set as a renderer does: each action names the version of the
// card it answers, unless the test says which.
func (r *sysRig) pressSet(t *testing.T, skipped []string, bodies ...map[string]any) UIActionResult {
	t.Helper()
	for i, b := range bodies {
		if _, set := b["revision"]; set {
			continue
		}
		with := map[string]any{"revision": r.revision(t, fmt.Sprint(b["surfaceId"]))}
		for k, v := range b {
			with[k] = v
		}
		bodies[i] = with
	}
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

// rewrite changes what a live card's button does, as its agent can.
func rewrite(t *testing.T, r *sysRig, id, action string) {
	t.Helper()
	body := `{"version":"1.0","updateComponents":{"surfaceId":"` + id + `","components":[{"id":"bl","component":"Text","text":"Delete the files"},` +
		`{"id":"b","component":"Button","child":"bl","action":{"event":{"name":"` + action + `","context":{}}}}]}}`
	r.appendLive(t, chatevents.EventUISurface, chatevents.UISurfacePayload{SurfaceID: id, Provenance: "agent", Message: json.RawMessage(body)})
}

// A press answers the card as it was shown: once the agent has changed it, the
// press goes nowhere, alone or in a set.
func TestAPressOnAChangedCardIsRefused(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	askLive(t, r, "w1", "Which region?")
	agentCard(t, r, "files")
	old := r.revision(t, "files")
	// The same action name, another meaning: only the version tells them apart.
	rewrite(t, r, "files", "pick_files")
	stale := cardBody("files")
	stale["revision"] = old
	res := r.pressSet(t, nil, answerBody("w1", "eu"), stale)
	if res.Outcome != "rejected" || res.Message != "" || !strings.Contains(res.Reason, "changed") {
		t.Fatalf("%+v", res)
	}
	if got, ok := r.answerOf(t, "w1"); ok {
		t.Fatalf("part of a refused set was delivered: %q", got)
	}
	bare := cardBody("files")
	bare["revision"] = ""
	if res := r.pressSet(t, nil, answerBody("w1", "eu"), bare); res.Outcome != "rejected" || !strings.Contains(res.Reason, "revision") {
		t.Fatalf("a set press that names no version: %+v", res)
	}
	if res := r.pressSet(t, nil, answerBody("w1", "eu"), cardBody("files")); res.Outcome != "accepted" {
		t.Fatalf("%+v", res)
	}
}

func TestASinglePressOnAChangedCardIsRefused(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	agentCard(t, r, "files")
	stale := cardBody("files")
	stale["revision"] = r.revision(t, "files")
	rewrite(t, r, "files", "pick_files")
	if res := r.pressOne(t, stale); res.Outcome != "rejected" || res.Message != "" || !strings.Contains(res.Reason, "changed") {
		t.Fatalf("%+v", res)
	}
	stale["revision"] = r.revision(t, "files")
	if res := r.pressOne(t, stale); res.Outcome != "accepted" {
		t.Fatalf("%+v", res)
	}
}

// bigCard journals an agent card whose button carries size bytes the agent put there.
func bigCard(t *testing.T, r *sysRig, id string, size int) {
	t.Helper()
	ctx := map[string]any{}
	for i := 0; size > 0; i++ {
		n := min(size, 3500)
		ctx[fmt.Sprintf("k%d", i)] = strings.Repeat("x", n)
		size -= n
	}
	body, _ := json.Marshal(map[string]any{"version": "1.0", "createSurface": map[string]any{"surfaceId": id, "components": []any{
		map[string]any{"id": "root", "component": "Column", "children": []any{"b"}},
		map[string]any{"id": "bl", "component": "Text", "text": "Go"},
		map[string]any{"id": "b", "component": "Button", "child": "bl", "action": map[string]any{"event": map[string]any{"name": "pick_" + id, "context": ctx}}},
	}}})
	r.appendLive(t, chatevents.EventUISurface, chatevents.UISurfacePayload{SurfaceID: id, Provenance: "agent", Message: body})
}

func bigBody(t *testing.T, r *sysRig, id string) map[string]any {
	t.Helper()
	s := r.surfaceNow(t, id)
	action, _ := s.Components["b"]["action"].(map[string]any)
	ev, _ := action["event"].(map[string]any)
	return map[string]any{"name": "pick_" + id, "surfaceId": id, "sourceComponentId": "b", "context": ev["context"]}
}

// A next-turn message over what a chat message holds is refused before any card
// is settled or any answer stored.
func TestAnOversizedSetIsRefusedBeforeAnythingSettles(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	askLive(t, r, "w1", "Which region?")
	ids := []string{"a", "b", "c", "d", "e"}
	for _, id := range ids {
		bigCard(t, r, id, 3600)
	}
	bodies := []map[string]any{answerBody("w1", "eu")}
	for _, id := range ids {
		bodies = append(bodies, bigBody(t, r, id))
	}
	res := r.pressSet(t, nil, bodies...)
	if res.Outcome != "rejected" || res.Message != "" || !strings.Contains(res.Reason, "16384") {
		t.Fatalf("outcome %s, %d bytes, %q", res.Outcome, len(res.Message), res.Reason)
	}
	if _, ok := r.answerOf(t, "w1"); ok {
		t.Fatal("an answer went out of a refused set")
	}
	// Nothing was settled: a smaller set of the same cards still goes.
	res = r.pressSet(t, []string{"b", "c", "d", "e"}, answerBody("w1", "eu"), bigBody(t, r, "a"))
	if res.Outcome != "accepted" || len(res.Message) > 16<<10 {
		t.Fatalf("%+v", res)
	}
}

// What the caller appends (a message's choices and forms) counts too.
func TestASetCountsWhatTheCallerAppends(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	agentCard(t, r, "region")
	body := cardBody("region")
	body["revision"] = r.revision(t, "region")
	raw, _ := json.Marshal(map[string]any{"version": "1.0", "actions": []any{body}, "appendBytes": 16 << 10})
	res, err := r.uiRig.press(t, string(raw))
	if err != nil || res.Outcome != "rejected" || !strings.Contains(res.Reason, "16384") {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestAnOversizedSinglePressIsRefused(t *testing.T) {
	r := newSysRig(t)
	liveTurn(t, r)
	bigCard(t, r, "a", 17<<10)
	if res := r.pressOne(t, bigBody(t, r, "a")); res.Outcome != "rejected" || res.Message != "" || !strings.Contains(res.Reason, "16384") {
		t.Fatalf("outcome %s, %d bytes", res.Outcome, len(res.Message))
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
