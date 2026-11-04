package profile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// A process inside a coder turn carries no marker, so only the turn-aware gate (opgate) can refuse it: the
// environment here is the user's own, and the gate says "in a turn".
func inATurn(t *testing.T, ordinary, recovery string) {
	t.Helper()
	opgate.SetForTest(t, func(r bool) string {
		if r {
			return recovery
		}
		return ordinary
	})
}

func requireRefusedByTheGate(t *testing.T, what string, err error, why string) {
	t.Helper()
	var coded interface{ JSONErrorFields() map[string]any }
	if err == nil || !errors.As(err, &coded) || coded.JSONErrorFields()["code"] != "operator_only" || exitCodeOf(t, err) != 3 || !strings.Contains(err.Error(), why) {
		t.Fatalf("%s: %v, want an operator_only refusal naming %q", what, err, why)
	}
}

func TestProfileVerbsAskTheTurnAwareGate(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	mustProfileJSON(t, dbPath, &profileCreateResult{}, "create", "Mine")
	var before []profileJSON
	mustProfileJSON(t, dbPath, &before, "list")
	inATurn(t, "in a coder turn", "in a coder turn")

	for _, args := range [][]string{
		{"update", "Mine", "--name", "Renamed"},
		{"backups", "purge", "--yes"},
		{"move", "Mine", filepath.Join(t.TempDir(), "elsewhere")},
		{"create", "evil", "--root-dir", t.TempDir()},
		{"delete", "Mine", "--yes-delete-profile", "Mine"},
	} {
		_, err := runProfileJSON(t, dbPath, args...)
		requireRefusedByTheGate(t, strings.Join(args, " "), err, "in a coder turn")
	}
	var list []profileJSON
	mustProfileJSON(t, dbPath, &list, "list")
	if !reflect.DeepEqual(list, before) {
		t.Fatalf("a refused verb changed the profiles: %+v, was %+v", list, before)
	}

	// Reads and the dry runs stay open to the same process.
	for _, args := range [][]string{{"list"}, {"delete", "Mine", "--summary"}, {"move", "Mine", filepath.Join(t.TempDir(), "x"), "--check"}, {"create", "Plain"}} {
		if _, err := runProfileJSON(t, dbPath, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

// Every verb asks the ordinary gate, `profile delete` included: it is not a recovery verb, so a recovery
// gate that lets the operator through changes nothing for it.
func TestProfileDeleteAsksTheOrdinaryGateNotTheRecoveryOne(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	mustProfileJSON(t, dbPath, &profileCreateResult{}, "create", "Mine")

	inATurn(t, "ordinary gate", "")
	for _, args := range [][]string{{"delete", "Mine"}, {"update", "Mine", "--name", "x"}, {"backups", "purge", "--yes"}} {
		_, err := runProfileJSON(t, dbPath, args...)
		requireRefusedByTheGate(t, strings.Join(args, " "), err, "ordinary gate")
	}

	inATurn(t, "", "recovery gate")
	_, err := runProfileJSON(t, dbPath, "delete", "Mine")
	if err == nil || strings.Contains(err.Error(), "recovery gate") || strings.Contains(err.Error(), "only the user") {
		t.Fatalf("delete with an ordinary gate that lets the operator through: %v", err)
	}
}

// `documents remove --delete-file` deletes a file from the profile folder: a process inside a turn is
// refused and both the file and the document stay; without the flag the same process removes the
// document and the file stays.
func TestDocumentsRemoveDeleteFileAsksTheTurnAwareGate(t *testing.T) {
	cfg, root := discoveredFixture(t)
	ids := docIDs(t, cfg)
	file := filepath.Join(root, "career-profile.md")
	inATurn(t, "in a coder turn", "in a coder turn")

	_, err := runDocs(t, cfg, "remove", ids["career-profile.md"], "--delete-file")
	requireRefusedByTheGate(t, "remove --delete-file", err, "in a coder turn")
	if _, statErr := os.Stat(file); statErr != nil {
		t.Fatalf("a refused caller deleted the file: %v", statErr)
	}
	if got := docIDs(t, cfg); got["career-profile.md"] == "" || len(got) != len(ids) {
		t.Fatalf("a refused caller removed a document: %v", got)
	}

	if out, err := runDocs(t, cfg, "remove", ids["career-profile.md"]); err != nil {
		t.Fatalf("remove without the flag: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(file); statErr != nil {
		t.Fatalf("remove without --delete-file deleted the file: %v", statErr)
	}
	if got := docIDs(t, cfg); got["career-profile.md"] != "" {
		t.Fatalf("the document is still indexed: %v", got)
	}
}
