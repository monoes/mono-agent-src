package dynorg

import (
	"context"

	"github.com/monoes/mono-agent/internal/tasks"
)

// OrgWritersHint is the one line that tells the operator how workers get to run in parallel.
const OrgWritersHint = "coder set --org-writers isolated (operator)"

// ParallelView is how much of a plan could run at once and how writers are set.
type ParallelView struct {
	// Parallelism is the most tasks of one dependency level that touch no file in common (a
	// task that declares no files counts as touching everything), epics left out.
	Parallelism int    `json:"parallelism"`
	Writers     string `json:"writers"` // "shared" (one write lease) or "isolated" (a worktree each; shared when isolation could not start)
	Hint        string `json:"parallel_hint,omitempty"`
}

// planParallelism computes ParallelView.Parallelism from a plan.
func planParallelism(all []tasks.Task) int {
	byID := map[int64]tasks.Task{}
	for _, t := range all {
		byID[t.ID] = t
	}
	level := map[int64]int{}
	var depth func(id int64, seen map[int64]bool) int
	depth = func(id int64, seen map[int64]bool) int {
		if l, ok := level[id]; ok {
			return l
		}
		if seen[id] {
			return 0
		}
		seen[id] = true
		l := 0
		for _, d := range byID[id].DependsOn {
			if _, ok := byID[d]; ok {
				l = max(l, depth(d, seen)+1)
			}
		}
		level[id] = l
		return l
	}
	byLevel := map[int][][]string{}
	for _, t := range all {
		if t.Kind == tasks.KindEpic {
			continue
		}
		l := depth(t.ID, map[int64]bool{})
		byLevel[l] = append(byLevel[l], TaskFiles(t.Notes))
	}
	best := 0
	for _, sets := range byLevel {
		var picked [][]string
		for _, files := range sets {
			clash := false
			for _, p := range picked {
				if filesOverlap(files, p) {
					clash = true
					break
				}
			}
			if !clash {
				picked = append(picked, files)
			}
		}
		best = max(best, len(picked))
	}
	return best
}

// parallelView reports the plan's parallelism and the writers setting; when writers are
// shared and more than one task could run at once it carries the hint.
func (c *Conductor) parallelView(all []tasks.Task) ParallelView {
	v := ParallelView{Parallelism: planParallelism(all), Writers: "shared"}
	if c.iso != nil {
		v.Writers = "isolated"
	}
	if c.cfg.Writers != WritersIsolated && v.Parallelism > 1 {
		v.Hint = OrgWritersHint
	}
	return v
}

// addParallel puts the parallelism, writers and hint into a mission tool's result.
func (c *Conductor) addParallel(ctx context.Context, m *MissionConfig, id int64, out map[string]any) {
	plan, err := m.Store.PlanOfSession(ctx, m.ProfileID, id, m.actor(id))
	if err != nil {
		return
	}
	v := c.parallelView(plan.Tasks)
	out["parallelism"], out["writers"] = v.Parallelism, v.Writers
	if v.Hint != "" {
		out["parallel_hint"] = v.Hint
	}
}
