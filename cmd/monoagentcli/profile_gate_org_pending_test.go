package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

// orgPendingRow is `org pending --all` in the RequireOperator wiring table.
var orgPendingRow = reqOpRow{name: "org pending --all", file: "org_env.go", build: newOrgCmd, args: []string{"pending", "--all"}}

// Whatever is not the user's own terminal is refused the cross-profile list of
// what organizations wait on, through the production gate, as `hil pending
// --all` is: a coder turn and every environment context.
func TestOrgPendingAllIsRefusedToWhateverIsNotTheOperator(t *testing.T) {
	row := opRow{path: "org pending", build: newOrgCmd, args: []string{"pending", "--all"}}
	env := func(t *testing.T) *globalConfig {
		cfg := opEnv(t)
		cfg.ProfileID = "" // --all names no profile
		return cfg
	}
	t.Run("operator", func(t *testing.T) {
		if err := row.run(t, env(t)); err != nil {
			t.Fatalf("the operator: %v", err)
		}
	})
	t.Run("a coder turn", func(t *testing.T) {
		needAncestry(t)
		cfg := env(t)
		markThisProcessAsACoderTurn(t, cfg)
		requireOperatorOnly(t, row.run(t, cfg))
	})
	for name, setup := range gateOSMenuContexts() {
		t.Run(name, func(t *testing.T) {
			cfg := env(t)
			setup(t)
			requireOperatorOnly(t, row.run(t, cfg))
		})
	}
}

// `org pending --all` needs no --profile and takes none; without --all the
// command names its profile like every other.
func TestOrgPendingAllNeedsAndTakesNoProfile(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	t.Setenv("MONOMIND_BIN", "/nonexistent/monomind") // no org exists: monomind is never needed
	dbPath := filepath.Join(t.TempDir(), "gate.db")
	for _, args := range [][]string{{"org", "pending"}, {"org", "pending", "--all=false"}} {
		if _, err := runGateCLIOn(dbPath, args...); err == nil || !strings.Contains(err.Error(), "profile") {
			t.Fatalf("%v without a profile: %v", args, err)
		}
	}
	// The test process may itself count as an agent's; either way nothing may ask for a profile.
	if out, err := runGateCLIOn(dbPath, "org", "pending", "--all"); err != nil && !strings.Contains(err.Error(), "only the user") {
		t.Fatalf("--all: %v\n%s", err, out)
	}
	if _, err := runGateCLIOn(dbPath, "--profile", "default", "org", "pending", "--all"); err == nil || !strings.Contains(err.Error(), "leave out") && !strings.Contains(err.Error(), "only the user") {
		t.Fatalf("both flags: %v", err)
	}
}
