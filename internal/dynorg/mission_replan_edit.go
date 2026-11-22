package dynorg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// TaskEdit is a change the lead asks for in the words of a task that has not begun: a field left
// out stays as it is. Notes replace the task's own text; its files, size and risk stay.
type TaskEdit struct {
	TaskID     int64   `json:"task_id"`
	Title      *string `json:"title,omitempty"`
	Notes      *string `json:"notes,omitempty"`
	Acceptance *string `json:"acceptance,omitempty"`
}

// UnmarshalJSON accepts acceptance as a string or a list of strings, as a plan task does.
func (e *TaskEdit) UnmarshalJSON(b []byte) error {
	var in struct {
		TaskID     int64           `json:"task_id"`
		Title      *string         `json:"title"`
		Notes      *string         `json:"notes"`
		Acceptance json.RawMessage `json:"acceptance"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	*e = TaskEdit{TaskID: in.TaskID, Title: in.Title, Notes: in.Notes}
	if len(in.Acceptance) > 0 && string(in.Acceptance) != "null" {
		a, err := acceptanceText(in.Acceptance)
		if err != nil {
			return err
		}
		e.Acceptance = &a
	}
	return nil
}

// validateEdits refuses an edit that names no task of the plan, changes nothing, or would leave a
// task with no title or no checkable acceptance. Nothing is written when any is refused.
func validateEdits(edits []TaskEdit, plan tasks.SessionPlan) []Rejection {
	have := map[int64]bool{}
	for _, t := range plan.Tasks {
		have[t.ID] = true
	}
	var rej []Rejection
	for _, e := range edits {
		var why []string
		switch {
		case !have[e.TaskID]:
			why = append(why, "no such task in this session")
		case e.Title == nil && e.Notes == nil && e.Acceptance == nil:
			why = append(why, "give a title, notes or acceptance to change")
		}
		if e.Title != nil && strings.TrimSpace(*e.Title) == "" {
			why = append(why, "the title cannot be empty")
		}
		if e.Acceptance != nil && !checkable(*e.Acceptance) {
			why = append(why, "acceptance is missing or not checkable")
		}
		if len(why) > 0 {
			rej = append(rej, Rejection{Ref: fmt.Sprintf("#%d", e.TaskID), Reasons: why})
		}
	}
	return rej
}

// editTasks applies the edits; the notes keep the task's meta line (files, size, risk).
func (m *MissionConfig) editTasks(ctx context.Context, se tasks.Session, edits []TaskEdit) error {
	plan, err := m.Store.PlanOfSession(ctx, m.ProfileID, se.ID, m.actor(se.ID))
	if err != nil {
		return err
	}
	if rej := validateEdits(edits, plan); len(rej) > 0 {
		return &RejectedError{rej}
	}
	byID := map[int64]tasks.Task{}
	for _, t := range plan.Tasks {
		byID[t.ID] = t
	}
	for _, e := range edits {
		in := tasks.SessionTaskEdit{Title: e.Title, Acceptance: e.Acceptance}
		if e.Notes != nil {
			notes := *e.Notes
			if meta, ok := ParseTaskMeta(byID[e.TaskID].Notes); ok {
				notes = notesWithMeta(notes, meta)
			}
			in.Notes = &notes
		}
		if _, err := m.Store.EditSessionTask(ctx, m.ProfileID, se.ID, e.TaskID, in, m.actor(se.ID)); err != nil {
			return fmt.Errorf("edit of #%d: %w (the earlier edits of this call were written)", e.TaskID, err)
		}
	}
	return nil
}

func editsSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Changes to tasks that have not begun (the reviewer's corrections to their words).", "items": obj(map[string]any{
		"task_id":    map[string]any{"type": "integer", "description": "The task to change."},
		"title":      str("Optional new title."),
		"notes":      str("Optional new notes; the task's files, size and risk stay."),
		"acceptance": str("Optional new acceptance: how anyone checks it is done."),
	}, "task_id")}
}
