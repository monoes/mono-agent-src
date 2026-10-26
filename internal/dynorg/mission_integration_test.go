package dynorg

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

func TestValidatePlanIntegrationMustDependOnEveryTask(t *testing.T) {
	mk := func(ref string, deps ...string) PlanTask {
		return PlanTask{Ref: ref, Title: "t " + ref, Acceptance: "`go test ./...` exits 0", Files: []string{ref + ".go"}, Size: SizeS, DependsOn: deps}
	}
	it := PlanTask{Ref: "int", Title: "Integration check", Kind: kindCheck, Acceptance: "`go test ./...` exits 0", Files: []string{"./"}, Size: SizeS, DependsOn: []string{"a"}}
	rej := integrationRejections([]PlanTask{mk("a"), mk("b"), it}, nil)
	if len(rej) != 1 || !strings.Contains(rej[0].Reasons[0], "b") {
		t.Fatalf("rejections %+v", rej)
	}
	it.DependsOn = []string{"a", "b"}
	if rej := integrationRejections([]PlanTask{mk("a"), mk("b"), it}, nil); len(rej) != 0 {
		t.Fatalf("a covering integration check: %+v", rej)
	}
}

// A task added to a mission after its integration check passed: the check waits for it and
// is back in Ready, so the mission cannot close on the old verdict.
func TestAddingATaskAfterTheIntegrationCheckRequeuesIt(t *testing.T) {
	r, _, it := closeRig(t, true, true)
	m := r.leadOf("")
	se := r.session()
	ids, err := m.writeTasks(tbg, se, []PlanTask{{Ref: "late", Title: "late work", Acceptance: "`go test ./...` exits 0", Files: []string{"late.go"}, Size: SizeS}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.store.SessionTask(tbg, "default", r.sid, it.ID, operator)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Task; got.Status != tasks.StatusReady || got.Verified || !slices.Contains(got.DependsOn, ids["late"]) {
		t.Fatalf("integration check %+v, want Ready and waiting for #%d", got, ids["late"])
	}
	var ref *closeRefusal
	if err := r.tryClose(); !errors.As(err, &ref) {
		t.Fatalf("close: %v", err)
	}
}

// Whatever made the tree change after the check (here a task the operator added without an
// edge), the close refuses and mission_finish sends the check back.
func TestCloseRefusesAnIntegrationCheckOlderThanATask(t *testing.T) {
	r, _, it := closeRig(t, true, true)
	x := r.add(spec{title: "late", files: "l.go"})
	r.work(x.ID)
	var ref *closeRefusal
	err := r.tryClose()
	if !errors.As(err, &ref) || !strings.Contains(err.Error(), "changed after the integration check") {
		t.Fatalf("close: %v", err)
	}
	if r.session().State == tasks.SessionDone {
		t.Fatal("closed")
	}
	v := r.finish()
	if v.Finished || r.task(it.ID).Status != tasks.StatusReady {
		t.Fatalf("mission_finish %+v, integration %+v", v, r.task(it.ID))
	}
}

func TestCloseAcceptsAnIntegrationCheckNewerThanEveryTask(t *testing.T) {
	r, _, _ := closeRig(t, true, true)
	if err := r.tryClose(); err != nil {
		t.Fatal(err)
	}
}

// writeTasks refuses a plan whose integration check misses a task, and writes nothing.
func TestWriteTasksRefusesAnIntegrationCheckThatMissesATask(t *testing.T) {
	r := newRig(t, rigOpts{})
	m := r.leadOf("")
	mk := func(ref string) PlanTask {
		return PlanTask{Ref: ref, Title: "t " + ref, Acceptance: "`go test ./...` exits 0", Files: []string{ref + ".go"}, Size: SizeS}
	}
	chk := PlanTask{Ref: "int", Title: "Integration check", Kind: kindCheck, Acceptance: "`go test ./...` exits 0", Files: []string{"./"}, Size: SizeS, DependsOn: []string{"a"}}
	_, err := m.writeTasks(tbg, r.session(), []PlanTask{mk("a"), mk("b"), chk})
	var rej *RejectedError
	if !errors.As(err, &rej) || !strings.Contains(err.Error(), "integration check must depend") {
		t.Fatalf("err %v", err)
	}
	if plan, _ := r.store.PlanOfSession(tbg, "default", r.sid, operator); len(plan.Tasks) != 0 {
		t.Fatalf("a rejected plan wrote %d tasks", len(plan.Tasks))
	}
	chk.DependsOn = []string{"a", "b"}
	if _, err := m.writeTasks(tbg, r.session(), []PlanTask{mk("a"), mk("b"), chk}); err != nil {
		t.Fatal(err)
	}
}
