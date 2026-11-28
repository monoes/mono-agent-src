package task

import (
	"strings"
	"testing"
)

// The preview and `pursuit readiness` agree about goal checks (#1767): a goal of rubric criteria only is told that no
// check runs for it, in the plain and the JSON preview; a goal with a command criterion is not told so.
func TestPursuitPreviewAgreesWithReadinessAboutGoalChecks(t *testing.T) {
	const line = "This goal has no command checks"
	w := newBeginWorld(t)
	if p := w.preview(t); strings.Contains(p.Cost.Text, line) {
		t.Fatalf("a goal with a command criterion:\n%s", p.Cost.Text)
	}
	asLead(t, w.tok)
	mustTaskJSON(t, w.db, "default", &goalDoc{}, `[{"kind":"rubric","spec":"The person is satisfied"}]`, "session", "goal", "propose", id(w.s), "--criteria", "-", "--as", "lead")
	asOperator(t)
	p := w.preview(t)
	if !strings.Contains(p.Cost.Text, line) || !strings.Contains(p.Cost.Text, "Goal checks are possibly ON") {
		t.Fatalf("rubric only, JSON:\n%s", p.Cost.Text)
	}
	out, _, err := runTask(t, w.db, "default", false, "", "session", "pursuit", "preview", id(w.s))
	if err != nil || !strings.Contains(out, line) {
		t.Fatalf("rubric only, plain: %v\n%s", err, out)
	}
	out, _, err = runTask(t, w.db, "default", false, "", "session", "pursuit", "readiness", id(w.s))
	if err != nil || !strings.Contains(out, "no checker is needed") {
		t.Fatalf("readiness: %v\n%s", err, out)
	}
	// the added line is not part of the digest's text: a begin with the shown digest still goes through
	if _, errOut, err := runTask(t, w.db, "default", false, "", w.beginArgs(p)...); err != nil {
		t.Fatalf("begin: %v %s", err, errOut)
	}
}
