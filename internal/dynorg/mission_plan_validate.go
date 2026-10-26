package dynorg

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Plan validation (plan 2026-10-25, section 7 step 2): a pure check of the
// tasks the lead wants to write, run before anything reaches the store. A plan
// with any failing task is rejected as a whole, with one reason list per task.

// MaxPlanTasks is the most tasks a draft plan holds (the store's max_tasks, the
// operator's to raise, bounds a running session instead).
const MaxPlanTasks = 200

// Task sizes: xs and s are minutes, m is up to about an hour, l is too big.
const (
	SizeXS = "xs"
	SizeS  = "s"
	SizeM  = "m"
	SizeL  = "l"
)

// sizeBudgetUSD is the rough cost a task of each size adds to the plan card's
// estimate: a hint for the operator, never a limit.
var sizeBudgetUSD = map[string]float64{SizeXS: 0.05, SizeS: 0.15, SizeM: 0.5}

// Task kinds, as the tasks package has them.
const (
	kindTask  = "task"
	kindEpic  = "epic"
	kindCheck = "check"
)

// PlanTask is one task of a plan the lead submits. Ref names it inside the
// batch; Parent and DependsOn name other tasks of the batch by ref, or tasks
// already in the session as "#<id>".
type PlanTask struct {
	Ref        string   `json:"ref"`
	Title      string   `json:"title"`
	Notes      string   `json:"notes,omitempty"`
	Acceptance string   `json:"acceptance"`
	Kind       string   `json:"kind,omitempty"`
	Parent     string   `json:"parent,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
	Files      []string `json:"files,omitempty"`
	Size       string   `json:"size,omitempty"`
	Risk       string   `json:"risk,omitempty"`
	Executor   string   `json:"executor,omitempty"`
}

// Rejection is why one task (or, with an empty Ref, the plan as a whole) was
// refused.
type Rejection struct {
	Ref     string   `json:"ref,omitempty"`
	Title   string   `json:"title,omitempty"`
	Reasons []string `json:"reasons"`
}

var refPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`)

// existingRef reads "#12" as task 12 of the session.
func existingRef(s string) (int64, bool) {
	if !strings.HasPrefix(s, "#") {
		return 0, false
	}
	n, err := strconv.ParseInt(s[1:], 10, 64)
	return n, err == nil && n > 0
}

// checkable says whether acceptance names something that can be checked: a
// command or name in backticks, a rubric, or one of the words of a check (a
// test, an exit code, a file that exists, an output). It cannot judge the
// criterion; it refuses the ones that name nothing ("it works").
func checkable(acc string) bool {
	a := strings.ToLower(strings.TrimSpace(acc))
	if len(a) < 15 {
		return false
	}
	if strings.Count(a, "`") >= 2 || strings.HasPrefix(a, "rubric:") {
		return true
	}
	for _, w := range []string{"test", "exit code", "exits", "exists", "output", "returns", "contains", "prints", "build", "lint", "vet", "compiles", "reviewer", "response", "status code"} {
		if strings.Contains(a, w) {
			return true
		}
	}
	return false
}

// cleanFile refuses a path that leaves the workspace.
func cleanFile(f string) string {
	f = strings.TrimSpace(f)
	switch {
	case f == "":
		return "has an empty file name"
	case path.IsAbs(f) || strings.HasPrefix(f, "~") || strings.Contains(f, `\`):
		return fmt.Sprintf("file %q must be a relative, slash-separated path", f)
	}
	for _, part := range strings.Split(f, "/") {
		if part == ".." {
			return fmt.Sprintf("file %q leaves the workspace", f)
		}
	}
	return ""
}

// ValidatePlan checks tasks against the rules of a good task. existing holds
// the ids of the session's tasks a "#id" may name; limit is the most tasks the
// session may hold and existingTotal how many it holds now. It returns no
// rejections for a plan that may be written.
func ValidatePlan(tasks []PlanTask, existing map[int64]bool, existingTotal, limit int) []Rejection {
	var out []Rejection
	if len(tasks) == 0 {
		return []Rejection{{Reasons: []string{"the plan has no tasks"}}}
	}
	if existingTotal+len(tasks) > limit {
		out = append(out, Rejection{Reasons: []string{fmt.Sprintf("the session would hold %d tasks, over its limit of %d: cut or merge tasks", existingTotal+len(tasks), limit)}})
	}
	refs := map[string]int{}
	dup := map[string]bool{}
	for i, t := range tasks {
		if t.Ref != "" {
			if _, seen := refs[t.Ref]; seen {
				dup[t.Ref] = true
			}
			refs[t.Ref] = i
		}
	}
	children := map[string]int{}
	for _, t := range tasks {
		if t.Parent != "" {
			children[t.Parent]++
		}
	}
	cyclic := cycleMembers(tasks, refs)
	for _, t := range tasks {
		var why []string
		add := func(f string, a ...any) { why = append(why, fmt.Sprintf(f, a...)) }
		switch {
		case t.Ref == "":
			add("has no ref")
		case !refPattern.MatchString(t.Ref):
			add("ref %q is not 1-40 letters, digits, dots, dashes or underscores", t.Ref)
		case dup[t.Ref]:
			add("ref %q is used twice", t.Ref)
		}
		if strings.TrimSpace(t.Title) == "" {
			add("has no title")
		}
		kind := t.Kind
		if kind == "" {
			kind = kindTask
		}
		if kind != kindTask && kind != kindEpic && kind != kindCheck {
			add("unknown kind %q (task, epic or check)", t.Kind)
		}
		if kind == kindEpic {
			if strings.TrimSpace(t.Acceptance) == "" {
				add("epic has no acceptance: say what its children together deliver")
			}
			if children[t.Ref] == 0 {
				add("epic has no children: give tasks this epic as their parent, or make it a task")
			}
		} else {
			if !checkable(t.Acceptance) {
				add("acceptance is missing or not checkable: name a command in backticks, a test, a file that must exist, an output, or start it with \"rubric:\" for a reviewer")
			}
			if len(t.Files) == 0 {
				add("lists no files: name the files it touches")
			}
			switch t.Size {
			case SizeXS, SizeS, SizeM:
			case SizeL:
				add("size l is too big for one task: split it into tasks of size m or smaller")
			default:
				add("size %q is not xs, s, m or l", t.Size)
			}
		}
		for _, f := range t.Files {
			if r := cleanFile(f); r != "" {
				add("%s", r)
			}
		}
		if t.Risk != "" && t.Risk != "low" && t.Risk != "medium" && t.Risk != "high" {
			add("risk %q is not low, medium or high", t.Risk)
		}
		for _, d := range t.DependsOn {
			if bad := refProblem(d, t.Ref, refs, existing); bad != "" {
				add("depends_on %s", bad)
			}
		}
		if t.Parent != "" {
			if bad := refProblem(t.Parent, t.Ref, refs, existing); bad != "" {
				add("parent %s", bad)
			}
		}
		if cyclic[t.Ref] && t.Ref != "" {
			add("is part of a dependency or parent cycle")
		}
		if len(why) > 0 {
			out = append(out, Rejection{Ref: t.Ref, Title: t.Title, Reasons: why})
		}
	}
	return out
}

// refProblem says why ref cannot be named from the task self, or "".
func refProblem(ref, self string, refs map[string]int, existing map[int64]bool) string {
	if n, ok := existingRef(ref); ok {
		if !existing[n] {
			return fmt.Sprintf("%s: no such task in this session", ref)
		}
		return ""
	}
	if ref == self {
		return fmt.Sprintf("%q: a task cannot name itself", ref)
	}
	if _, ok := refs[ref]; !ok {
		return fmt.Sprintf("%q: no such task in this plan (use a ref of this batch or #<id> of an existing task)", ref)
	}
	return ""
}

// cycleMembers finds the tasks of the batch that lie on a cycle of
// dependencies or parents.
func cycleMembers(tasks []PlanTask, refs map[string]int) map[string]bool {
	edges := map[string][]string{}
	for _, t := range tasks {
		if t.Ref == "" {
			continue
		}
		for _, d := range append(append([]string{}, t.DependsOn...), t.Parent) {
			if _, ok := refs[d]; ok && d != "" {
				edges[t.Ref] = append(edges[t.Ref], d)
			}
		}
	}
	on := map[string]bool{}
	for start := range edges {
		// start is on a cycle when it can reach itself.
		seen := map[string]bool{}
		stack := append([]string{}, edges[start]...)
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n == start {
				on[start] = true
				break
			}
			if seen[n] {
				continue
			}
			seen[n] = true
			stack = append(stack, edges[n]...)
		}
	}
	return on
}

// writeOrder sorts a validated batch so every task comes after the batch tasks
// it depends on and its parent (the store takes dependencies that exist).
func writeOrder(tasks []PlanTask) []PlanTask {
	byRef := map[string]PlanTask{}
	for _, t := range tasks {
		byRef[t.Ref] = t
	}
	done := map[string]bool{}
	var out []PlanTask
	var visit func(t PlanTask)
	visit = func(t PlanTask) {
		if done[t.Ref] {
			return
		}
		done[t.Ref] = true
		for _, d := range append(append([]string{}, t.DependsOn...), t.Parent) {
			if p, ok := byRef[d]; ok {
				visit(p)
			}
		}
		out = append(out, t)
	}
	for _, t := range tasks {
		visit(t)
	}
	return out
}

// MetaPrefix starts the line that carries a task's machine-readable plan data
// at the end of its notes.
const MetaPrefix = "[mission-meta] "

// TaskMeta is what a plan says about a task beyond the store's columns. The
// tasks table has no column for it, so it is the last line of the notes:
// MetaPrefix and one JSON object. Phase 4 reads Files for write-conflict
// planning.
type TaskMeta struct {
	Files []string `json:"files,omitempty"`
	Size  string   `json:"size,omitempty"`
	Risk  string   `json:"risk,omitempty"`
}

// notesWithMeta appends the meta line to the lead's notes.
func notesWithMeta(notes string, m TaskMeta) string {
	files := append([]string{}, m.Files...)
	sort.Strings(files)
	m.Files = files
	b, _ := json.Marshal(m)
	notes = strings.TrimRight(notes, "\n ")
	if notes != "" {
		notes += "\n\n"
	}
	return notes + MetaPrefix + string(b)
}

// ParseTaskMeta reads the meta line of a task's notes; ok is false when the
// task has none (a task the operator wrote).
func ParseTaskMeta(notes string) (m TaskMeta, ok bool) {
	lines := strings.Split(strings.TrimRight(notes, "\n "), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, MetaPrefix) {
		return TaskMeta{}, false
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(last, MetaPrefix)), &m); err != nil {
		return TaskMeta{}, false
	}
	return m, true
}
