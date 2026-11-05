package org

import (
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"strings"
	"testing"
)

// The class-B decision: an unmarked process inside a coder turn is treated as an agent for the
// commands that switch an org into a looser mode. Outright: grant add (every approval mode),
// autonomy set, autonomy resume, approve. The restrictive verbs (grant remove, autonomy pause)
// stay open to a turn; their edits are just not re-signed (turn_resign_test.go).

var classBCommands = []struct {
	name  string
	setup func(t *testing.T, f *orgCLIFixture)
	args  []string
}{
	{"grant add (default approval)", addAutomation, []string{"grant", "add", "growth", "--role", "writer", "--automation", "publish_post"}},
	{"grant add (approval required)", addAutomation, []string{"grant", "add", "growth", "--role", "writer", "--automation", "publish_post", "--approval", "required"}},
	{"grant add (approval none)", addAutomation, []string{"grant", "add", "growth", "--role", "writer", "--automation", "publish_post", "--approval", "none"}},
	{"autonomy set (stricter)", nil, []string{"autonomy", "set", "growth", "--level", "manual"}},
	{"autonomy set (mid)", nil, []string{"autonomy", "set", "growth", "--level", "mid"}},
	{"autonomy resume (nothing paused)", nil, []string{"autonomy", "resume", "growth"}},
}

func TestClassBCommandsRefuseAnUnmarkedTurn(t *testing.T) {
	for _, c := range classBCommands {
		t.Run(c.name, func(t *testing.T) {
			f := newOrgCLIFixture(t)
			if c.setup != nil {
				c.setup(t, f)
			}
			asInATurn(t)
			if _, err := f.run(t, c.args...); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), turnStandIn) {
				t.Fatalf("a turn ran it: %v", err)
			}
		})
	}
}

func TestClassBCommandsRefuseAMarkedAgent(t *testing.T) {
	for _, c := range classBCommands {
		t.Run(c.name, func(t *testing.T) {
			f := newOrgCLIFixture(t)
			if c.setup != nil {
				c.setup(t, f)
			}
			t.Setenv("CLAUDECODE", "1")
			if _, err := f.run(t, c.args...); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "CLAUDECODE") {
				t.Fatalf("a marked agent ran it: %v", err)
			}
		})
	}
}

func TestClassBCommandsStayTheOperators(t *testing.T) {
	for _, c := range classBCommands {
		t.Run(c.name, func(t *testing.T) {
			f := newOrgCLIFixture(t)
			if c.setup != nil {
				c.setup(t, f)
			}
			opgateOperator(t)
			if _, err := f.run(t, c.args...); err != nil {
				t.Fatalf("the operator: %v", err)
			}
		})
	}
}

// The restrictive verbs stay a turn's.
func TestClassBRestrictiveVerbsStayOpenToATurn(t *testing.T) {
	f := newOrgCLIFixture(t)
	addGrant(t, f)
	asInATurn(t)
	f.mustRun(t, "autonomy", "pause", "growth", "--for", "30m")
	f.mustRun(t, "grant", "remove", "growth", "--role", "writer", "--automation", "publish_post")
}

func opgateOperator(t *testing.T) { opgate.SetForTest(t, func(bool) string { return "" }) }
