package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/workflow"
)

// fakeCLI answers RunCLI: a canned output per command prefix, every call recorded.
type fakeCLI struct {
	calls   [][]string
	outputs map[string]string // key: the first two args joined by a space
	errs    map[string]error
}

func (f *fakeCLI) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	key := strings.Join(args[:min(2, len(args))], " ")
	if err := f.errs[key]; err != nil {
		return nil, err
	}
	return []byte(f.outputs[key]), nil
}

func (f *fakeCLI) called(prefix ...string) int {
	n := 0
	for _, c := range f.calls {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], " ") == strings.Join(prefix, " ") {
			n++
		}
	}
	return n
}

type sysRig struct {
	*uiRig
	cli *fakeCLI
}

func newSysRig(t *testing.T) *sysRig {
	r := &sysRig{uiRig: newUIRig(t), cli: &fakeCLI{outputs: map[string]string{}, errs: map[string]error{}}}
	r.cfg.RunCLI = r.cli.run
	registerSystemHandlers(r.cfg, r.sys)
	return r
}

// show journals built system messages as the host would.
func (r *sysRig) show(t *testing.T, ref chatevents.UIRef, msgs [][]byte, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var ps []chatevents.UISurfacePayload
	for _, m := range msgs {
		pm, perr := a2ui.ParseMessage(m, a2ui.DefaultLimits)
		if perr != nil {
			t.Fatal(perr)
		}
		rf := ref
		ps = append(ps, chatevents.UISurfacePayload{SurfaceID: pm.SurfaceID, Provenance: "system", Ref: &rf, Message: m})
	}
	if _, err := r.store.RecordUISurfaces("default", r.conv.ID, ps); err != nil {
		t.Fatal(err)
	}
}

func (r *sysRig) surfaceNow(t *testing.T, id string) *a2ui.Surface {
	t.Helper()
	evs, err := r.store.UIEvents("default", r.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	return dynorg.RebuildRegistry(evs).Registry.Surface(id)
}

func (r *sysRig) press(t *testing.T, name, surface, button string, ctx map[string]any) (UIActionResult, error) {
	t.Helper()
	// As a renderer does: the button's literal digest goes with the press,
	// unless the test says what to send.
	if _, set := ctx[surfaces.DigestKey]; !set {
		if s := r.surfaceNow(t, surface); s != nil {
			action, _ := s.Components[button]["action"].(map[string]any)
			ev, _ := action["event"].(map[string]any)
			declared, _ := ev["context"].(map[string]any)
			if d, ok := declared[surfaces.DigestKey]; ok {
				with := map[string]any{surfaces.DigestKey: d}
				for k, v := range ctx {
					with[k] = v
				}
				ctx = with
			}
		}
	}
	raw, _ := json.Marshal(map[string]any{"version": "1.0", "action": map[string]any{"name": name, "surfaceId": surface,
		"sourceComponentId": button, "timestamp": "2026-11-24T10:00:00Z", "context": ctx}})
	return r.uiRig.press(t, string(raw))
}

func hilItem() surfaces.HILItem {
	return surfaces.HILItem{ID: "482", Digest: "d1", NodeName: "Send DM", WorkflowName: "Outreach",
		ReadonlyData: map[string]any{"to": "@sara"},
		EditableData: map[string]any{"message": "Hi", "delay_minutes": float64(5)}}
}

const hilListJSON = `[{"id":"482","digest":"d1","node_name":"Send DM","workflow_name":"Outreach","readonly_data":{"to":"@sara"},"editable_data":{"message":"Hi","delay_minutes":5}}]`

func (r *sysRig) showHIL(t *testing.T) {
	msgs, err := surfaces.HIL(hilItem())
	r.show(t, chatevents.UIRef{Kind: surfaces.RefHIL, ID: "482", Digest: "d1"}, msgs, err)
}

func TestHILApproveRunsTheCLIWithTypedEditsAndSettlesTheCard(t *testing.T) {
	r := newSysRig(t)
	r.showHIL(t)
	r.cli.outputs["hil list"] = hilListJSON
	r.cli.outputs["hil approve"] = `{"id":"482","status":"approved"}`
	res, err := r.press(t, "hil.approve", "hil-482", "btn-approve", map[string]any{"id": "482", "message": "Hi Sara!", "delay_minutes": "7"})
	if err != nil || res.Outcome != "accepted" || !res.Handled {
		t.Fatalf("%+v %v", res, err)
	}
	last := r.cli.calls[len(r.cli.calls)-1]
	if strings.Join(last, "|") != `hil|approve|482|--data|{"delay_minutes":7,"message":"Hi Sara!"}|--if-digest|d1` {
		t.Fatalf("command %v", last)
	}
	s := r.surfaceNow(t, "hil-482")
	if s == nil || hasButton(s) || s.Components["note"]["text"] != "Approved by you" {
		t.Fatalf("surface after approval: %+v", s)
	}
}

// Pressing again must not approve again: the card is a notice with no button, so the
// press is refused before any handler runs.
func TestSecondPressIsRefusedAndRunsNothing(t *testing.T) {
	r := newSysRig(t)
	r.showHIL(t)
	r.cli.outputs["hil list"] = hilListJSON
	r.cli.outputs["hil approve"] = `{"status":"approved"}`
	press := func() UIActionResult {
		res, err := r.press(t, "hil.approve", "hil-482", "btn-approve", map[string]any{"id": "482", "message": "Hi", "delay_minutes": "5"})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if press().Outcome != "accepted" {
		t.Fatal("first press")
	}
	if got := press(); got.Outcome == "accepted" {
		t.Fatalf("second press accepted: %+v", got)
	}
	if n := r.cli.called("hil", "approve"); n != 1 {
		t.Fatalf("hil approve ran %d times", n)
	}
}

func TestHILRejectAndFailureLeavesTheCardLive(t *testing.T) {
	r := newSysRig(t)
	r.showHIL(t)
	r.cli.errs["hil reject"] = errors.New("database is locked")
	r.cli.outputs["hil list"] = hilListJSON
	res, err := r.press(t, "hil.reject", "hil-482", "btn-reject", map[string]any{"id": "482"})
	if err != nil || res.Outcome != "rejected" || !strings.Contains(res.Reason, "database is locked") {
		t.Fatalf("%+v %v", res, err)
	}
	if s := r.surfaceNow(t, "hil-482"); s == nil || !s.DeclaresAction("btn-reject", "hil.reject") {
		t.Fatal("a failed decision must leave the card live")
	}
	delete(r.cli.errs, "hil reject")
	r.cli.outputs["hil reject"] = `{"id":"482","status":"rejected"}`
	res, _ = r.press(t, "hil.reject", "hil-482", "btn-reject", map[string]any{"id": "482"})
	if res.Outcome != "accepted" || r.surfaceNow(t, "hil-482").Components["note"]["text"] != "Rejected by you" {
		t.Fatalf("%+v", res)
	}
}

// A press whose context names another item is refused by the handler (the registry
// only checks the button): nothing runs.
func TestForgedContextIDIsRefusedEndToEnd(t *testing.T) {
	r := newSysRig(t)
	r.showHIL(t)
	r.cli.outputs["hil list"] = hilListJSON
	for name, ctx := range map[string]map[string]any{
		"another id":     {"id": "999", "message": "x", "delay_minutes": "1"},
		"no id":          {"message": "x"},
		"unknown field":  {"id": "482", "message": "x", "delay_minutes": "1", "admin": "yes"},
		"list for field": {"id": "482", "message": []any{"a", "b"}},
	} {
		res, err := r.press(t, "hil.approve", "hil-482", "btn-approve", ctx)
		if err != nil || res.Outcome != "rejected" {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	if n := r.cli.called("hil", "approve"); n != 0 {
		t.Fatalf("a refused press ran hil approve %d times", n)
	}
	if !hasButton(r.surfaceNow(t, "hil-482")) {
		t.Fatal("refusals must leave the card live")
	}
}

func TestSendApprovalPassesTextAndReportsAFailedSend(t *testing.T) {
	r := newSysRig(t)
	p := hilsend.Payload{Kind: "send", ID: "7c1e", State: hilsend.StatePending, Action: hilsend.ActionDM, Channel: "instagram",
		Recipient: hilsend.Recipient{Name: "Ada"}, Text: "Hi Ada", Subject: "Hello"}
	msgs, err := surfaces.Send(p, "d1")
	r.show(t, chatevents.UIRef{Kind: surfaces.RefSend, ID: "7c1e", Digest: "d1"}, msgs, err)
	r.cli.outputs["hil approve"] = `{"id":"7c1e","state":"failed","error":"rate limited"}`
	res, _ := r.press(t, "hil.approve", "send-7c1e", "btn-approve", map[string]any{"id": "7c1e", "text": "Hi Ada, edited", "subject": "Hello"})
	if res.Outcome != "rejected" || !strings.Contains(res.Reason, "rate limited") {
		t.Fatalf("%+v", res)
	}
	if got := r.cli.calls[len(r.cli.calls)-1][4]; got != `{"subject":"Hello","text":"Hi Ada, edited"}` {
		t.Fatalf("data %s", got)
	}
	if s := r.surfaceNow(t, "send-7c1e"); hasButton(s) || !strings.Contains(s.Components["note"]["text"].(string), "rate limited") {
		t.Fatal("a send that failed after approval is settled and says so")
	}
	// the edit round-trips whole at the returned-string cap
	r2 := newSysRig(t)
	p.Text = strings.Repeat("é", 10<<10)
	m2, err := surfaces.Send(p, "d1")
	r2.show(t, chatevents.UIRef{Kind: surfaces.RefSend, ID: "7c1e", Digest: "d1"}, m2, err)
	r2.cli.outputs["hil approve"] = `{"state":"sent"}`
	res, _ = r2.press(t, "hil.approve", "send-7c1e", "btn-approve", map[string]any{"id": "7c1e", "text": p.Text, "subject": "Hello"})
	if res.Outcome != "accepted" || !strings.Contains(r2.cli.calls[len(r2.cli.calls)-1][4], p.Text) {
		t.Fatalf("%+v", res)
	}
}

func openQuestion(t *testing.T, r *sysRig, agent, qid string) {
	t.Helper()
	if _, _, err := r.store.CreateTurn(r.conv.ID, "default", "live", "i", "go"); err != nil {
		t.Fatal(err)
	}
	q := chatevents.AgentMessagePayload{AgentID: agent, Direction: "question", QuestionID: qid, From: agent, To: "user", Text: "Which region?"}
	if _, err := r.store.AppendEvent("default", r.conv.ID, "live", chatevents.EventAgentMessage, q); err != nil {
		t.Fatal(err)
	}
}

func TestAskAnswerReachesTheWaitingWorker(t *testing.T) {
	r := newSysRig(t)
	openQuestion(t, r, "w1", "q1")
	msgs, err := surfaces.Ask("w1.q1", []surfaces.Question{{Text: "Which region?", Options: []string{"eu", "us"}}, {Text: "Anything else?"}})
	r.show(t, chatevents.UIRef{Kind: surfaces.RefAsk, ID: "w1.q1"}, msgs, err)
	res, err := r.press(t, "system.answer", "ask-w1.q1", "btn-continue", map[string]any{"questionId": "w1.q1", "q0": []any{"eu"}, "q1": "fast"})
	if err != nil || res.Outcome != "accepted" {
		t.Fatalf("%+v %v", res, err)
	}
	text, ok, err := r.store.TakeAnswer("default", "live", "w1", "q1")
	if err != nil || !ok || text != "Which region?\neu\n\nAnything else?\nfast" {
		t.Fatalf("answer %q %v %v", text, ok, err)
	}
	if s := r.surfaceNow(t, "ask-w1.q1"); hasButton(s) {
		t.Fatal("card still has its button")
	}
}

func TestAskSingleQuestionIsThePlainAnswerAndNeedsAnOpenQuestion(t *testing.T) {
	r := newSysRig(t)
	msgs, err := surfaces.Ask("w1.q1", []surfaces.Question{{Text: "Which region?"}})
	r.show(t, chatevents.UIRef{Kind: surfaces.RefAsk, ID: "w1.q1"}, msgs, err)
	ctx := map[string]any{"questionId": "w1.q1", "q0": "eu"}
	if res, _ := r.press(t, "system.answer", "ask-w1.q1", "btn-continue", ctx); res.Outcome != "rejected" || !strings.Contains(res.Reason, "not open") {
		t.Fatalf("answer to a question nobody waits on: %+v", res)
	}
	openQuestion(t, r, "w1", "q1")
	if res, _ := r.press(t, "system.answer", "ask-w1.q1", "btn-continue", map[string]any{"questionId": "w1.q1", "q0": ""}); res.Outcome != "rejected" {
		t.Fatalf("empty answer accepted: %+v", res)
	}
	if res, _ := r.press(t, "system.answer", "ask-w1.q1", "btn-continue", ctx); res.Outcome != "accepted" {
		t.Fatalf("%+v", res)
	}
	if text, _, _ := r.store.TakeAnswer("default", "live", "w1", "q1"); text != "eu" {
		t.Fatalf("answer %q", text)
	}
}

func TestMissionButtons(t *testing.T) {
	r := newSysRig(t)
	plan := chatevents.MissionPlanPayload{SessionID: 17, Title: "Ship docs", Goal: "Rewrite", TaskCount: 4, EstimatedBudgetUSD: 2}
	ref := chatevents.UIRef{Kind: surfaces.RefMissionPlan, ID: "17"}
	msgs, err := surfaces.MissionPlan(plan, 10)
	r.show(t, ref, msgs, err)
	ctx := map[string]any{"sessionId": "17"}

	res, _ := r.press(t, "mission.edit", "mission-17", "btn-edit", ctx)
	if res.Outcome != "accepted" || res.Delivered != "next_turn" || res.Message != "I want to change the plan of mission #17." || r.cli.called("task") != 0 {
		t.Fatalf("edit: %+v", res)
	}
	r.cli.outputs["task session"] = `{}`
	if res, _ = r.press(t, "mission.approve", "mission-17", "btn-approve", ctx); res.Outcome == "accepted" || len(r.cli.calls) != 0 {
		t.Fatalf("a plan card approved: %+v %v", res, r.cli.calls)
	}
	res, _ = r.press(t, "mission.stop", "mission-17", "btn-stop", ctx)
	if res.Outcome != "accepted" || r.cli.called("task", "session", "stop") != 1 || hasButton(r.surfaceNow(t, "mission-17")) {
		t.Fatalf("stop: %+v", res)
	}
	// a forged session id never runs a command
	n := len(r.cli.calls)
	r2 := newSysRig(t)
	r2.show(t, ref, msgs, nil)
	if res, _ = r2.press(t, "mission.stop", "mission-17", "btn-stop", map[string]any{"sessionId": "18"}); res.Outcome != "rejected" || len(r2.cli.calls) != 0 {
		t.Fatalf("forged: %+v", res)
	}
	_ = n
}

func TestMissionClose(t *testing.T) {
	r := newSysRig(t)
	rep := chatevents.MissionReportPayload{SessionID: 17, Title: "Ship docs", State: "done", TaskCount: 2, Done: 2, CloseProposed: true, Report: "ok"}
	msgs, err := surfaces.MissionReport(rep)
	r.show(t, chatevents.UIRef{Kind: surfaces.RefMissionReport, ID: "17"}, msgs, err)
	res, _ := r.press(t, "mission.close", "mission-report-17", "btn-close", map[string]any{"sessionId": "17"})
	if res.Outcome != "accepted" || strings.Join(r.cli.calls[0], " ") != "task session close 17" {
		t.Fatalf("%+v %v", res, r.cli.calls)
	}
}

// startChat gives the conversation a finished work turn; sync-hil writes nothing before one.
func startChat(t *testing.T, r *sysRig) {
	t.Helper()
	if _, _, err := r.store.CreateTurn(r.conv.ID, "default", "work-1", "i", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.FinalizeTurn("default", r.conv.ID, "work-1", chatevents.StatusCompleted, "", nil, false); err != nil {
		t.Fatal(err)
	}
}

func TestSyncHILWritesNothingBeforeTheConversationStarts(t *testing.T) {
	r := newSysRig(t)
	r.cli.outputs["hil list"] = hilListJSON
	if res := syncHIL(t, r); len(res.Surfaced) != 0 {
		t.Fatalf("surfaced into an unstarted chat: %+v", res)
	}
	// No turn at all: any turn locks the chat's mode (migration 163) and counts as the chat having started.
	if turns, _, err := r.store.ListTurns(r.conv.ID, "default", "", 10); err != nil || len(turns) != 0 {
		t.Fatalf("turns = %+v err=%v", turns, err)
	}
	startChat(t, r)
	if res := syncHIL(t, r); len(res.Surfaced) != 1 {
		t.Fatalf("the card must appear once the chat has started: %+v", res)
	}
	turns, _, err := r.store.ListCardTurns(r.conv.ID, "default", "", 10)
	if err != nil || len(turns) != 1 {
		t.Fatalf("card turns = %+v err=%v", turns, err)
	}
	if work, _, err := r.store.ListTurns(r.conv.ID, "default", "", 10); err != nil || len(work) != 1 || work[0].ID != "work-1" {
		t.Fatalf("the chat's turns = %+v err=%v", work, err)
	}
	for _, turn := range turns {
		// Readers of the newest or first work turn skip system turns; the card turn must be one.
		if !turn.IsSystem() || !strings.HasPrefix(turn.ID, ai.UISurfaceTurnPrefix) {
			t.Fatalf("card turn %q is not a system turn", turn.ID)
		}
	}
}

// Decided cards must not use up the chat's slots, or approvals stop appearing after the first few.
func TestSyncHILKeepsShowingNewApprovalsAfterManyDecidedOnes(t *testing.T) {
	r := newSysRig(t)
	startChat(t, r)
	for i := 0; i < 2*a2ui.MaxSurfaces+1; i++ {
		r.cli.outputs["hil list"] = fmt.Sprintf(`[{"id":"i%d","node_name":"n","readonly_data":{},"editable_data":{}}]`, i)
		res := syncHIL(t, r)
		if fmt.Sprint(res.Surfaced) != fmt.Sprintf("[hil-i%d]", i) || len(res.Skipped) != 0 {
			t.Fatalf("approval %d: %+v", i, res)
		}
	}
}

func syncHIL(t *testing.T, r *sysRig) SyncHILResult {
	t.Helper()
	res, err := runSyncHIL(context.Background(), r.cfg, r.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	return *res
}

func TestSyncHILIsIdempotentAndResolvesWhatIsNoLongerPending(t *testing.T) {
	r := newSysRig(t)
	startChat(t, r)
	send := `{"id":"s1","kind":"send","node_name":"x","send":{"kind":"send","id":"s1","state":"pending","action":"dm","channel":"instagram","recipient":{"name":"Ada"},"text":"Hi"}}`
	r.cli.outputs["hil list"] = `[` + hilListJSON[1:len(hilListJSON)-1] + `,` + send + `]`
	first := syncHIL(t, r)
	if fmt.Sprint(first.Surfaced) != "[hil-482 send-s1]" || len(first.Resolved) != 0 {
		t.Fatalf("first %+v", first)
	}
	if second := syncHIL(t, r); len(second.Surfaced) != 0 || len(second.Resolved) != 0 {
		t.Fatalf("second sync did something: %+v", second)
	}
	// the send was decided elsewhere
	r.cli.outputs["hil list"] = hilListJSON
	third := syncHIL(t, r)
	if fmt.Sprint(third.Resolved) != "[send-s1]" || len(third.Surfaced) != 0 {
		t.Fatalf("third %+v", third)
	}
	if s := r.surfaceNow(t, "send-s1"); hasButton(s) || s.Components["note"]["text"] != "No longer pending" {
		t.Fatalf("send card %+v", s)
	}
	if !hasButton(r.surfaceNow(t, "hil-482")) {
		t.Fatal("a still-pending card must stay")
	}
	if fourth := syncHIL(t, r); len(fourth.Resolved) != 0 {
		t.Fatal("a resolved card is resolved once")
	}
	// the journal is the only memory: every surfaced item reads the same through a rebuilt registry
	if r.cli.called("hil", "approve") != 0 {
		t.Fatal("sync decided something")
	}
}

func TestSyncHILStaysWithinTheSurfaceCapAndSaysWhatItSkipped(t *testing.T) {
	r := newSysRig(t)
	startChat(t, r)
	var items []string
	for i := 0; i < 10; i++ {
		items = append(items, fmt.Sprintf(`{"id":"i%d","node_name":"n","readonly_data":{},"editable_data":{}}`, i))
	}
	r.cli.outputs["hil list"] = "[" + strings.Join(items, ",") + "]"
	res := syncHIL(t, r)
	if len(res.Surfaced) != a2ui.MaxSurfaces || len(res.Skipped) != 2 {
		t.Fatalf("%+v", res)
	}
	if again := syncHIL(t, r); len(again.Surfaced) != 0 || len(again.Skipped) != 2 {
		t.Fatalf("again %+v", again)
	}
}

func TestSyncHILNeedsAConversationAndTheCLI(t *testing.T) {
	r := newSysRig(t)
	startChat(t, r)
	if _, err := runSyncHIL(context.Background(), r.cfg, "nope"); err == nil {
		t.Fatal("unknown conversation")
	}
	r.cli.errs["hil list"] = errors.New("boom")
	if _, err := runSyncHIL(context.Background(), r.cfg, r.conv.ID); err == nil {
		t.Fatal("list failure swallowed")
	}
}

// A pending item whose card is gone, or stands as a notice with no button, is
// drawn again: nothing can hide an approval for good.
func TestSyncHILRedrawsAGoneCardWhileItsItemIsPending(t *testing.T) {
	r := newSysRig(t)
	startChat(t, r)
	r.cli.outputs["hil list"] = hilListJSON
	syncHIL(t, r)
	ref := chatevents.UIRef{Kind: surfaces.RefHIL, ID: "482"}
	for name, gone := range map[string]func() ([][]byte, error){
		"deleted": func() ([][]byte, error) { return surfaces.Resolved("hil-482") },
		"notice":  func() ([][]byte, error) { return surfaces.ResolvedNotice("hil-482", "Approved by you", "success") },
	} {
		msgs, err := gone()
		r.show(t, ref, msgs, err)
		if res := syncHIL(t, r); fmt.Sprint(res.Surfaced) != "[hil-482]" {
			t.Fatalf("%s: %+v", name, res)
		}
		if s := r.surfaceNow(t, "hil-482"); s == nil || !s.DeclaresAction("btn-approve", "hil.approve") {
			t.Fatalf("%s: no approval card: %+v", name, s)
		}
		if res := syncHIL(t, r); len(res.Surfaced) != 0 {
			t.Fatalf("%s: drawn twice: %+v", name, res)
		}
	}
}

// A press that did not do what its button says (the item was decided
// elsewhere) is journaled rejected and the card shows the real state: never
// "Approved by you" for a send somebody rejected, nor the other way round.
func TestAPressThatDidNotHappenShowsTheRealState(t *testing.T) {
	send := hilsend.Payload{Kind: "send", ID: "7c1e", State: hilsend.StatePending, Action: hilsend.ActionDM, Channel: "instagram",
		Recipient: hilsend.Recipient{Name: "Ada"}, Text: "Hi Ada"}
	for _, c := range []struct {
		button, state, note string
	}{
		{"approve", "rejected", "Not sent: this send was rejected"},
		{"reject", "sent", "Not rejected: it was already sent"},
		{"reject", "approved", "Not rejected: it was already approved and is being sent"},
		{"reject", "failed", "Not rejected: it was approved and sending failed: boom"},
		{"approve", "", "No longer pending"},
	} {
		r := newSysRig(t)
		msgs, err := surfaces.Send(send, "d1")
		r.show(t, chatevents.UIRef{Kind: surfaces.RefSend, ID: "7c1e", Digest: "d1"}, msgs, err)
		r.cli.outputs["hil "+c.button] = fmt.Sprintf(`{"id":"7c1e","state":%q,"error":"boom"}`, c.state)
		r.cli.outputs["hil list"] = `[]`
		ctx := map[string]any{"id": "7c1e"}
		if c.button == "approve" {
			ctx["text"] = "Hi Ada"
		}
		res, err := r.press(t, "hil."+c.button, "send-7c1e", "btn-"+c.button, ctx)
		if err != nil || res.Outcome != "rejected" || res.Reason == "" {
			t.Fatalf("%s of a %q item: %+v %v", c.button, c.state, res, err)
		}
		s := r.surfaceNow(t, "send-7c1e")
		if s == nil || hasButton(s) || s.Components["note"]["text"] != c.note {
			t.Fatalf("%s of a %q item: card %+v, want %q", c.button, c.state, s.Components["note"], c.note)
		}
	}
}

// A refused command leaves the card live while the item still waits, and says
// it is gone when it does not.
func TestAFailedDecisionOfAGoneItemClosesItsCard(t *testing.T) {
	r := newSysRig(t)
	r.showHIL(t)
	r.cli.errs["hil reject"] = errors.New(`HIL item "482" not found or already resolved`)
	r.cli.outputs["hil list"] = `[]`
	res, err := r.press(t, "hil.reject", "hil-482", "btn-reject", map[string]any{"id": "482"})
	if err != nil || res.Outcome != "rejected" || !strings.Contains(res.Reason, "already resolved") {
		t.Fatalf("%+v %v", res, err)
	}
	if s := r.surfaceNow(t, "hil-482"); hasButton(s) || s.Components["note"]["text"] != "No longer pending" {
		t.Fatalf("card %+v", s.Components)
	}
}

func listedWith(digest, message string) string {
	return `[{"id":"482","node_name":"Send DM","workflow_name":"Outreach","digest":"` + digest + `","readonly_data":{"to":"@sara"},"editable_data":{"message":` +
		fmt.Sprintf("%q", message) + `,"delay_minutes":5}}]`
}

// A card is tied to the item as it was drawn: a changed item is drawn again by
// sync, and a press on the old card decides nothing.
func TestAChangedItemIsRedrawnAndItsOldCardDecidesNothing(t *testing.T) {
	r := newSysRig(t)
	startChat(t, r)
	r.cli.outputs["hil list"] = listedWith("d1", "Hi")
	syncHIL(t, r)
	if ref := r.refNow(t, "hil-482"); ref.Digest != "d1" {
		t.Fatalf("ref %+v", ref)
	}
	if res := syncHIL(t, r); len(res.Surfaced) != 0 {
		t.Fatalf("unchanged item drawn again: %+v", res)
	}
	// edited elsewhere, pressed before the next sync: the command names the old digest and is refused
	r.cli.outputs["hil list"] = listedWith("d2", "Hi, now with the price")
	r.cli.errs["hil approve"] = errors.New("HIL item \"482\": " + hilsend.ErrChanged.Error())
	res, err := r.press(t, "hil.approve", "hil-482", "btn-approve", map[string]any{"id": "482", "message": "Hi", "delay_minutes": "5"})
	if err != nil || res.Outcome != "rejected" || !strings.Contains(res.Reason, "changed") {
		t.Fatalf("%+v %v", res, err)
	}
	var approve []string
	for _, c := range r.cli.calls {
		if c[1] == "approve" {
			approve = c
		}
	}
	if got := strings.Join(approve, " "); !strings.HasSuffix(got, "--if-digest d1") {
		t.Fatalf("approve ran without the card's digest: %s", got)
	}
	s := r.surfaceNow(t, "hil-482")
	if s == nil || !hasButton(s) || !strings.Contains(fmt.Sprint(s.Data), "now with the price") || r.refNow(t, "hil-482").Digest != "d2" {
		t.Fatalf("the card was not redrawn: %+v", s)
	}
	// edited again: sync alone redraws it
	r.cli.outputs["hil list"] = listedWith("d3", "Third text")
	if res := syncHIL(t, r); fmt.Sprint(res.Surfaced) != "[hil-482]" || !strings.Contains(fmt.Sprint(r.surfaceNow(t, "hil-482").Data), "Third text") {
		t.Fatalf("%+v", res)
	}
	// and a change that no longer fits a card leaves no Approve behind
	r.cli.outputs["hil list"] = listedWith("d4", strings.Repeat("x", 30<<10))
	if res := syncHIL(t, r); len(res.Skipped) != 1 {
		t.Fatalf("%+v", res)
	}
	s = r.surfaceNow(t, "hil-482")
	if s == nil || hasButton(s) || !strings.Contains(s.Components["note"]["text"].(string), "decide it in Communications") {
		t.Fatalf("the old card is still there: %+v", s)
	}
	before, _ := r.store.UIEvents("default", r.conv.ID)
	syncHIL(t, r)
	if after, _ := r.store.UIEvents("default", r.conv.ID); len(after) != len(before) {
		t.Fatalf("the note is written again at every sync: %d then %d events", len(before), len(after))
	}
}

// A card that does not say what it showed decides nothing.
func TestACardWithoutADigestDecidesNothing(t *testing.T) {
	r := newSysRig(t)
	msgs, err := surfaces.HIL(hilItem())
	r.show(t, chatevents.UIRef{Kind: surfaces.RefHIL, ID: "482"}, msgs, err)
	r.cli.outputs["hil list"] = hilListJSON
	res, _ := r.press(t, "hil.reject", "hil-482", "btn-reject", map[string]any{"id": "482"})
	if res.Outcome != "rejected" || r.cli.called("hil", "reject") != 0 {
		t.Fatalf("%+v %v", res, r.cli.calls)
	}
}

func (r *sysRig) refNow(t *testing.T, id string) chatevents.UIRef {
	t.Helper()
	evs, err := r.store.UIEvents("default", r.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	return refOfSurface(evs, id)
}

// The journal is read by agents (`chat history events`), so it only ever holds
// the redacted form: an item with a hidden reply link gets no card to approve
// from, and an error that quotes a link is redacted too.
func TestTheChatJournalNeverHoldsAReplyToken(t *testing.T) {
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19724")
	resume, err := workflow.ExecutionResumeURL("ex-r")
	if err != nil {
		t.Fatal(err)
	}
	token := resume[strings.LastIndex(resume, "/")+1:]
	r := newSysRig(t)
	startChat(t, r)
	r.showHIL(t) // a card drawn before the item held a link
	r.cli.outputs["hil list"] = `[{"id":"482","node_name":"Send DM","digest":"d2","redacted":true,"readonly_data":{"link":"[hidden]"},"editable_data":{}}]`
	res := syncHIL(t, r)
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "decide it in Communications") || len(res.Surfaced) != 0 {
		t.Fatalf("%+v", res)
	}
	if hasButton(r.surfaceNow(t, "hil-482")) {
		t.Fatal("a card offers to approve what it does not show")
	}
	for _, c := range r.cli.calls {
		if c[1] == "list" && strings.Join(c, " ") != "hil list --redact" {
			t.Fatalf("listed without redaction: %v", c)
		}
	}

	msgs, err := surfaces.Send(hilsend.Payload{Kind: "send", ID: "7c1e", State: hilsend.StatePending, Action: hilsend.ActionDM, Text: "Hi"}, "d1")
	r.show(t, chatevents.UIRef{Kind: surfaces.RefSend, ID: "7c1e", Digest: "d1"}, msgs, err)
	r.cli.outputs["hil approve"] = fmt.Sprintf(`{"id":"7c1e","state":"failed","error":"POST %s refused"}`, resume)
	press, _ := r.press(t, "hil.approve", "send-7c1e", "btn-approve", map[string]any{"id": "7c1e", "text": "Hi"})
	if press.Outcome != "rejected" || !strings.Contains(press.Reason, "refused") || strings.Contains(press.Reason, token) {
		t.Fatalf("%+v", press)
	}
	evs, err := r.store.UIEvents("default", r.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if strings.Contains(string(ev.Payload), token) {
			t.Fatalf("the journal holds the token: %s", ev.Payload)
		}
	}
}

func sendCard(t *testing.T, r *sysRig) {
	t.Helper()
	msgs, err := surfaces.Send(hilsend.Payload{Kind: "send", ID: "7c1e", State: hilsend.StatePending, Action: hilsend.ActionDM, Channel: "instagram",
		Recipient: hilsend.Recipient{Name: "Ada"}, Text: "Hi Ada"}, "d1")
	r.show(t, chatevents.UIRef{Kind: surfaces.RefSend, ID: "7c1e", Digest: "d1"}, msgs, err)
}

const sendListed = `[{"id":"7c1e","digest":"d1","kind":"send","send":{"kind":"send","id":"7c1e","state":"pending","action":"dm","channel":"instagram","recipient":{"name":"Ada"},"text":"Hi Ada"}}]`

// While a send runs, which can take minutes, every view of the chat shows it
// in progress, with no button to press again.
func TestASendShowsInProgressWhileItRuns(t *testing.T) {
	r := newSysRig(t)
	sendCard(t, r)
	var during *a2ui.Surface
	var second UIActionResult
	r.cfg.RunCLI = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[1] == "approve" {
			if during != nil {
				t.Fatal("hil approve ran a second time")
			}
			during = r.surfaceNow(t, "send-7c1e")
			second, _ = r.press(t, "hil.approve", "send-7c1e", "btn-approve", map[string]any{"id": "7c1e", "text": "Hi Ada"})
			return []byte(`{"id":"7c1e","state":"sent"}`), nil
		}
		return r.cli.run(ctx, args...)
	}
	res, err := r.press(t, "hil.approve", "send-7c1e", "btn-approve", map[string]any{"id": "7c1e", "text": "Hi Ada"})
	if err != nil || res.Outcome != "accepted" {
		t.Fatalf("%+v %v", res, err)
	}
	if during == nil || hasButton(during) || during.Components["note"]["text"] != sendingNote {
		t.Fatalf("while sending: %+v", during)
	}
	if second.Outcome == "accepted" {
		t.Fatalf("a second press during the send: %+v", second)
	}
	if got := r.surfaceNow(t, "send-7c1e").Components["note"]["text"]; got != "Approved by you" {
		t.Fatalf("after: %v", got)
	}
}

// A send the CLI refused to start gets its card back.
func TestARefusedSendGetsItsCardBack(t *testing.T) {
	r := newSysRig(t)
	sendCard(t, r)
	r.cli.outputs["hil list"] = sendListed
	r.cli.errs["hil approve"] = errors.New("database is locked")
	res, _ := r.press(t, "hil.approve", "send-7c1e", "btn-approve", map[string]any{"id": "7c1e", "text": "Hi Ada"})
	if res.Outcome != "rejected" {
		t.Fatalf("%+v", res)
	}
	if s := r.surfaceNow(t, "send-7c1e"); !s.DeclaresAction("btn-approve", "hil.approve") {
		t.Fatalf("no card to try again from: %+v", s.Components)
	}
}

// An in-progress note whose command never wrote the end (killed, powered off)
// is settled by sync from the item's own state.
func TestSyncSettlesAnInProgressSendFromTheItem(t *testing.T) {
	r := newSysRig(t)
	startChat(t, r)
	msgs, err := surfaces.ResolvedNotice("send-7c1e", sendingNote, "info")
	r.show(t, chatevents.UIRef{Kind: surfaces.RefSend, ID: "7c1e", Digest: "d1", State: refSending}, msgs, err)
	r.cli.outputs["hil list"] = `[]`
	r.cli.outputs["hil show"] = `{"id":"7c1e","state":"approved"}`
	if res := syncHIL(t, r); len(res.Resolved) != 0 || r.surfaceNow(t, "send-7c1e").Components["note"]["text"] != sendingNote {
		t.Fatalf("still sending: %+v", res)
	}
	r.cli.outputs["hil show"] = `{"id":"7c1e","state":"failed","error":"rate limited"}`
	if res := syncHIL(t, r); fmt.Sprint(res.Resolved) != "[send-7c1e]" {
		t.Fatalf("%+v", res)
	}
	if got := r.surfaceNow(t, "send-7c1e").Components["note"]["text"]; got != "Approved by you, but sending failed: rate limited" {
		t.Fatalf("note %v", got)
	}
	if res := syncHIL(t, r); len(res.Resolved) != 0 {
		t.Fatalf("settled twice: %+v", res)
	}
}

// Between the in-progress note and the claim the item still reads pending: a
// sync in that moment must not offer the buttons again. A note long past it,
// with the item still pending, means the send never started.
func TestSyncDoesNotOfferASendAgainWhileItStarts(t *testing.T) {
	r := newSysRig(t)
	startChat(t, r)
	msgs, err := surfaces.ResolvedNotice("send-7c1e", sendingNote, "info")
	r.show(t, chatevents.UIRef{Kind: surfaces.RefSend, ID: "7c1e", Digest: "d1", State: refSending}, msgs, err)
	r.cli.outputs["hil list"] = sendListed
	if res := syncHIL(t, r); len(res.Surfaced) != 0 || hasButton(r.surfaceNow(t, "send-7c1e")) {
		t.Fatalf("drawn again while starting: %+v", res)
	}
	old := sendingGrace
	sendingGrace = 0
	defer func() { sendingGrace = old }()
	if res := syncHIL(t, r); fmt.Sprint(res.Surfaced) != "[send-7c1e]" || !hasButton(r.surfaceNow(t, "send-7c1e")) {
		t.Fatalf("a send that never started: %+v", res)
	}
}

// The running turn's question is found however many newer turns the chat
// holds (notices, cards).
func TestTheRunningTurnsQuestionIsFoundBehindNewerTurns(t *testing.T) {
	r := newSysRig(t)
	openQuestion(t, r, "w1", "q1")
	for i := 0; i < 8; i++ {
		if err := r.store.RecordSystemNotice("default", r.conv.ID, "stopped", fmt.Sprintf("notice %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// the notices are newer than the running turn
	if newest, _, err := r.store.ListTurns(r.conv.ID, "default", "", 5); err != nil || len(newest) != 5 {
		t.Fatalf("%d %v", len(newest), err)
	}
	turn, qid, err := openQuestionOf(r.store, "default", r.conv.ID, "w1")
	if err != nil || turn != "live" || qid != "q1" {
		t.Fatalf("turn %q question %q: %v", turn, qid, err)
	}
}

// A send decided elsewhere before the press: the card says what happened to it
// and never that this press approved it.
func TestAPressOnASendDecidedElsewhereShowsItsState(t *testing.T) {
	decided := errors.New("send item 7c1e: " + hilsend.ErrDecided.Error())
	for _, c := range []struct{ button, state, note string }{
		{"approve", "sent", "Already sent. This press changed nothing, edits included"},
		{"approve", "approved", "Already approved and being sent. This press changed nothing, edits included"},
		{"approve", "rejected", "Already rejected, nothing was sent. This press changed nothing, edits included"},
		{"approve", "failed", "Already approved, and sending failed: boom. This press changed nothing, edits included"},
		{"reject", "sent", "Already sent. This press changed nothing, edits included"},
	} {
		// pending at the check before the press, and gone before the command; or gone already
		for _, listed := range []string{`[]`, sendListed} {
			r := newSysRig(t)
			sendCard(t, r)
			r.cli.outputs["hil list"] = listed
			r.cli.outputs["hil show"] = fmt.Sprintf(`{"id":"7c1e","state":%q,"error":"boom"}`, c.state)
			r.cli.errs["hil "+c.button] = decided
			if listed == sendListed {
				// decided between the check and the command
				ran := false
				r.cfg.RunCLI = func(ctx context.Context, args ...string) ([]byte, error) {
					if args[1] == "list" && ran {
						return []byte(`[]`), nil
					}
					ran = ran || args[1] == c.button
					return r.cli.run(ctx, args...)
				}
			}
			ctx := map[string]any{"id": "7c1e"}
			if c.button == "approve" {
				ctx["text"] = "my edit"
			}
			res, err := r.press(t, "hil."+c.button, "send-7c1e", "btn-"+c.button, ctx)
			if err != nil || res.Outcome != "rejected" {
				t.Fatalf("%v: %+v %v", c, res, err)
			}
			s := r.surfaceNow(t, "send-7c1e")
			if s == nil || hasButton(s) || s.Components["note"]["text"] != c.note {
				t.Fatalf("%v: card %v", c, s.Components["note"])
			}
			if listed == `[]` && r.cli.called("hil", c.button) != 0 && c.button == "approve" {
				t.Fatalf("%v: approve ran for an item that does not wait", c)
			}
			evs, _ := r.store.UIEvents("default", r.conv.ID)
			for _, ev := range evs {
				if listed == `[]` && strings.Contains(string(ev.Payload), "Sending now") {
					t.Fatalf("%v: the card claimed to be sending", c)
				}
			}
		}
	}
	// Someone else's send in progress is settled without calling it yours.
	r := newSysRig(t)
	startChat(t, r)
	sendCard(t, r)
	r.cli.outputs["hil list"] = `[]`
	r.cli.outputs["hil show"] = `{"id":"7c1e","state":"approved"}`
	r.press(t, "hil.approve", "send-7c1e", "btn-approve", map[string]any{"id": "7c1e", "text": "x"})
	r.cli.outputs["hil show"] = `{"id":"7c1e","state":"failed","error":"boom"}`
	syncHIL(t, r)
	if got := r.surfaceNow(t, "send-7c1e").Components["note"]["text"]; got != "Already approved, and sending failed: boom. This press changed nothing, edits included" {
		t.Fatalf("%v", got)
	}
}

// The digest a press decides with is the pressed card's own, carried by its
// button: a view that still shows an older card cannot decide the item with
// the text it shows, although a newer card is journaled.
func TestAPressOnAnOlderCardDecidesNothing(t *testing.T) {
	r := newSysRig(t)
	startChat(t, r)
	r.cli.outputs["hil list"] = listedWith("d1", "old text")
	syncHIL(t, r)
	r.cli.outputs["hil list"] = listedWith("d2", "newer text from another window")
	syncHIL(t, r)
	r.cli.outputs["hil approve"] = `{"id":"482","status":"approved"}`
	r.cli.outputs["hil reject"] = `{"id":"482","status":"rejected"}`
	for name, ctx := range map[string]map[string]any{
		"the old card":  {"id": "482", "message": "old text", "delay_minutes": "5", surfaces.DigestKey: "d1"},
		"no digest":     {"id": "482", "message": "old text", "delay_minutes": "5", surfaces.DigestKey: ""},
		"a made-up one": {"id": "482", "message": "old text", "delay_minutes": "5", surfaces.DigestKey: "zz"},
	} {
		for _, verb := range []string{"approve", "reject"} {
			c := ctx
			if verb == "reject" {
				c = map[string]any{"id": "482", surfaces.DigestKey: ctx[surfaces.DigestKey]}
			}
			res, err := r.press(t, "hil."+verb, "hil-482", "btn-"+verb, c)
			if err != nil || res.Outcome != "rejected" {
				t.Fatalf("%s, %s: %+v %v", name, verb, res, err)
			}
		}
	}
	if r.cli.called("hil", "approve")+r.cli.called("hil", "reject") != 0 {
		t.Fatalf("an old card decided: %v", r.cli.calls)
	}
	if s := r.surfaceNow(t, "hil-482"); !hasButton(s) || !strings.Contains(fmt.Sprint(s.Data), "newer text") {
		t.Fatalf("the current card was touched: %+v", s)
	}
	// the current card carries the current digest and decides with it
	res, _ := r.press(t, "hil.approve", "hil-482", "btn-approve", map[string]any{"id": "482", "message": "newer text from another window", "delay_minutes": "5"})
	if res.Outcome != "accepted" {
		t.Fatalf("%+v", res)
	}
	if last := strings.Join(r.cli.calls[len(r.cli.calls)-1], " "); !strings.HasSuffix(last, "--if-digest d2") {
		t.Fatal(last)
	}
}

// A card journaled with Approve does not approve a send that carries
// attachments: the press is refused and the card drawn again without Approve.
func TestAnApproveCardDoesNotSendAttachments(t *testing.T) {
	r := newSysRig(t)
	sendCard(t, r)
	r.cli.outputs["hil list"] = strings.Replace(sendListed, `"text":"Hi Ada"`, `"text":"Hi Ada","attachments":[{"kind":"file","name":"offer.pdf"}]`, 1)
	res, err := r.press(t, "hil.approve", "send-7c1e", "btn-approve", map[string]any{"id": "7c1e", "text": "Hi Ada"})
	if err != nil || res.Outcome != "rejected" || !strings.Contains(res.Reason, "Communications") || r.cli.called("hil", "approve") != 0 {
		t.Fatalf("%+v %v %v", res, err, r.cli.calls)
	}
	s := r.surfaceNow(t, "send-7c1e")
	if s == nil || s.DeclaresAction("btn-approve", "hil.approve") || !s.DeclaresAction("btn-reject", "hil.reject") {
		t.Fatalf("card %+v", s)
	}
}

// An item that can only be approved on the approvals page gets no card, an
// earlier card of it is closed, and a press of that card runs no approval.
func TestAnItemDecidedOnTheApprovalsPageHasNoApproveInChat(t *testing.T) {
	listed := strings.Replace(hilListJSON, `"digest":"d1",`, `"digest":"d1","no_chat_approve":true,`, 1)
	r := newSysRig(t)
	startChat(t, r)
	r.cli.outputs["hil list"] = listed
	res := syncHIL(t, r)
	if len(res.Surfaced) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "Communications") || r.surfaceNow(t, "hil-482") != nil {
		t.Fatalf("%+v", res)
	}
	r.showHIL(t) // journaled before
	press, err := r.press(t, "hil.approve", "hil-482", "btn-approve", map[string]any{"id": "482", "message": "Hi", "delay_minutes": "5"})
	if err != nil || press.Outcome != "rejected" || !strings.Contains(press.Reason, "Communications") || r.cli.called("hil", "approve") != 0 {
		t.Fatalf("%+v %v", press, err)
	}
	s := r.surfaceNow(t, "hil-482")
	if s == nil || hasButton(s) || s.Components["note"]["text"] != approvalsPageNote {
		t.Fatalf("card %+v", s)
	}
	r.showHIL(t)
	syncHIL(t, r)
	if s := r.surfaceNow(t, "hil-482"); hasButton(s) || s.Components["note"]["text"] != approvalsPageNote {
		t.Fatalf("sync left the old card: %+v", s)
	}
}
