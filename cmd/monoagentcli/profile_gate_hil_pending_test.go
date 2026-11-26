package main

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"strings"
	"testing"
)

// Whatever is not the user's own terminal is refused the cross-profile list,
// through the production gate: a coder turn and every environment context.
func TestHILPendingAllIsRefusedToWhateverIsNotTheOperator(t *testing.T) {
	row := opRow{path: "hil pending", build: newHILCmd, args: []string{"pending", "--all"}}
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

// `hil pending --all` is the one cross-profile read of the queue: it needs no
// --profile and takes none; without --all the command names its profile like
// every other.
func TestHILPendingAllNeedsAndTakesNoProfile(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "gate.db")
	if _, err := runGateCLIOn(dbPath, "--profile", "default", "hil", "pending"); err != nil {
		t.Fatalf("with a profile: %v", err)
	}
	if _, err := runGateCLIOn(dbPath, "--json", "hil", "pending"); err == nil || !strings.Contains(err.Error(), "acts on a profile") {
		t.Fatalf("without --all or --profile: got %v, want the gate's refusal", err)
	}
	if _, err := runGateCLIOn(dbPath, "hil", "pending", "--all=false"); err == nil || !strings.Contains(err.Error(), "--profile") {
		t.Fatalf("--all=false without a profile: %v", err)
	}
	// The test process may itself count as an agent's; either way the gate must not ask for a profile.
	if out, err := runGateCLIOn(dbPath, "--json", "hil", "pending", "--all"); err != nil && strings.Contains(out+err.Error(), "acts on a profile") {
		t.Fatalf("--all: the gate asked for a profile: %v", err)
	}
	if _, err := runGateCLIOn(dbPath, "--profile", "default", "hil", "pending", "--all"); err == nil || !strings.Contains(err.Error(), "leave out") {
		t.Fatalf("both flags: %v", err)
	}
}
