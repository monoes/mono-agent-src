// Package callers lists the ways a caller can reach a user of the TypeSafe
// key, for the tests that hold that the stored key goes only to the TypeSafe
// host and, below the allowed level, is not read at all. It is test support:
// it is imported only by _test.go files.
package callers

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/secrets"
)

// Caller is one way of running a user of the key.
type Caller struct {
	Name string
	// Operator is the user's own terminal (or a workflow the user approved):
	// the only caller whose TYPESAFE_BASE_URL is honoured, for a
	// TYPESAFE_API_KEY.
	Operator bool
	// Refused: the key's read is refused (an unapproved step, agent access off).
	Refused bool
	// Granted: an unapproved step under the profile's agent access.
	Granted bool
	prep    func(t *testing.T)
	refuse  bool
}

// Prepare readies the process (markers, the turn check) for c.
func (c Caller) Prepare(t *testing.T) {
	t.Helper()
	if c.prep != nil {
		c.prep(t)
	}
}

// Ctx is base as c's step runs it. For a grant it switches agent access on
// for profile (a profile that exists in db) and names the step "n1".
func (c Caller) Ctx(t *testing.T, db *sql.DB, base context.Context, profile, nodeType string) context.Context {
	t.Helper()
	ctx := base
	if c.refuse {
		ctx = secrets.RefuseResolving(ctx, "an AI wrote this step")
	}
	if c.Granted {
		if err := secrets.SetAgentAccessGrant(context.Background(), db, profile, secrets.AccessGrant{AnyHost: true}); err != nil {
			t.Fatal(err)
		}
		ctx = secrets.AllowResolving(ctx, db, profile, secrets.AccessOrigin{WorkflowID: "wf", ExecutionID: "ex"})
		ctx = secrets.WithAccessNodeType(secrets.WithAccessNode(ctx, "n1", "node"), nodeType)
	}
	return ctx
}

// All is every caller: the operator, each kind of marked process, a process
// of a coder turn that cleared its markers, and an unapproved step with agent
// access off or on, alone and inside a marked agent.
func All() []Caller {
	claude := func(t *testing.T) { t.Setenv("CLAUDECODE", "1") }
	return []Caller{
		{Name: "operator", Operator: true},
		{Name: "marked agent process", prep: claude},
		{Name: "chat process", prep: func(t *testing.T) { t.Setenv("MONOAGENT_AI_CALLER", "chat") }},
		{Name: "workflow-run process", prep: func(t *testing.T) { t.Setenv("MONOAGENT_WORKFLOW_RUN", "exec-1") }},
		{Name: "in-turn process with its markers cleared", prep: func(t *testing.T) {
			opgate.SetForTest(t, func(bool) string { return "a coder session's turn runs this command" })
		}},
		{Name: "unapproved step, agent access off", Refused: true, refuse: true},
		{Name: "unapproved step, marked agent, agent access off", Refused: true, refuse: true, prep: claude},
		{Name: "unapproved step, agent access on", Granted: true},
		{Name: "unapproved step in a marked agent, agent access on", Granted: true, prep: claude},
	}
}

// StoredKeyReach is what must hold after a user of the key ran as c with the
// vault's key `key` stored and TwoHosts up. refused is the text the refusal
// of the step reads in whatever the user of the key returned ("" skips that
// check, for a user that swallows the error).
func (c Caller) StoredKeyReach(t *testing.T, typesafe, envHost *jevtest.Probe, key, refused, got string) {
	t.Helper()
	switch {
	case c.Refused:
		if typesafe.Calls() != 0 {
			t.Errorf("a step refused its key sent %d request(s) to the TypeSafe host", typesafe.Calls())
		}
		if refused != "" && !strings.Contains(got, refused) {
			t.Errorf("an unapproved step with agent access off was not refused: %q", got)
		}
	default:
		if !typesafe.Saw(key) {
			t.Errorf("the key should reach the TypeSafe host itself")
		}
	}
	if envHost.Calls() != 0 || envHost.Saw(key) {
		t.Errorf("the stored TypeSafe key reached the host the environment named (%d calls)", envHost.Calls())
	}
}

// Audited is the audit holding under c: one row naming the key's entry for a
// grant, none otherwise.
func (c Caller) Audited(t *testing.T, db *sql.DB, profile, entry string) {
	t.Helper()
	rows, _ := secrets.AgentAccessAudit(context.Background(), db, profile)
	switch {
	case c.Granted && (len(rows) == 0 || rows[0].Secret != entry || rows[0].NodeID != "n1"):
		t.Errorf("under agent access the key's use is audited by name: %+v", rows)
	case !c.Granted && len(rows) != 0:
		t.Errorf("audit rows without a grant: %+v", rows)
	}
}
