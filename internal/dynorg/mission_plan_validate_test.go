package dynorg

import (
	"strings"
	"testing"
)

func okTask(ref string) PlanTask {
	return PlanTask{Ref: ref, Title: "Do " + ref, Acceptance: "`go test ./pkg` exits 0", Files: []string{"pkg/a.go"}, Size: SizeS}
}

func with(t PlanTask, f func(*PlanTask)) PlanTask { f(&t); return t }

func reasonsOf(rej []Rejection, ref string) string {
	for _, r := range rej {
		if r.Ref == ref {
			return strings.Join(r.Reasons, "; ")
		}
	}
	return ""
}

func TestValidatePlanRules(t *testing.T) {
	cases := []struct {
		name string
		task PlanTask
		want string // substring of the reason; "" = accepted
	}{
		{"ok", okTask("a"), ""},
		{"no title", with(okTask("a"), func(p *PlanTask) { p.Title = "  " }), "no title"},
		{"no acceptance", with(okTask("a"), func(p *PlanTask) { p.Acceptance = "" }), "not checkable"},
		{"vague acceptance", with(okTask("a"), func(p *PlanTask) { p.Acceptance = "everything works as people expect" }), "not checkable"},
		{"short acceptance", with(okTask("a"), func(p *PlanTask) { p.Acceptance = "test" }), "not checkable"},
		{"rubric acceptance", with(okTask("a"), func(p *PlanTask) { p.Acceptance = "rubric: the doc explains the flag" }), ""},
		{"no files", with(okTask("a"), func(p *PlanTask) { p.Files = nil }), "lists no files"},
		{"absolute file", with(okTask("a"), func(p *PlanTask) { p.Files = []string{"/etc/passwd"} }), "relative"},
		{"escaping file", with(okTask("a"), func(p *PlanTask) { p.Files = []string{"a/../../b"} }), "leaves the workspace"},
		{"size l", with(okTask("a"), func(p *PlanTask) { p.Size = SizeL }), "split it"},
		{"no size", with(okTask("a"), func(p *PlanTask) { p.Size = "" }), "not xs, s, m or l"},
		{"size xs", with(okTask("a"), func(p *PlanTask) { p.Size = SizeXS }), ""},
		{"bad kind", with(okTask("a"), func(p *PlanTask) { p.Kind = "story" }), "unknown kind"},
		{"bad risk", with(okTask("a"), func(p *PlanTask) { p.Risk = "scary" }), "risk"},
		{"no ref", with(okTask(""), func(p *PlanTask) {}), "no ref"},
		{"bad ref", with(okTask("a b"), func(p *PlanTask) {}), "not 1-40"},
		{"unknown dep", with(okTask("a"), func(p *PlanTask) { p.DependsOn = []string{"zzz"} }), "no such task in this plan"},
		{"unknown existing dep", with(okTask("a"), func(p *PlanTask) { p.DependsOn = []string{"#99"} }), "no such task in this session"},
		{"existing dep", with(okTask("a"), func(p *PlanTask) { p.DependsOn = []string{"#7"} }), ""},
		{"self dep", with(okTask("a"), func(p *PlanTask) { p.DependsOn = []string{"a"} }), "cannot name itself"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rej := ValidatePlan([]PlanTask{c.task}, map[int64]bool{7: true}, 0, MaxPlanTasks)
			got := reasonsOf(rej, c.task.Ref)
			if c.want == "" && len(rej) != 0 {
				t.Fatalf("rejected: %+v", rej)
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Fatalf("reasons %q lack %q (all: %+v)", got, c.want, rej)
			}
		})
	}
}

func TestValidatePlanCycleDuplicateAndEpics(t *testing.T) {
	a := with(okTask("a"), func(p *PlanTask) { p.DependsOn = []string{"b"} })
	b := with(okTask("b"), func(p *PlanTask) { p.DependsOn = []string{"c"} })
	c := with(okTask("c"), func(p *PlanTask) { p.DependsOn = []string{"a"} })
	d := okTask("d")
	rej := ValidatePlan([]PlanTask{a, b, c, d}, nil, 0, MaxPlanTasks)
	for _, ref := range []string{"a", "b", "c"} {
		if !strings.Contains(reasonsOf(rej, ref), "cycle") {
			t.Errorf("task %s not flagged as cyclic: %+v", ref, rej)
		}
	}
	if len(rej) != 3 {
		t.Errorf("the task off the cycle was rejected too: %+v", rej)
	}

	rej = ValidatePlan([]PlanTask{okTask("x"), okTask("x")}, nil, 0, MaxPlanTasks)
	if !strings.Contains(reasonsOf(rej, "x"), "used twice") {
		t.Errorf("duplicate ref accepted: %+v", rej)
	}

	epic := PlanTask{Ref: "e", Title: "Epic", Kind: "epic", Acceptance: "all children done"}
	if !strings.Contains(reasonsOf(ValidatePlan([]PlanTask{epic}, nil, 0, MaxPlanTasks), "e"), "no children") {
		t.Error("childless epic accepted")
	}
	kid := with(okTask("k"), func(p *PlanTask) { p.Parent = "e" })
	if rej := ValidatePlan([]PlanTask{epic, kid}, nil, 0, MaxPlanTasks); len(rej) != 0 {
		t.Errorf("epic with a child rejected: %+v", rej)
	}
	pc := ValidatePlan([]PlanTask{with(okTask("p"), func(p *PlanTask) { p.Parent = "q" }), with(okTask("q"), func(p *PlanTask) { p.Parent = "p" })}, nil, 0, MaxPlanTasks)
	if len(pc) != 2 {
		t.Errorf("parent cycle: %+v", pc)
	}
}

func TestValidatePlanLimitsAndEmpty(t *testing.T) {
	if rej := ValidatePlan(nil, nil, 0, MaxPlanTasks); len(rej) != 1 || rej[0].Ref != "" {
		t.Errorf("empty plan: %+v", rej)
	}
	var many []PlanTask
	for i := 0; i < MaxPlanTasks+1; i++ {
		many = append(many, okTask("t"+string(rune('a'+i%26))+strings.Repeat("x", i/26)))
	}
	rej := ValidatePlan(many, nil, 0, MaxPlanTasks)
	if len(rej) == 0 || !strings.Contains(reasonsOf(rej, ""), "over its limit of 200") {
		t.Errorf("201 tasks accepted: %d rejections", len(rej))
	}
	if rej := ValidatePlan(many[:MaxPlanTasks], nil, 0, MaxPlanTasks); len(rej) != 0 {
		t.Errorf("200 tasks rejected: %+v", rej[:1])
	}
	// Tasks already in the session count.
	if rej := ValidatePlan(many[:10], nil, 195, MaxPlanTasks); len(rej) == 0 {
		t.Error("existing tasks not counted")
	}
}

func TestTaskMetaRoundTrip(t *testing.T) {
	n := notesWithMeta("context here\n", TaskMeta{Files: []string{"b.go", "a.go"}, Size: "m", Risk: "low"})
	m, ok := ParseTaskMeta(n)
	if !ok || m.Size != "m" || m.Risk != "low" || len(m.Files) != 2 || m.Files[0] != "a.go" {
		t.Fatalf("meta %+v ok=%v from %q", m, ok, n)
	}
	if !strings.HasPrefix(n, "context here\n\n"+MetaPrefix) {
		t.Errorf("notes %q", n)
	}
	if _, ok := ParseTaskMeta("plain notes"); ok {
		t.Error("meta found in plain notes")
	}
	// A meta line the lead wrote in its notes does not win: ours is last.
	forged := notesWithMeta(MetaPrefix+`{"size":"xs"}`, TaskMeta{Size: "m"})
	if m, _ := ParseTaskMeta(forged); m.Size != "m" {
		t.Errorf("forged meta won: %+v", m)
	}
}
