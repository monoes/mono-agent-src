package doomsday

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenVerbs are the names of verbs that act on a run. The control room reads; it never starts, approves, pauses,
// resumes or grants, and none of those is ever added to its verb set (Stop alone stays the existing `doomsday stop`).
var forbiddenVerbs = []string{"start", "approve", "pause", "resume", "grant"}

// forbiddenFlags are the flags of the verbs that act; a read verb with one of them would be a way to act.
var forbiddenFlags = []string{"yes-i-lift", "hash", "charter", "boss-session", "chat", "approve", "resume", "pause", "grant", "start"}

func TestControlRoomVerbSetHasNoActingVerb(t *testing.T) {
	if len(ControlRoomVerbs) == 0 {
		t.Fatal("empty verb set")
	}
	for _, v := range ControlRoomVerbs {
		for _, bad := range forbiddenVerbs {
			if strings.Contains(strings.ToLower(v), bad) {
				t.Fatalf("control room verb %q is or contains %q", v, bad)
			}
		}
	}
}

func TestControlRoomVerbsAreRegisteredReadOnlyAndExactly(t *testing.T) {
	grp := NewCommand(Deps{})
	for _, name := range ControlRoomVerbs {
		c, _, err := grp.Find([]string{name})
		if err != nil || c == nil || c.Name() != name {
			t.Fatalf("verb %q is not registered: %v", name, err)
		}
		if c.HasSubCommands() {
			t.Fatalf("%q has sub-verbs", name)
		}
		for _, f := range forbiddenFlags {
			if c.Flags().Lookup(f) != nil {
				t.Fatalf("read verb %q has the acting flag --%s", name, f)
			}
		}
		if !strings.Contains(c.Long, "operator_only") {
			t.Fatalf("%q does not carry the operator-only help", name)
		}
	}
	// the group's verbs are the existing ones plus exactly the control room's
	existing := map[string]bool{"start": true, "stop": true, "status": true, "render": true, "finish": true, "rubric": true, "strategy": true,
		// probe answers "can a run start here, and why not" (nothing is written, nothing is read of a run): classified on
		// purpose and held to the same bar as a read verb just below, TestProbeVerbIsReadOnlyAndOperatorOnly.
		"probe": true}
	for _, v := range ControlRoomVerbs {
		existing[v] = true
	}
	for _, c := range grp.Commands() {
		if !existing[c.Name()] {
			t.Fatalf("unclassified doomsday verb %q", c.Name())
		}
	}
}

// The files of the control room do not call anything of the store or the engine that writes.
func TestControlRoomSourcesCallNoMutator(t *testing.T) {
	mutators := []string{".Enter(", ".Advance(", ".AppendAudit(", ".Register(", ".Release(", ".ConsumeCumulative(", ".ConsumeMany(", ".Stop(",
		".BindBoss(", ".Issue(", ".Rescind(", ".ExecContext(", "BeginTx(", ".Exec("}
	files, err := filepath.Glob("controlroom*.go")
	if err != nil || len(files) < 3 {
		t.Fatalf("control room sources not found: %v %v", files, err)
	}
	files2, _ := filepath.Glob("storeengine_readview.go")
	for _, f := range append(files, files2...) {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range mutators {
			if strings.Contains(string(src), m) {
				t.Errorf("%s calls %s: the control room is read-only", f, m)
			}
		}
	}
}

// The probe verb is not a control-room read (its document is not an overview) but it is held to the same bar: no flag
// of a verb that acts, no sub-verbs, the operator-only help.
func TestProbeVerbIsReadOnlyAndOperatorOnly(t *testing.T) {
	c, _, err := NewCommand(Deps{}).Find([]string{"probe"})
	if err != nil || c == nil || c.Name() != "probe" || c.HasSubCommands() {
		t.Fatalf("probe: %v", err)
	}
	for _, f := range forbiddenFlags {
		if c.Flags().Lookup(f) != nil {
			t.Fatalf("probe has the acting flag --%s", f)
		}
	}
	if !strings.Contains(c.Long, "operator_only") {
		t.Fatal("probe does not carry the operator-only help")
	}
}
