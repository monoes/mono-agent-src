package coderhost

import (
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
)

// The operator's switch is off in a default configuration: a master whose max_children was set long ago (so
// max_children > 0) and a task a model marked start nothing, by any entry point, until the operator enables it.
func TestADefaultConfigSpawnsNoMissionChildEvenWithAMarkAndRoom(t *testing.T) {
	f := newMCDefault(t, true)
	if on, err := f.st.MissionChildrenEnabled(); err != nil || on {
		t.Fatalf("enabled = %v %v on a default config", on, err)
	}
	n := 3
	f.setPolicy(t, OperatorScope{}, PolicyPatch{MaxChildren: &n})
	f.mark(t, 0) // a mark is stored: the lead may still write marks
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrChildrenOff) {
		t.Fatalf("Reserve = %v, want ErrChildrenOff", err)
	}
	if _, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "brief"); !errors.Is(err, ErrChildrenOff) {
		t.Fatalf("SpawnForTask = %v, want ErrChildrenOff", err)
	}
	if kids, _ := f.st.ChildrenOfSession("p", f.se.ID); len(kids) != 1 || kids[0].State != ChildMarked || kids[0].Attempts != 0 {
		t.Fatalf("rows = %+v: the mark must stay marked, with no attempt used", kids)
	}
	// once the operator enables, the same call spawns
	if err := f.st.SetMissionChildrenEnabled(true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "brief"); err != nil {
		t.Fatalf("after enable: %v", err)
	}
}

func TestMissionChildrenSwitchIsAReservedConfigRow(t *testing.T) {
	if !operator.ReservedConfig(MissionChildrenConfig) {
		t.Fatalf("%s is not reserved: `config set` or an agent write path could flip it", MissionChildrenConfig)
	}
}

// Turning the switch off stops new spawns only: the running child stays running and is later settled and charged.
func TestDisablingLeavesARunningChildRunningAndSettled(t *testing.T) {
	f := newHostFix(t)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	conv := f.endedChild(t, 0, 1.25) // spawned with the switch on, finished its work
	if err := f.st.SetMissionChildrenEnabled(false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[1], MarkOptions{}); err != nil {
		t.Fatal(err) // marks still store
	}
	if _, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[1], "brief"); !errors.Is(err, ErrChildrenOff) {
		t.Fatalf("second spawn = %v, want ErrChildrenOff", err)
	}
	if r := f.row(t, 0); r.State != ChildSpawned || r.ChildConversationID != conv {
		t.Fatalf("running child after disable: %+v", r)
	}
	f.h.missionChildPass() // the pass reconciles the existing row even though the switch is off
	if r := f.row(t, 0); r.State != ChildEnded {
		t.Fatalf("the child was not settled after disable: %+v", r)
	}
	if got := f.missionSpent(t); got != 1.25 {
		t.Fatalf("mission spent = %v, want the child's 1.25", got)
	}
}

func TestTheDigestSaysChildrenAreOffForMarks(t *testing.T) {
	f := newMCDefault(t, true)
	f.mark(t, 0)
	h := &Host{Store: f.st}
	m, ok := h.missionOf("p", f.lead.ConversationID)
	if !ok || m == nil || !m.ChildrenOff || m.ChildQueued != 0 {
		t.Fatalf("fact = %+v ok=%v", m, ok)
	}
	if line := missionLine(m); !strings.Contains(line, "off (operator setting)") {
		t.Fatalf("digest line = %q", line)
	}
}

func TestLeadStatusAndOverviewShowTheSwitch(t *testing.T) {
	f := newMCDefault(t, true)
	st, err := f.st.LeadStatusOf(f.scope(), "p")
	if err != nil || st.Switch != "children: off (operator setting)" {
		t.Fatalf("status = %+v %v", st, err)
	}
	if ov, err := f.st.OverviewOf("p"); err != nil || ov.Enabled {
		t.Fatalf("overview = %+v %v", ov, err)
	}
	_ = f.st.SetMissionChildrenEnabled(true)
	if st, _ := f.st.LeadStatusOf(f.scope(), "p"); st.Switch != "children: on" {
		t.Fatalf("status = %+v", st)
	}
	if ov, _ := f.st.OverviewOf("p"); !ov.Enabled {
		t.Fatalf("overview = %+v", ov)
	}
}
