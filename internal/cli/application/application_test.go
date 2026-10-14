package application

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/testdb"
)

func TestApplicationAddListGetStatusTag(t *testing.T) {
	t.Parallel()
	dbPath := testdb.Path(t)

	addOut, err := run(t, dbPath, true, "add", "--kind", "job", "--title", "Backend Engineer", "--company", "Acme", "--url", "https://acme.example/1")
	if err != nil {
		t.Fatalf("application add: %v (%s)", err, addOut)
	}
	if !strings.Contains(addOut, `"id"`) {
		t.Fatalf("expected JSON id in add output, got: %s", addOut)
	}
	// Extract id crudely (tests below just need list/get to find something).
	var id string
	for _, tok := range strings.Split(addOut, `"`) {
		if len(tok) == 36 && strings.Count(tok, "-") == 4 {
			id = tok
			break
		}
	}
	if id == "" {
		t.Fatalf("could not extract id from add output: %s", addOut)
	}

	listOut, err := run(t, dbPath, true, "list")
	if err != nil {
		t.Fatalf("application list: %v", err)
	}
	if !strings.Contains(listOut, "Acme") {
		t.Fatalf("expected list output to contain company, got: %s", listOut)
	}

	getOut, err := run(t, dbPath, true, "get", id)
	if err != nil {
		t.Fatalf("application get: %v (%s)", err, getOut)
	}
	if !strings.Contains(getOut, "pending") {
		t.Fatalf("expected get output to show pending status, got: %s", getOut)
	}

	// Uses "cancelled" (not "applied") here: pending->applied via the
	// generic `status set` command is the exact bug under test in
	// TestApplicationStatusRejectsAppliedStatus below -- this scenario only
	// cares about a valid, non-"applied" transition plus tagging.
	statusOut, err := run(t, dbPath, true, "status", id, "set", "cancelled")
	if err != nil {
		t.Fatalf("application status set: %v (%s)", err, statusOut)
	}

	tagOut, err := run(t, dbPath, true, "tag", id, "add", "urgent")
	if err != nil {
		t.Fatalf("application tag add: %v (%s)", err, tagOut)
	}

	getOut2, err := run(t, dbPath, true, "get", id)
	if err != nil {
		t.Fatalf("application get (after status/tag): %v", err)
	}
	if !strings.Contains(getOut2, "cancelled") || !strings.Contains(getOut2, "urgent") {
		t.Fatalf("expected updated status+tag in get output, got: %s", getOut2)
	}
}

// TestApplicationStatusRejectsAppliedStatus covers the invariant that
// automation/generic status changes must never be able to mark an
// application "applied" -- only `application send` (an explicit,
// human-triggered action) may do that. Regression test for a bypass where
// `application status <id> set applied` shelled straight through to
// store.SetStatus with no restriction on the target status.
func TestApplicationStatusRejectsAppliedStatus(t *testing.T) {
	t.Parallel()
	dbPath := testdb.Path(t)
	addOut, err := run(t, dbPath, true, "add", "--kind", "job", "--title", "Backend Engineer", "--company", "Acme", "--url", "https://acme.example/1")
	if err != nil {
		t.Fatalf("application add: %v (%s)", err, addOut)
	}
	var id string
	for _, tok := range strings.Split(addOut, `"`) {
		if len(tok) == 36 && strings.Count(tok, "-") == 4 {
			id = tok
			break
		}
	}
	if id == "" {
		t.Fatalf("could not extract id from add output: %s", addOut)
	}

	if _, err := run(t, dbPath, true, "status", id, "set", "applied"); err == nil {
		t.Fatal("expected error setting status to \"applied\" via the generic status command, got nil")
	}
	// Case-insensitive variants must be rejected too.
	if _, err := run(t, dbPath, true, "status", id, "set", "Applied"); err == nil {
		t.Fatal("expected error setting status to \"Applied\" via the generic status command, got nil")
	}

	getOut, err := run(t, dbPath, true, "get", id)
	if err != nil {
		t.Fatalf("application get: %v (%s)", err, getOut)
	}
	if !strings.Contains(getOut, "pending") {
		t.Fatalf("expected application to remain pending after rejected \"applied\" transition, got: %s", getOut)
	}
	if strings.Contains(getOut, "\"applied\"") {
		t.Fatalf("application must not have been marked applied via generic status command, got: %s", getOut)
	}
}

// TestApplicationListTableShowsJobTitle exercises the plain-text table
// rendering path (JSONOutput: false) of `application list` and asserts the
// TITLE column shows the job's actual title, not its company name.
func TestApplicationListTableShowsJobTitle(t *testing.T) {
	t.Parallel()
	dbPath := testdb.Path(t)

	runCmd := func(jsonOutput bool, args ...string) (string, error) {
		return run(t, dbPath, jsonOutput, args...)
	}

	addOut, err := runCmd(true, "add", "--kind", "job", "--title", "Backend Engineer", "--company", "Acme", "--url", "https://acme.example/1")
	if err != nil {
		t.Fatalf("application add: %v (%s)", err, addOut)
	}

	listOut, err := runCmd(false, "list")
	if err != nil {
		t.Fatalf("application list: %v", err)
	}
	if !strings.Contains(listOut, "Backend Engineer") {
		t.Fatalf("expected list table TITLE column to show the job title, got: %s", listOut)
	}
	if strings.Contains(listOut, "Acme") {
		t.Fatalf("did not expect the company name in the list table output, got: %s", listOut)
	}
}

// TestApplicationListTableTruncatesLongValues guards against the tablewriter
// v1 migration regression where `application list`'s TITLE/TAGS columns lost
// their old wrap-based readability (v0.0.5 wrapped long cells; the v1
// migration's shared newPlainTable helper (Deps.NewTable) hardcodes no-wrap for every
// caller) without gaining truncation to compensate, unlike sibling commands
// that already call truncateStr (truncate here). A long title/tag must render truncated
// ("...") rather than as one long unbroken line.
func TestApplicationListTableTruncatesLongValues(t *testing.T) {
	t.Parallel()
	dbPath := testdb.Path(t)

	runCmd := func(jsonOutput bool, args ...string) (string, error) {
		return run(t, dbPath, jsonOutput, args...)
	}

	longTitle := "Senior Distinguished Principal Staff Software Engineer, Platform Infrastructure and Reliability Team"
	addOut, err := runCmd(true, "add", "--kind", "job", "--title", longTitle, "--company", "Acme", "--url", "https://acme.example/2")
	if err != nil {
		t.Fatalf("application add: %v (%s)", err, addOut)
	}

	listOut, err := runCmd(false, "list")
	if err != nil {
		t.Fatalf("application list: %v", err)
	}
	if strings.Contains(listOut, longTitle) {
		t.Fatalf("expected the long title to be truncated in table output, got the full string unbroken: %s", listOut)
	}
	if !strings.Contains(listOut, "...") {
		t.Fatalf("expected a truncation marker in table output, got: %s", listOut)
	}
}

func TestApplicationStatusRejectsInvalidTransition(t *testing.T) {
	t.Parallel()
	dbPath := testdb.Path(t)
	addOut, err := run(t, dbPath, true, "add", "--kind", "job", "--title", "Backend Engineer", "--company", "Acme", "--url", "https://acme.example/1")
	if err != nil {
		t.Fatalf("application add: %v (%s)", err, addOut)
	}
	var id string
	for _, tok := range strings.Split(addOut, `"`) {
		if len(tok) == 36 && strings.Count(tok, "-") == 4 {
			id = tok
			break
		}
	}
	if _, err := run(t, dbPath, true, "status", id, "set", "rejected"); err == nil {
		t.Fatal("expected error for pending->rejected, got nil")
	}
}

func TestApplicationGetNotFound(t *testing.T) {
	t.Parallel()
	dbPath := testdb.Path(t)
	if _, err := run(t, dbPath, true, "get", "does-not-exist"); err == nil {
		t.Fatal("expected error for unknown id, got nil")
	}
}

// The CLI's exit codes: 2 for an unknown application, 3 for invalid input.
func TestApplicationExitCodes(t *testing.T) {
	t.Parallel()
	dbPath := testdb.Path(t)
	addOut, err := run(t, dbPath, true, "add", "--kind", "job", "--title", "Backend Engineer", "--company", "Acme", "--url", "https://acme.example/1")
	if err != nil {
		t.Fatalf("application add: %v (%s)", err, addOut)
	}
	var id string
	for _, tok := range strings.Split(addOut, `"`) {
		if len(tok) == 36 && strings.Count(tok, "-") == 4 {
			id = tok
			break
		}
	}
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"get", "does-not-exist"}, 2},
		{[]string{"status", "does-not-exist", "set", "cancelled"}, 2},
		{[]string{"tag", "does-not-exist", "add", "x"}, 2},
		{[]string{"add", "--kind", "gig", "--title", "x", "--url", "u"}, 3},
		{[]string{"status", id, "put", "cancelled"}, 3},
		{[]string{"status", id, "set", "APPLIED"}, 3},
		{[]string{"status", id, "set", "rejected"}, 3},
		{[]string{"tag", id, "toggle", "x"}, 3},
		{[]string{"apply", id, "--mode", "later"}, 3},
		{[]string{"discover"}, 3},
		{[]string{"discover", "--keywords", "go", "--source", "nowhere"}, 3},
	} {
		if _, err := run(t, dbPath, true, c.args...); code(err) != c.want {
			t.Errorf("%v: exit %d (%v), want %d", c.args, code(err), err, c.want)
		}
	}
}
