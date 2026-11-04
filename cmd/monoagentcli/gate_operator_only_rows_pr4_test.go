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
// deleting a document's file (documents remove --delete-file), approving a person for outreach and sending a
// draft. Every row's args would fail the verb's next check (an unknown profile, a blank folder, a missing
// confirmation), so the gate has to come first for a refused caller to be told it is refused, and the
// operator's pass reaches no profile, folder or message.
//
// Recovery verb: `profile delete` only (plan Q5). It clears an install, so the damaged-database rule applies;
// the others do not repair anything: purging backups destroys the copies a repair would use, and the rest
// change what an agent or a workflow may reach or send.
func operatorOnlyRowsPR4() []opRow {
	return []opRow{
		{path: "profile update", build: newProfileCmd, args: []string{"update", "no-such-profile"}},
		{path: "profile delete", build: newProfileCmd, args: []string{"delete", "no-such-profile"}, recovery: true},
		{path: "profile backups purge", build: newProfileCmd, args: []string{"backups", "purge"}},
		{path: "profile move", build: newProfileCmd, args: []string{"move", "no-such-profile", " "}},
		{path: "profile create", build: newProfileCmd, args: []string{"create", " ", "--root-dir", "relative"}},
		{path: "profile documents remove", build: newProfileCmd, args: []string{"documents", "remove", "no-such-doc", "--delete-file"}},
		{path: "people review approve", build: newPeopleCmd, args: []string{"review", "approve", "no-such-person"}},
		{path: "people messages send-draft", build: newPeopleCmd, args: []string{"messages", "send-draft", "no-such-message"}},
	}
}

const readOnlyListing = "read-only or shows what exists"

// openInTheProfileAndPeopleChange are the leaves next to a converted verb that stay open on purpose.
var openInTheProfileAndPeopleChange = map[string]string{
	"people review reject":         "only takes a person out of the outreach queue; nothing is sent, and the GUI and agents both reject",
	"people review list":           readOnlyListing,
	"profile folder":               "prints the folder (and creates its missing layout inside the folder the profile already has); it chooses nothing",
	"profile documents rm":         "removes the document from MonoAgent and never deletes a file; only --delete-file of `documents remove` does",
	"profile backups list":         readOnlyListing,
	"people messages reject-draft": "discards a draft; nothing is sent",
}

// convertedProfileAndPeopleVerbs are the paths PR 4 converted. Deleting a row (or converting a verb and
// forgetting its row) fails the guard.
var convertedProfileAndPeopleVerbs = []string{
	"profile update", "profile delete", "profile backups purge", "profile move", "profile create",
	"profile documents remove", "people review approve", "people messages send-draft",
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

func TestOnlyProfileDeleteIsARecoveryVerbOfTheProfileAndPeopleChange(t *testing.T) {
	var recovery []string
	for _, r := range operatorOnlyRowsPR4() {
		if r.recovery {
			recovery = append(recovery, r.path)
		}
	}
	if strings.Join(recovery, ",") != "profile delete" {
		t.Fatalf("recovery verbs of the profile and people change: %v, want profile delete only", recovery)
	}
}

// `profile delete` is the recovery verb of this change. Operator on a database that cannot be read: the gate
// lets the operator through (the delete then fails on the database, which is the damage), while the sibling
// `profile update` is refused because the process cannot be placed. A marked turn on a readable database is
// refused by both.
func TestProfileDeleteOnAnUnreadableDatabaseAndInATurn(t *testing.T) {
	del := opRow{path: "profile delete", build: newProfileCmd, args: []string{"delete", "no-such-profile"}, recovery: true}
	update := opRow{path: "profile update", build: newProfileCmd, args: []string{"update", "no-such-profile"}}

	cfg := opEnv(t)
	cfg.DBPath = filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(cfg.DBPath, []byte("this is not a database, and it is long enough to be read as a header, or not"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := del.run(t, cfg); isOperatorOnly(err) {
		t.Fatalf("delete locked the operator out of an unreadable database: %v", err)
	}
	err := update.run(t, cfg)
	requireOperatorOnly(t, err)
	if !strings.Contains(err.Error(), "cannot tell whether a coder session's turn runs this command") {
		t.Fatalf("update on an unreadable database: %v", err)
	}

	needAncestry(t)
	cfg = opEnv(t)
	markThisProcessAsACoderTurn(t, cfg)
	for _, r := range []opRow{del, update} {
		err := r.run(t, cfg)
		requireOperatorOnly(t, err)
		if !strings.Contains(err.Error(), "coder session's turn") {
			t.Fatalf("%s in a recorded turn: %v", r.path, err)
		}
	}
	if opgate.RecoveryRefusal() == "" {
		t.Fatal("the recovery gate let a process inside a recorded turn through")
	}
}
