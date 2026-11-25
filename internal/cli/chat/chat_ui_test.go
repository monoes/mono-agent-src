package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

const uiTestBody = `{"version":"1.0","updateComponents":{"surfaceId":"s1","components":[
 {"id":"root","component":"Column","children":["b"]},
 {"id":"bl","component":"Text","text":"Go"},
 {"id":"b","component":"Button","child":"bl","action":{"event":{"name":"%s","context":{}}}}]}}`

type uiRig struct {
	dbPath string
	store  *ai.AIStore
	conv   ai.Conversation
	cfg    *Deps
	sys    *SystemActions
	turns  int
}

func newUIRig(t *testing.T) *uiRig {
	t.Helper()
	asOperator(t)
	r := &uiRig{dbPath: newChatCLITestDB(t), sys: &SystemActions{}}
	r.store = openTestChatStore(t, r.dbPath)
	c, err := r.store.CreateConversation("default", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	r.conv = c
	r.cfg = testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default", JSONOutput: true})
	return r
}

// surface journals a finished turn that holds the surface s1 with one button named action.
func (r *uiRig) surface(t *testing.T, prov, agent, action string, extra ...string) {
	t.Helper()
	r.turns++
	turn := "t" + string(rune('a'+r.turns))
	if _, _, err := r.store.CreateTurn(r.conv.ID, "default", turn, "i", "go"); err != nil {
		t.Fatal(err)
	}
	msgs := append([]string{`{"version":"1.0","createSurface":{"surfaceId":"s1"}}`, strings.Replace(uiTestBody, "%s", action, 1)}, extra...)
	for _, m := range msgs {
		p := chatevents.UISurfacePayload{SurfaceID: "s1", Provenance: prov, AgentID: agent, Message: json.RawMessage(m)}
		if _, err := r.store.AppendEvent("default", r.conv.ID, turn, chatevents.EventUISurface, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := r.store.FinalizeTurn("default", r.conv.ID, turn, chatevents.StatusCompleted, "", nil, false); err != nil {
		t.Fatal(err)
	}
}

func uiPress(name string) string {
	return `{"version":"1.0","action":{"name":"` + name + `","surfaceId":"s1","sourceComponentId":"b","timestamp":"2026-11-24T10:00:00Z","context":{"k":"v"}}}`
}

func (r *uiRig) press(t *testing.T, raw string) (UIActionResult, error) {
	t.Helper()
	cmd := newChatUIActionCmd(r.cfg, r.sys)
	cmd.SetArgs([]string{r.conv.ID, "--json", raw})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	var res UIActionResult
	if err == nil {
		if jerr := json.Unmarshal([]byte(out), &res); jerr != nil {
			t.Fatalf("output %q: %v", out, jerr)
		}
	}
	return res, err
}

// actions are the ui.action events journaled so far.
func (r *uiRig) actions(t *testing.T) []chatevents.UIActionPayload {
	t.Helper()
	evs, err := r.store.UIEvents("default", r.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []chatevents.UIActionPayload
	for _, e := range evs {
		if e.Type == chatevents.EventUIAction {
			var p chatevents.UIActionPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			out = append(out, p)
		}
	}
	return out
}

func TestUIActionAcceptedGoesToTheNextUserMessage(t *testing.T) {
	r := newUIRig(t)
	r.surface(t, "agent", "", "submit_form")
	res, err := r.press(t, uiPress("submit_form"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "accepted" || res.Delivered != "next_turn" || !strings.Contains(res.Message, `"name":"submit_form"`) || !strings.Contains(res.Message, `"k":"v"`) {
		t.Fatalf("result %+v", res)
	}
	got := r.actions(t)
	if len(got) != 1 || got[0].Outcome != "accepted" || got[0].Name != "submit_form" || got[0].Context["k"] != "v" {
		t.Fatalf("journal %+v", got)
	}
}

// The registry is rebuilt from the journal alone: the press is checked against the live surface.
func TestUIActionRejectedAndStale(t *testing.T) {
	r := newUIRig(t)
	r.surface(t, "agent", "", "submit_form")
	cases := map[string]string{
		"a name the button never declared": uiPress("delete_everything"),
		"a surface that never existed":     strings.Replace(uiPress("submit_form"), `"s1"`, `"ghost"`, 1),
		"a component that is no button":    strings.Replace(uiPress("submit_form"), `"b"`, `"bl"`, 1),
	}
	for name, raw := range cases {
		res, err := r.press(t, raw)
		if err != nil || res.Outcome != "rejected" || res.Delivered != "" || res.Reason == "" {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	// Close the surface: the same press is now stale, not forged.
	r.turns++
	if _, _, err := r.store.CreateTurn(r.conv.ID, "default", "tdel", "i", "go"); err != nil {
		t.Fatal(err)
	}
	del := chatevents.UISurfacePayload{SurfaceID: "s1", Provenance: "agent", Message: json.RawMessage(`{"version":"1.0","deleteSurface":{"surfaceId":"s1"}}`)}
	if _, err := r.store.AppendEvent("default", r.conv.ID, "tdel", chatevents.EventUISurface, del); err != nil {
		t.Fatal(err)
	}
	res, err := r.press(t, uiPress("submit_form"))
	if err != nil || res.Outcome != "stale" {
		t.Fatalf("after delete: %+v %v", res, err)
	}
	got := r.actions(t)
	if len(got) != 4 || got[3].Outcome != "stale" || got[0].Outcome != "rejected" {
		t.Fatalf("journal %+v", got)
	}
}

func TestUIActionAnswersTheWaitingWorker(t *testing.T) {
	r := newUIRig(t)
	r.surface(t, "agent", "w1", "submit_form")
	if _, _, err := r.store.CreateTurn(r.conv.ID, "default", "live", "i", "go on"); err != nil {
		t.Fatal(err)
	}
	q := chatevents.AgentMessagePayload{AgentID: "w1", Direction: "question", QuestionID: "q1", From: "w1", To: "user", Text: "Which?"}
	if _, err := r.store.AppendEvent("default", r.conv.ID, "live", chatevents.EventAgentMessage, q); err != nil {
		t.Fatal(err)
	}
	res, err := r.press(t, uiPress("submit_form"))
	if err != nil || res.Delivered != "answer" || res.Message != "" || res.AgentID != "w1" {
		t.Fatalf("%+v %v", res, err)
	}
	text, ok, err := r.store.TakeAnswer("default", "live", "w1", "q1")
	if err != nil || !ok || !strings.Contains(text, `"name":"submit_form"`) {
		t.Fatalf("answer %q %v %v", text, ok, err)
	}
	// Once that question is closed the next press goes to the next user message.
	closed := chatevents.AgentMessagePayload{AgentID: "w1", Direction: "followup", QuestionID: "q1", From: "user", To: "w1", Text: "x"}
	if _, err := r.store.AppendEvent("default", r.conv.ID, "live", chatevents.EventAgentMessage, closed); err != nil {
		t.Fatal(err)
	}
	if res, _ := r.press(t, uiPress("submit_form")); res.Delivered != "next_turn" {
		t.Errorf("closed question still got the answer: %+v", res)
	}
}

func TestUIActionOfASystemSurfaceIsHandedToItsPrefixHandler(t *testing.T) {
	r := newUIRig(t)
	r.surface(t, "system", "", "hil.approve")
	var calls []a2ui.Action
	var surf *a2ui.Surface
	if err := r.sys.Register("hil.", func(_ context.Context, a a2ui.Action, s *a2ui.Surface) error {
		calls, surf = append(calls, a), s
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res, err := r.press(t, uiPress("hil.approve"))
	if err != nil || res.Outcome != "accepted" || res.Delivered != "system" || !res.Handled || res.Message != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(calls) != 1 || calls[0].Name != "hil.approve" || calls[0].Context["k"] != "v" || surf == nil || surf.Prov != a2ui.FromSystem {
		t.Fatalf("handler saw %+v %+v", calls, surf)
	}
	// A press the guard refuses never reaches the handler.
	if res, _ := r.press(t, uiPress("hil.reject")); res.Outcome != "rejected" || len(calls) != 1 {
		t.Errorf("undeclared system action: %+v, %d calls", res, len(calls))
	}
	// A handler that refuses turns the press into a rejection in the journal.
	_ = r.sys.Register("hil.", func(context.Context, a2ui.Action, *a2ui.Surface) error { return errors.New("item already decided") })
	res, err = r.press(t, uiPress("hil.approve"))
	if err != nil || res.Outcome != "rejected" || !strings.Contains(res.Reason, "already decided") {
		t.Fatalf("%+v %v", res, err)
	}
	if got := r.actions(t); len(got) != 3 || got[0].Outcome != "accepted" || got[2].Outcome != "rejected" {
		t.Errorf("journal %+v", got)
	}
}

func TestUIActionOfASystemSurfaceWithoutAHandlerIsOnlyJournaled(t *testing.T) {
	r := newUIRig(t)
	r.surface(t, "system", "", "mission.confirm")
	res, err := r.press(t, uiPress("mission.confirm"))
	if err != nil || res.Outcome != "accepted" || res.Handled || res.Delivered != "system" {
		t.Fatalf("%+v %v", res, err)
	}
}

// A system surface may only carry reserved action names, and only a system surface may carry one.
func TestUIActionPrefixAndProvenanceMustAgree(t *testing.T) {
	r := newUIRig(t)
	r.surface(t, "system", "", "plain_name")
	called := false
	_ = r.sys.Register("hil.", func(context.Context, a2ui.Action, *a2ui.Surface) error { called = true; return nil })
	res, err := r.press(t, uiPress("plain_name"))
	if err != nil || res.Outcome != "rejected" || called {
		t.Fatalf("%+v %v called %v", res, err, called)
	}
}

func TestUIActionRegisterOnlyTakesReservedPrefixes(t *testing.T) {
	if err := (&SystemActions{}).Register("custom.", nil); err == nil {
		t.Error("a non-reserved prefix was registered")
	}
	if err := RegisterSystemActionHandler("system.", func(context.Context, a2ui.Action, *a2ui.Surface) error { return nil }); err != nil {
		t.Error(err)
	}
	DefaultSystemActions = &SystemActions{}
}

func TestUIActionIsTheUsersAndMalformedInputIsRefusedUnjournaled(t *testing.T) {
	r := newUIRig(t)
	r.surface(t, "agent", "", "submit_form")
	r.cfg.RequireOperator = func(string) error { return errNotOperator }
	if _, err := r.press(t, uiPress("submit_form")); err == nil || exitCodeFor(err) != 3 {
		t.Fatalf("agent press: %v", err)
	}
	r.cfg.RequireOperator = func(string) error { return nil }
	for _, raw := range []string{`not json`, `{"version":"1.0","createSurface":{"surfaceId":"x"}}`, `{"version":"1.0","action":{"name":"x"}}`} {
		if _, err := r.press(t, raw); err == nil || exitCodeFor(err) != 3 {
			t.Errorf("%s: %v", raw, err)
		}
	}
	cmd := newChatUIActionCmd(r.cfg, r.sys)
	cmd.SetArgs([]string{"no-such-conversation", "--json", uiPress("submit_form")})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var err error
	captureStdout(t, func() { err = cmd.Execute() })
	if err == nil || exitCodeFor(err) != 2 {
		t.Errorf("unknown conversation: %v (code %d)", err, exitCodeFor(err))
	}
	if got := r.actions(t); len(got) != 0 {
		t.Errorf("refused input was journaled: %+v", got)
	}
}

func TestUIConfigSeedsFromTheJournal(t *testing.T) {
	r := newUIRig(t)
	r.surface(t, "agent", "", "submit_form")
	db, err := r.cfg.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	evs, err := uiConfig(db.DB, "default", r.conv.ID).Seed()
	if err != nil || len(evs) != 2 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
}
