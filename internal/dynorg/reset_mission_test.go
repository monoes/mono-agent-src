package dynorg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// org_clear_worker and org_compact_worker must not drop the context of a worker that is
// bound to a mission task, running (a verifier mid-run is one) or between its run and the
// verdict: the report and session are what the task's retry and verification build on.
func TestResetToolsRefuseMissionWorkers(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	res := r.next(r.c)
	if got := r.startedIDs(res); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("task a not started: %+v", res)
	}
	refuse := func(when string) {
		t.Helper()
		for _, tool := range []string{ToolClearWorker, ToolCompactWorker} {
			_, err := r.c.Handle(tbg, tool, json.RawMessage(`{"agent_id":"w1"}`))
			if err == nil || !(strings.Contains(err.Error(), "mission task") || strings.Contains(err.Error(), "still working")) {
				t.Errorf("%s %s: %v", tool, when, err)
			}
		}
	}
	refuse("while the task is in progress")
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the worker to finish", func() bool {
		r.c.mu.Lock()
		defer r.c.mu.Unlock()
		return !running(r.c.workers["w1"].status)
	})
	refuse("after its run, before the verdict")
	r.c.mu.Lock()
	w := r.c.workers["w1"]
	kept := w.session != "" || w.report != ""
	r.c.mu.Unlock()
	if !kept {
		t.Error("the refused reset still dropped the worker's context")
	}
}

// The mission tools and the reset tools are offered to the same lead: no name may repeat.
func TestLeadToolNamesAreUniqueWithMissionAndResetTools(t *testing.T) {
	m := &MissionConfig{Store: missionStore(t), ProfileID: missionProfile, ConversationID: "c1", Actor: "mission-lead", Access: "coding", Confirmed: true}
	c, _, _ := missionConductor(t, m.Store, "c1", true, nil)
	if _, err := call(t, c, ToolMissionPlan, goodPlan()); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	specs := append(ToolSpecs(true), MissionPlanToolSpecs(m)...)
	specs = append(specs, MissionRunToolSpecs(tbg, m)...)
	for _, s := range specs {
		if seen[s.Name] {
			t.Errorf("tool %s is offered twice", s.Name)
		}
		seen[s.Name] = true
	}
	for _, n := range []string{ToolClearWorker, ToolCompactWorker, ToolMissionNext, ToolMissionFinish, ToolMissionPlan} {
		if !seen[n] {
			t.Errorf("%s is not offered", n)
		}
	}
}
