package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Inside a coder session's turn the profile refusal says that the session token
// does not choose the profile (#1765); elsewhere it says nothing about it.
func TestProfileRefusalInASessionTurnSaysTheTokenNamesNoProfile(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "gate.db")
	if _, err := runGateCLIOn(dbPath, "--profile", "default", "people", "list"); err != nil {
		t.Fatalf("seeding the database: %v", err)
	}
	const hint = "session token does not choose the profile"
	for _, tc := range []struct {
		token string
		want  bool
	}{{"", false}, {coderhost.SessionTokenPrefix + "not-a-real-token", true}} {
		t.Setenv(coderhost.SessionTokenEnv, tc.token)
		_, err := runGateCLIOn(dbPath, "coder", "sessions", "status")
		if err == nil || !strings.Contains(err.Error(), "acts on a profile") {
			t.Fatalf("token %q: got %v, want the gate's refusal", tc.token, err)
		}
		if got := strings.Contains(err.Error(), hint); got != tc.want {
			t.Errorf("token %q: hint = %v, want %v: %v", tc.token, got, tc.want, err)
		}
		if tc.token != "" && strings.Contains(err.Error(), tc.token) {
			t.Errorf("the refusal prints the token: %v", err)
		}
	}
}
