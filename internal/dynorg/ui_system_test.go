package dynorg

import (
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

func systemSurfaces(em *recEmitter) []chatevents.UISurfacePayload {
	var out []chatevents.UISurfacePayload
	for _, p := range uiEvents(em) {
		if p.Provenance == "system" {
			out = append(out, p)
		}
	}
	return out
}

func kinds(ps []chatevents.UISurfacePayload) []string {
	var out []string
	for _, p := range ps {
		m, _ := a2ui.ParseMessage(p.Message, a2ui.DefaultLimits)
		out = append(out, string(m.Kind)+":"+m.SurfaceID)
	}
	return out
}

func TestAskCardIsShownAndClosedWithTheQuestion(t *testing.T) {
	c, em := uiConductor(t, nil)
	c.askSurface("w1", "q1", "Which region?")
	ps := systemSurfaces(em)
	if len(ps) != 1 || ps[0].SurfaceID != "ask-w1.q1" || ps[0].Ref == nil || ps[0].Ref.Kind != surfaces.RefAsk || ps[0].Ref.ID != "w1.q1" {
		t.Fatalf("%+v", ps)
	}
	if s := c.ui.reg.Surface("ask-w1.q1"); s == nil || !s.DeclaresAction("btn-continue", "system.answer") {
		t.Fatal("the card is not live in the registry")
	}
	c.emitAnswer("w1", "q1", "user", "eu")
	got := strings.Join(kinds(systemSurfaces(em)), " ")
	if got != "createSurface:ask-w1.q1 deleteSurface:ask-w1.q1 createSurface:ask-w1.q1" {
		t.Fatalf("journal %s", got)
	}
	if hasButtonIn(c.ui.reg.Surface("ask-w1.q1")) {
		t.Fatal("the closed card kept its button")
	}
	// closing a question that never had a card journals nothing
	n := len(systemSurfaces(em))
	c.emitAnswer("w2", "q1", "system", "stopped")
	if len(systemSurfaces(em)) != n {
		t.Fatal("a card appeared for a question that had none")
	}
}

func hasButtonIn(s *a2ui.Surface) bool {
	for _, c := range s.Components {
		if c["component"] == "Button" {
			return true
		}
	}
	return false
}

func TestSystemCardsNeedConfigUIAndAValidToken(t *testing.T) {
	c, em := newAskConductorWithoutUI(t)
	c.askSurface("w1", "q1", "x")
	c.missionPlanSurface(chatevents.MissionPlanPayload{SessionID: 3, Title: "t"}, 1)
	if len(uiEvents(em)) != 0 {
		t.Fatal("system cards without Config.UI")
	}
	c2, em2 := uiConductor(t, nil)
	c2.askSurface("worker one", "q1", "x") // an id that is not a token: no card, the old event stays the only one
	if len(uiEvents(em2)) != 0 {
		t.Fatal("a card for an agent id that is not a token")
	}
}

func newAskConductorWithoutUI(t *testing.T) (*Conductor, *recEmitter) {
	c, em := uiConductor(t, nil)
	c.cfg.UI = nil
	return c, em
}

func TestMissionCardsReplaceThemselvesWhenThePlanChanges(t *testing.T) {
	c, em := uiConductor(t, nil)
	p := chatevents.MissionPlanPayload{SessionID: 17, Title: "Ship", Goal: "g", TaskCount: 3, EstimatedBudgetUSD: 2}
	c.missionPlanSurface(p, 10)
	p.TaskCount, p.Round = 4, 1
	c.missionPlanSurface(p, 10)
	if got := strings.Join(kinds(systemSurfaces(em)), " "); got != "createSurface:mission-17 deleteSurface:mission-17 createSurface:mission-17" {
		t.Fatalf("%s", got)
	}
	c.missionReportSurface(chatevents.MissionReportPayload{SessionID: 17, Title: "Ship", State: "done", CloseProposed: true})
	ps := systemSurfaces(em)
	if last := ps[len(ps)-1]; last.SurfaceID != "mission-report-17" || last.Ref.Kind != surfaces.RefMissionReport {
		t.Fatalf("%+v", last)
	}
}

func TestMissionProgressMovesTheCardOnlyWhenTaskStatesChange(t *testing.T) {
	r := newRig(t, rigOpts{budget: 5})
	a := r.add(spec{title: "a"})
	r.add(spec{title: "b"})
	c := r.c
	c.cfg.UI = &UIConfig{}
	old := progressEvery
	progressEvery = 0
	t.Cleanup(func() { progressEvery = old })

	se := r.session()
	c.missionProgressSurface(tbg, r.mc, se) // no card yet: nothing to move
	if len(r.em.find(chatevents.EventUISurface)) != 0 {
		t.Fatal("progress without a card")
	}
	c.missionPlanSurface(chatevents.MissionPlanPayload{SessionID: r.sid, Title: "m", TaskCount: 2, EstimatedBudgetUSD: 2.5}, 5)
	n := len(systemSurfaces(r.em))
	c.missionProgressSurface(tbg, r.mc, se)
	c.missionProgressSurface(tbg, r.mc, se) // same states: no second update
	ps := systemSurfaces(r.em)
	if len(ps) != n+1 || !strings.Contains(string(ps[n].Message), `"total":2`) || !strings.Contains(string(ps[n].Message), `"done":0`) {
		t.Fatalf("%d events, last %s", len(ps), ps[len(ps)-1].Message)
	}
	if !strings.Contains(string(ps[n].Message), `"estimated":2.5`) {
		t.Fatalf("the update dropped the estimate: %s", ps[n].Message)
	}
	// Approved in the app: the plan card becomes the running card, then moves.
	r.approve()
	r.done(a.ID)
	c.missionProgressSurface(tbg, r.mc, r.session())
	ps = systemSurfaces(r.em)
	if got := strings.Join(kinds(ps[n+1:]), " "); got != fmt.Sprintf("deleteSurface:mission-%[1]d createSurface:mission-%[1]d updateDataModel:mission-%[1]d", r.sid) {
		t.Fatalf("after the approval: %s", got)
	}
	card := c.ui.reg.Surface(surfaces.MissionSurfaceID(r.sid))
	if card.Components["btn-edit"] != nil || card.Components["round"]["text"] != "Running" || card.Components["title"]["text"] != "m" || estimateOf(card) != 2.5 {
		t.Fatalf("running card %+v %v", card.Components, card.Data)
	}
	if !strings.Contains(string(ps[len(ps)-1].Message), `"done":1`) {
		t.Fatalf("%s", ps[len(ps)-1].Message)
	}
	n = len(ps) - 2
	// the debounce: inside the interval nothing more goes out, whatever changed
	progressEvery = 3600e9
	r.done(r.add(spec{title: "c"}).ID)
	c.missionProgressSurface(tbg, r.mc, r.session())
	if len(systemSurfaces(r.em)) != n+2 {
		t.Fatal("the debounce let a second update through")
	}
}
