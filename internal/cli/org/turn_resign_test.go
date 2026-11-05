package org

import (
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/orgsign/orgsigntest"
)

// A coder turn's shell that carries no agent marker is still a turn: the
// turn-aware gate (opgate) says so. Its edit to an org the operator had
// signed must not be re-signed with the operator's key, or the turn would
// launder its own change into an approved one (#1473). The edit itself lands.

type turnResignCase struct {
	name string
	// setup runs as the operator before the org is signed.
	setup func(t *testing.T, f *orgCLIFixture)
	args  []string
	// orgs are the org files signed before the command; checked is the one whose signature must go stale.
	orgs    []string
	checked string
	// operatorOnly marks a command the class-B decision refuses a turn outright (class_b_test.go):
	// there is no turn edit to leave unsigned.
	operatorOnly bool
}

func addAutomation(t *testing.T, f *orgCLIFixture) {
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.outboundWF, "--alias", "publish_post")
}

func addGrant(t *testing.T, f *orgCLIFixture) {
	addAutomation(t, f)
	f.mustRun(t, "grant", "add", "growth", "--role", "writer", "--automation", "publish_post")
}

func addAutomationRole(t *testing.T, f *orgCLIFixture) {
	addAutomation(t, f)
	f.mustRun(t, "automation-role", "add", "growth", "--alias", "publish_post", "--reports-to", "lead")
}

func addHolding(t *testing.T, f *orgCLIFixture) {
	for _, child := range []string{"sales", "support"} {
		if _, err := orgdesign.Save(f.root, orgdesign.NewOrg(child, "work", orgdesign.NewOrgOptions{})); err != nil {
			t.Fatal(err)
		}
	}
	hq := orgdesign.NewOrg("hq", "own the group", orgdesign.NewOrgOptions{RootRoleID: "initiator", RootRoleTitle: "Initiator"})
	hq.Kind = orgdesign.OrgKindHolding
	hq.ChildOrgs = []orgdesign.ChildOrg{{Org: "sales"}, {Org: "support"}}
	if _, err := orgdesign.Save(f.root, hq); err != nil {
		t.Fatal(err)
	}
}

var turnResignCases = []turnResignCase{
	{name: "grant add", setup: addAutomation, args: []string{"grant", "add", "growth", "--role", "writer", "--automation", "publish_post"}, operatorOnly: true},
	{name: "grant remove", setup: addGrant, args: []string{"grant", "remove", "growth", "--role", "writer", "--automation", "publish_post"}},
	{name: "automation add", args: []string{"automation", "add", "growth", "--workflow", "@outbound", "--alias", "publish_post"}},
	{name: "automation remove", setup: addAutomation, args: []string{"automation", "remove", "growth", "--alias", "publish_post"}},
	{name: "automation-role add", setup: addAutomation, args: []string{"automation-role", "add", "growth", "--alias", "publish_post", "--reports-to", "lead"}},
	{name: "automation-role remove", setup: addAutomationRole, args: []string{"automation-role", "remove", "growth", "--role", "publish-post"}},
	{name: "automation-role rotate", setup: addAutomationRole, args: []string{"automation-role", "rotate", "growth", "--role", "publish-post"}},
	{name: "autonomy set (non-widening)", args: []string{"autonomy", "set", "growth", "--level", "manual"}, operatorOnly: true},
	{name: "autonomy pause", args: []string{"autonomy", "pause", "growth", "--for", "30m"}},
	{name: "group init", setup: addHolding, args: []string{"group", "init", "hq"}, orgs: []string{"hq", "sales", "support"}, checked: "hq"},
	{name: "rename", args: []string{"rename", "growth", "beta"}, checked: "beta"},
}

// runTurnResignCase signs the case's orgs, runs the command, and returns the
// signer's calls and the state of the org the command changed.
func runTurnResignCase(t *testing.T, c turnResignCase, before func(t *testing.T)) (calls int, st orgsign.Status) {
	t.Helper()
	f, s, _ := newSigningFixture(t, "2.21.0")
	if c.setup != nil {
		c.setup(t, f)
	}
	orgs := c.orgs
	if orgs == nil {
		orgs = []string{"growth"}
	}
	for _, o := range orgs {
		orgsigntest.Sign(t, f.root, o)
	}
	s.calls = nil
	before(t)
	args := append([]string(nil), c.args...)
	for i, a := range args {
		if a == "@outbound" {
			args[i] = f.outboundWF
		}
	}
	captureStderr(t, func() { f.mustRun(t, args...) })
	checked := c.checked
	if checked == "" {
		checked = "growth"
	}
	st, _, err := orgsign.VerifyFile(f.root, checked)
	if err != nil {
		t.Fatal(err)
	}
	return len(s.calls), st
}

func inTurn(t *testing.T) {
	opgate.SetForTest(t, func(bool) string { return "a coder session's turn runs this command" })
}

func TestTurnEditLeavesASignedOrgUnsigned(t *testing.T) {
	for _, c := range turnResignCases {
		if c.operatorOnly {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			calls, st := runTurnResignCase(t, c, inTurn)
			if calls != 0 || st.OK() {
				t.Fatalf("a turn's edit was re-signed: sign calls %d, state %+v", calls, st)
			}
		})
	}
}

// The operator's own terminal outside a turn keeps the re-signature.
func TestOperatorEditOfASignedOrgIsResigned(t *testing.T) {
	for _, c := range turnResignCases {
		t.Run(c.name, func(t *testing.T) {
			calls, st := runTurnResignCase(t, c, func(t *testing.T) { opgate.SetForTest(t, func(bool) string { return "" }) })
			if calls == 0 || !st.OK() {
				t.Fatalf("the operator's edit was not re-signed: sign calls %d, state %+v", calls, st)
			}
		})
	}
}

// A marked agent is still not re-signed.
func TestMarkedAgentEditIsStillNotResigned(t *testing.T) {
	for _, c := range turnResignCases {
		if c.name != "grant remove" && c.name != "automation add" && c.name != "automation remove" {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			calls, st := runTurnResignCase(t, c, func(t *testing.T) { t.Setenv("CLAUDECODE", "1") })
			if calls != 0 || st.OK() {
				t.Fatalf("a marked agent's edit was re-signed: sign calls %d, state %+v", calls, st)
			}
		})
	}
}

// A new org (create-json) is never signed by the write, in a turn or not.
func TestTurnCreateJSONStaysUnsigned(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.21.0")
	inTurn(t)
	doc := `{"name":"fresh","goal":"g","status":"stopped","schedule":null,"roles":[{"id":"lead","title":"Lead","type":"lead","reports_to":null,"responsibilities":["lead"]}]}`
	captureStderr(t, func() { f.mustRun(t, "create-json", "fresh", "--json", doc) })
	if len(s.calls) != 0 {
		t.Fatalf("sign calls %v", s.calls)
	}
}
