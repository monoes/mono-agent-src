package dynorg

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// System surfaces of the conductor (Config.UI set): the Ask card of a worker's
// question and the mission plan, progress and report cards. They are built by
// internal/a2ui/surfaces from the same records the older events carry, and are
// journaled as ui.surface events with system provenance next to those events,
// which stay as they were. A card that cannot be built is skipped: the old
// event is the fallback.

// progressEvery is the least time between two progress updates of a mission.
var progressEvery = time.Second

// emitSystemUI applies built envelopes to the live registry as system
// messages and journals each one. A createSurface for a surface that is live
// replaces it (the old one is deleted first).
func (c *Conductor) emitSystemUI(ref chatevents.UIRef, msgs [][]byte) {
	if c.cfg.UI == nil || c.cfg.Emit == nil {
		return
	}
	st := c.uiStateFor()
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := c.uiSeed(st); err != nil {
		return
	}
	for _, raw := range msgs {
		m, err := a2ui.ParseMessage(raw, a2ui.DefaultLimits)
		if err != nil {
			return
		}
		if m.Kind == a2ui.KindCreateSurface && st.reg.Surface(m.SurfaceID) != nil {
			if del, derr := surfaces.Resolved(m.SurfaceID); derr == nil {
				c.applySystem(st, ref, m.SurfaceID, del[0])
			}
		}
		if !c.applySystem(st, ref, m.SurfaceID, raw) {
			return
		}
	}
}

func (c *Conductor) applySystem(st *uiState, ref chatevents.UIRef, surfaceID string, raw []byte) bool {
	if _, err := st.reg.Handle(raw, a2ui.FromSystem); err != nil {
		return false
	}
	r := ref
	c.cfg.Emit.Emit(chatevents.EventUISurface, chatevents.UISurfacePayload{
		SurfaceID: surfaceID, Provenance: string(a2ui.FromSystem), Ref: &r, Message: append(json.RawMessage(nil), raw...),
	})
	return true
}

// askRef is the ref and question id an Ask card of worker agentID's question
// qid carries: "<agent>.<qid>" (worker ids are tokens; a worker whose id is
// not one gets no card).
func askRef(agentID, qid string) (chatevents.UIRef, string) {
	id := agentID + "." + qid
	return chatevents.UIRef{Kind: surfaces.RefAsk, ID: id}, id
}

// askSurface shows a worker's questions as one Ask card.
func (c *Conductor) askSurface(agentID, qid string, questions ...string) {
	if c.cfg.UI == nil {
		return
	}
	ref, id := askRef(agentID, qid)
	qs := make([]surfaces.Question, len(questions))
	for i, q := range questions {
		bounded, _, _ := chatevents.BoundText(q, 2000)
		qs[i] = surfaces.Question{Text: bounded}
	}
	msgs, err := surfaces.Ask(id, qs)
	if err != nil {
		return
	}
	c.emitSystemUI(ref, msgs)
}

// closeAskSurface replaces the card of a question that ended with a notice.
func (c *Conductor) closeAskSurface(agentID, qid, text string) {
	if c.cfg.UI == nil {
		return
	}
	ref, id := askRef(agentID, qid)
	msgs, err := surfaces.ResolvedNotice(surfaces.AskSurfaceID(id), text, "neutral")
	if err != nil {
		return
	}
	c.emitSystemUI(ref, msgs)
}

func missionRef(kind string, sid int64) chatevents.UIRef {
	return chatevents.UIRef{Kind: kind, ID: fmt.Sprint(sid)}
}

// missionPlanSurface shows the plan card next to the mission.plan event.
func (c *Conductor) missionPlanSurface(p chatevents.MissionPlanPayload, budgetUSD float64) {
	if c.cfg.UI == nil {
		return
	}
	if msgs, err := surfaces.MissionPlan(p, budgetUSD); err == nil {
		c.emitSystemUI(missionRef(surfaces.RefMissionPlan, p.SessionID), msgs)
	}
}

// missionReportSurface shows the report card next to the mission.report event.
func (c *Conductor) missionReportSurface(p chatevents.MissionReportPayload) {
	if c.cfg.UI == nil {
		return
	}
	if msgs, err := surfaces.MissionReport(p); err == nil {
		c.emitSystemUI(missionRef(surfaces.RefMissionReport, p.SessionID), msgs)
	}
}

// missionProgressSurface moves the mission card's bars when the session's
// task states changed, at most once per progressEvery per mission.
func (c *Conductor) missionProgressSurface(ctx context.Context, mc *MissionRun, se tasks.Session) {
	if c.cfg.UI == nil || c.cfg.Emit == nil {
		return
	}
	now := time.Now()
	c.uiProgress.mu.Lock()
	if c.uiProgress.marks == nil {
		c.uiProgress.marks = map[int64]progressMark{}
	}
	mark := c.uiProgress.marks[se.ID]
	c.uiProgress.mu.Unlock()
	if now.Sub(mark.at) < progressEvery {
		return
	}
	st := c.uiStateFor()
	st.mu.Lock()
	err := c.uiSeed(st)
	var card *a2ui.Surface
	if err == nil {
		card = st.reg.Surface(surfaces.MissionSurfaceID(se.ID))
	}
	st.mu.Unlock()
	if card == nil {
		return
	}
	// Approved (in the app): the plan card becomes the running mission's.
	if se.State == tasks.SessionRunning && card.Components["btn-edit"] != nil {
		c.missionRunningSurface(se, card)
	}
	plan, err := mc.Store.PlanOfSession(ctx, mc.ProfileID, se.ID, missionActor())
	if err != nil {
		return
	}
	states := make(map[int64]string, len(plan.Tasks))
	done := 0
	for _, t := range plan.Tasks {
		states[t.ID] = string(t.Status)
		if t.Status == tasks.StatusDone {
			done++
		}
	}
	sig := fmt.Sprintf("%d/%d/%.4f/%.4f/%s", done, len(plan.Tasks), se.SpentUSD, se.BudgetUSD, stateSignature(states))
	c.uiProgress.mu.Lock()
	changed := sig != mark.sig
	if changed {
		c.uiProgress.marks[se.ID] = progressMark{at: now, sig: sig}
	}
	c.uiProgress.mu.Unlock()
	if !changed {
		return
	}
	if msgs, err := surfaces.MissionProgressUpdate(se.ID, done, len(plan.Tasks), se.SpentUSD, se.BudgetUSD, estimateOf(card), states); err == nil {
		c.emitSystemUI(missionRef(surfaces.RefMissionPlan, se.ID), msgs)
	}
}

// missionRunningSurface replaces a plan card with the running mission's card,
// built from what the plan card held.
func (c *Conductor) missionRunningSurface(se tasks.Session, card *a2ui.Surface) {
	text := func(id string) string { s, _ := card.Components[id]["text"].(string); return s }
	p := chatevents.MissionPlanPayload{SessionID: se.ID, Title: text("title"), Goal: text("goal"),
		TaskCount: int(missionNumber(card, "total")), EstimatedBudgetUSD: estimateOf(card)}
	if msgs, err := surfaces.MissionRunning(p, se.BudgetUSD); err == nil {
		c.emitSystemUI(missionRef(surfaces.RefMissionPlan, se.ID), msgs)
	}
}

// missionNumber reads /mission/<key> of a mission card's data; 0 when absent.
func missionNumber(card *a2ui.Surface, key string) float64 {
	data, _ := card.Data.(map[string]any)
	mission, _ := data["mission"].(map[string]any)
	n, _ := strconv.ParseFloat(fmt.Sprint(mission[key]), 64)
	return n
}

func estimateOf(card *a2ui.Surface) float64 { return missionNumber(card, "estimated") }

// progressTracker remembers, per mission, what the last progress update said.
type progressTracker struct {
	mu    sync.Mutex
	marks map[int64]progressMark
}

type progressMark struct {
	at  time.Time
	sig string
}

func stateSignature(states map[int64]string) string {
	ids := make([]int64, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "%d=%s,", id, states[id])
	}
	return b.String()
}
