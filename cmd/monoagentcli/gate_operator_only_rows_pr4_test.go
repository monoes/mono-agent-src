package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// operatorOnlyRowsPR4 are the profile and people verbs converted in the fourth change of issue 1333: renaming
// or re-iconing a profile, deleting it, purging its backups, choosing its folder (move, create --root-dir),
// deleting a document's file (documents remove --delete-file), approving or rejecting a person, and sending or
// discarding a draft. Every row's args would fail the verb's next check (an unknown profile, a blank folder, a missing
// confirmation), so the gate has to come first for a refused caller to be told it is refused, and the
// operator's pass reaches no profile, folder or message.
//
// No verb of this change is a recovery verb (ruling of 2026-11-04): `profile delete` removes a profile and
// its data, it repairs nothing, so a database that cannot be read refuses it like the rest.
func operatorOnlyRowsPR4() []opRow {
	return []opRow{
		{path: "profile update", build: newProfileCmd, args: []string{"update", "no-such-profile"}},
		{path: "profile delete", build: newProfileCmd, args: []string{"delete", "no-such-profile"}},
		{path: "profile backups purge", build: newProfileCmd, args: []string{"backups", "purge"}},
		{path: "profile move", build: newProfileCmd, args: []string{"move", "no-such-profile", " "}},
		{path: "profile create", build: newProfileCmd, args: []string{"create", " ", "--root-dir", "relative"}},
		{path: "profile documents remove", build: newProfileCmd, args: []string{"documents", "remove", "no-such-doc", "--delete-file"}},
		{path: "people review approve", build: newPeopleCmd, args: []string{"review", "approve", "no-such-person", "--send", "--send-plan"}},
		{path: "people review reject", build: newPeopleCmd, args: []string{"review", "reject", "no-such-person"}},
		{path: "people messages send-draft", build: newPeopleCmd, args: []string{"messages", "send-draft", "no-such-message"}},
		{path: "people messages reject-draft", build: newPeopleCmd, args: []string{"messages", "reject-draft", "no-such-message"}},
	}
}

const readOnlyListing = "read-only or shows what exists"

// openInTheProfileAndPeopleChange are the leaves next to a converted verb that stay open on purpose.
var openInTheProfileAndPeopleChange = map[string]string{
	"people review reject": "only takes a person out of the outreach queue; nothing is sent, and the GUI and agents both reject",
	"people review list":   readOnlyListing,
	"profile folder":       "prints the folder (and creates its missing layout inside the folder the profile already has); it chooses nothing",
	"profile documents rm": "removes the document from MonoAgent and never deletes a file; only --delete-file of `documents remove` does",
	"profile backups list": readOnlyListing,
}

// convertedProfileAndPeopleVerbs are the paths PR 4 converted. Deleting a row (or converting a verb and
// forgetting its row) fails the guard.
var convertedProfileAndPeopleVerbs = []string{
	"profile update", "profile delete", "profile backups purge", "profile move", "profile create",
	"profile documents remove", "people review approve", "people review reject", "people messages send-draft",
	"people messages reject-draft",
}

func TestEveryConvertedProfileAndPeopleVerbHasARow(t *testing.T) {
	rows := map[string]bool{}
	for _, r := range operatorOnlyRows() {
		rows[r.path] = true
	}
	all, _ := treeKeys(t)
	var bad []string
	for _, k := range convertedProfileAndPeopleVerbs {
		if !rows[k] {
			bad = append(bad, k+": converted in PR 4, but not a row of operatorOnlyRows")
		}
		if !all[k] {
			bad = append(bad, k+": not a command of the tree")
		}
	}
	for _, r := range operatorOnlyRowsPR4() {
		found := false
		for _, k := range convertedProfileAndPeopleVerbs {
			found = found || k == r.path
		}
		if !found {
			bad = append(bad, r.path+": a PR 4 row that convertedProfileAndPeopleVerbs does not list")
		}
	}
	for k, why := range openInTheProfileAndPeopleChange {
		if !all[k] || rows[k] || why == "" {
			bad = append(bad, k+": an open verb must be a command of the tree, not a row, and carry a reason")
		}
	}
	if len(bad) > 0 {
		t.Fatalf("the profile and people verbs and the table disagree:\n%s", strings.Join(bad, "\n"))
	}
}

func TestNoVerbOfTheProfileAndPeopleChangeIsARecoveryVerb(t *testing.T) {
	for _, r := range operatorOnlyRowsPR4() {
		if r.recovery {
			t.Errorf("%s is marked as a recovery verb", r.path)
		}
	}
}

// `profile delete` is not a recovery verb: on a database that cannot be read the operator is refused (the
// process cannot be placed), exactly like `profile update`.
func TestProfileDeleteOnAnUnreadableDatabaseIsRefusedLikeTheOthers(t *testing.T) {
	cfg := opEnv(t)
	cfg.DBPath = filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(cfg.DBPath, []byte("this is not a database, and it is long enough to be read as a header, or not"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, r := range operatorOnlyRowsPR4() {
		if r.path != "profile delete" && r.path != "profile update" {
			continue
		}
		err := r.run(t, cfg)
		requireOperatorOnly(t, err)
		if !strings.Contains(err.Error(), "cannot tell whether a coder session's turn runs this command") {
			t.Fatalf("%s on an unreadable database: %v", r.path, err)
		}
	}
	if opgate.Refusal() == "" {
		t.Fatal("the ordinary gate let a process through on an unreadable database")
	}
}

// The dry runs of two converted verbs stay open to a process the gate refuses: they change and delete
// nothing. Inside a recorded turn, as a token holder and in the agent environments they get as far as the
// profile lookup (not found), never an operator_only refusal.
var openDryRunsOfTheProfileAndPeopleChange = []opRow{
	{path: "profile move --check", build: newProfileCmd, args: []string{"move", "no-such-profile", "{tmp}", "--check"}},
	{path: "profile delete --summary", build: newProfileCmd, args: []string{"delete", "no-such-profile", "--summary"}},
}

func TestTheDryRunsOfProfileMoveAndDeleteStayOpenToARefusedProcess(t *testing.T) {
	needAncestry(t)
	for _, r := range openDryRunsOfTheProfileAndPeopleChange {
		t.Run(r.path+"/turn", func(t *testing.T) {
			cfg := opEnv(t)
			markThisProcessAsACoderTurn(t, cfg)
			if err := r.run(t, cfg); err == nil || isOperatorOnly(err) {
				t.Fatalf("%v: want the profile not found, not a refusal", err)
			}
		})
		for ctxName, setup := range gateOSMenuContexts() {
			t.Run(r.path+"/"+ctxName, func(t *testing.T) {
				cfg := opEnv(t)
				setup(t)
				if err := r.run(t, cfg); err == nil || isOperatorOnly(err) {
					t.Fatalf("%v: want the profile not found, not a refusal", err)
				}
			})
		}
	}
}
