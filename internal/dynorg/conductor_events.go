package dynorg

import (
	"encoding/json"
	"sort"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// workerEvent journals a worker's own events: its tool calls (with its
// AgentID, and call ids made unique across the org), its session, and the
// switch to working.
func (c *Conductor) workerEvent(w *worker, ev monomind.Event) {
	switch ev.Type {
	case monomind.EventStart:
		c.setStatus(w, chatevents.AgentWorking, "")
	case monomind.EventSession:
		if ev.SessionID != "" {
			c.mu.Lock()
			w.session = ev.SessionID
			c.mu.Unlock()
		}
	case monomind.EventToolActivity:
		callID := w.id + ":" + ev.ID
		switch ev.Phase {
		case "start":
			args, _ := chatevents.RedactAndBoundFields(ev.Input)
			parent := ""
			if ev.ParentToolUseID != "" {
				parent = w.id + ":" + ev.ParentToolUseID
			}
			c.noteChangedFile(w, ev)
			c.cfg.Emit.Emit(chatevents.EventToolStarted, chatevents.ToolStartedPayload{
				AgentID: w.id, CallID: callID, Name: ev.Name, Arguments: args, Native: true, ParentCallID: parent, Kind: ev.Kind,
			})
		case "end":
			out, cut, _ := chatevents.BoundText(ev.Output, chatevents.MaxToolPreviewBytes)
			ok := ev.OK
			if ok == nil {
				v := !ev.Denied && !ev.Cancelled
				ok = &v
			}
			c.cfg.Emit.Emit(chatevents.EventToolCompleted, chatevents.ToolCompletedPayload{
				AgentID: w.id, CallID: callID, OK: ok, Result: out, Truncated: cut, DurationMs: ev.DurationMs,
				Denied: ev.Denied, Cancelled: ev.Cancelled,
			})
		}
	}
}

// noteChangedFile remembers the file a writing tool call targets.
func (c *Conductor) noteChangedFile(w *worker, ev monomind.Event) {
	if p := editTarget(ev); p != "" {
		c.mu.Lock()
		w.changed[p] = true
		c.mu.Unlock()
	}
}

// isEditCall reports whether a tool call edits files.
func isEditCall(ev monomind.Event) bool {
	kind := ev.Kind
	if kind == "" {
		switch ev.Name {
		case "Edit", "MultiEdit", "NotebookEdit":
			kind = "edit"
		case "Write":
			kind = "write"
		}
	}
	return kind == "edit" || kind == "write" || kind == "patch"
}

// editTarget is the file an editing tool call targets, or "".
func editTarget(ev monomind.Event) string {
	if !isEditCall(ev) {
		return ""
	}
	var input map[string]any
	if json.Unmarshal(ev.Input, &input) != nil {
		return ""
	}
	for _, k := range []string{"file_path", "path", "notebook_path"} {
		if p, ok := input[k].(string); ok && p != "" {
			return p
		}
	}
	return ""
}

func (c *Conductor) setStatus(w *worker, to, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setStatusLocked(w, to, detail)
}

func (c *Conductor) setStatusLocked(w *worker, to, detail string) {
	if w.status == to {
		return
	}
	from := w.status
	w.status = to
	c.cfg.Emit.Emit(chatevents.EventAgentStatus, chatevents.AgentStatusPayload{AgentID: w.id, From: from, To: to, Detail: detail, Leases: leaseNames(w.leases), Branch: w.branch, Confinement: w.confinement})
}

func (c *Conductor) finish(w *worker, outcome, report, errText string) {
	c.missionFinish(w, outcome, report, errText) // while the worker still counts as running
	c.mu.Lock()
	w.report, w.errText = boundText(report, maxReport), errText
	payload := chatevents.AgentFinishedPayload{
		AgentID: w.id, Outcome: outcome, Summary: boundText(report, 600),
		DurationMs: c.cfg.Now().Sub(w.started).Milliseconds(), FilesChanged: sortedKeys(w.changed),
	}
	if w.hasTok {
		in, out := w.inTok, w.outTok
		payload.InputTokens, payload.OutputTokens = &in, &out
	}
	if w.hasCost {
		cost := w.cost
		payload.CostUSD, payload.CostEstimated = &cost, w.costEstimated
	}
	c.setStatusLocked(w, outcome, "")
	c.mu.Unlock()
	c.remember(w)
	c.recordResult(w, outcome, errText)
	if report != "" {
		c.emitMessage(w.id, "result", w.id, "lead", report)
	}
	c.cfg.Emit.Emit(chatevents.EventAgentFinished, payload)
}

func (c *Conductor) emitMessage(agentID, direction, from, to, text string) {
	bounded, cut, _ := chatevents.BoundText(text, maxReport)
	c.cfg.Emit.Emit(chatevents.EventAgentMessage, chatevents.AgentMessagePayload{
		AgentID: agentID, Direction: direction, From: from, To: to, Text: bounded, Truncated: cut,
	})
}

func boundText(s string, max int) string {
	out, _, _ := chatevents.BoundText(s, max)
	return out
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
