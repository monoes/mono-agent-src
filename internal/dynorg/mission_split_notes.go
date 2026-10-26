package dynorg

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// splitPartNotes is the notes a part of a split carries: the text of the task split (or the
// part's own text) and a meta line whose files and size are the part's own when it names
// them, else the task's. A part that names neither keeps the task's notes as they are.
func splitPartNotes(parent tasks.Task, p SplitPart) string {
	if len(p.Files) == 0 && p.Size == "" && strings.TrimSpace(p.Notes) == "" {
		return ""
	}
	meta, _ := ParseTaskMeta(parent.Notes)
	body := parent.Notes
	if _, ok := ParseTaskMeta(parent.Notes); ok {
		body = parent.Notes[:strings.LastIndex(parent.Notes, MetaPrefix)]
	}
	if strings.TrimSpace(p.Notes) != "" {
		body = p.Notes
	}
	if len(p.Files) > 0 {
		meta.Files = p.Files
	}
	if p.Size != "" {
		meta.Size = p.Size
	}
	return notesWithMeta(body, meta)
}

// validateSplitMeta checks the files and size a part names for itself.
func validateSplitMeta(s SplitSpec) []string {
	var why []string
	for i, p := range s.Parts {
		switch p.Size {
		case "", SizeXS, SizeS, SizeM:
		case SizeL:
			why = append(why, fmt.Sprintf("part %d: size l is too big: split it further", i+1))
		default:
			why = append(why, fmt.Sprintf("part %d: size %q is not xs, s or m", i+1, p.Size))
		}
		for _, f := range p.Files {
			if r := cleanFile(f); r != "" {
				why = append(why, fmt.Sprintf("part %d: %s", i+1, r))
			}
		}
	}
	return why
}

// splitParts turns the lead's parts into the store's, each with its own notes.
func splitParts(ctx context.Context, m *MissionConfig, session int64, s SplitSpec) ([]tasks.SplitPart, error) {
	d, err := m.Store.SessionTask(ctx, m.ProfileID, session, s.TaskID, m.actor(session))
	if err != nil {
		return nil, err
	}
	parent := d.Task
	parts := make([]tasks.SplitPart, len(s.Parts))
	for i, p := range s.Parts {
		parts[i] = tasks.SplitPart{Title: p.Title, Acceptance: p.Acceptance, Notes: splitPartNotes(parent, p)}
	}
	return parts, nil
}
