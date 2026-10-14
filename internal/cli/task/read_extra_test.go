package task

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTaskListTextIsATableWithTheProfileTheAgeAndTheHolder(t *testing.T) {
	db := newTaskTestDB(t)
	zone := localNoonZone(t)
	addTaskProfile(t, db, "work-id", "Work")
	now := time.Now()
	ids := seedTaskRows(t, db,
		taskSeed{profile: "work-id", title: "Draft the report"},
		taskSeed{profile: "work-id", title: "Pay the invoice", status: "ready", source: "os", created: now.Add(-73 * time.Hour)},
		taskSeed{profile: "work-id", title: "Fix the build", status: "in_progress", holder: "bot", until: now.Add(time.Hour), created: now.Add(-5*time.Hour - 10*time.Minute)},
		taskSeed{profile: "work-id", title: "Old claim", status: "in_progress", holder: "gone", until: now.Add(-time.Hour), created: now.Add(-12*time.Minute - 10*time.Second)},
		taskSeed{profile: "work-id", title: "Next day", status: "in_progress", holder: "late", until: now.Add(30 * time.Hour)},
		taskSeed{profile: "work-id", title: "Check the results", status: "review"},
		taskSeed{profile: "work-id", title: "Shipped", status: "done", created: now.Add(-49 * time.Hour)},
	)
	out, _, err := runTask(t, db, "Work", false, "", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 9 || lines[0] != "Profile: Work" {
		t.Fatalf("want the profile line, a header and seven rows, got %d lines:\n%s", len(lines), out)
	}
	if !regexp.MustCompile(`^ID\s+STATUS\s+TITLE\s+SOURCE\s+AGE\s+HELD BY$`).MatchString(lines[1]) {
		t.Errorf("the header: %q", lines[1])
	}
	until := now.Add(time.Hour).In(zone).Format("15:04")
	nextDay := now.Add(30 * time.Hour).In(zone).Format("01-02 15:04")
	for i, want := range []struct{ status, title, source, age, held string }{
		{"inbox", "Draft the report", "cli", "now", ""},
		{"ready", "Pay the invoice", "os", "3d", ""},
		{"in_progress", "Fix the build", "cli", "5h", "bot until " + until},
		{"in_progress", "Old claim", "cli", "12m", "stale: gone"},
		{"in_progress", "Next day", "cli", "now", "late until " + nextDay},
		{"review", "Check the results", "cli", "now", ""},
		{"done", "Shipped", "cli", "2d", ""},
	} {
		tail := `\s*`
		if want.held != "" {
			tail = `\s{2,}` + regexp.QuoteMeta(want.held) + `\s*`
		}
		row := fmt.Sprintf(`^#%d\s{2,}%s\s{2,}%s\s{2,}%s\s{2,}%s%s$`, ids[i], want.status, regexp.QuoteMeta(want.title), want.source, want.age, tail)
		if !regexp.MustCompile(row).MatchString(lines[2+i]) {
			t.Errorf("row %d is %q, want the pattern %s", i+1, lines[2+i], row)
		}
	}
}

func TestTaskBoardTextListsTheFiveColumnsInOrderWithTheirCards(t *testing.T) {
	db := newTaskTestDB(t)
	zone := localNoonZone(t)
	addTaskProfile(t, db, "work-id", "Work")
	now := time.Now()
	ids := seedTaskRows(t, db,
		taskSeed{profile: "work-id", title: "Idea one"},
		taskSeed{profile: "work-id", title: "Next up", status: "ready"},
		taskSeed{profile: "work-id", title: "After that", status: "ready"},
		taskSeed{profile: "work-id", title: "Fix the build", status: "in_progress", holder: "bot", until: now.Add(time.Hour)},
		taskSeed{profile: "work-id", title: "Old claim", status: "in_progress", holder: "gone", until: now.Add(-time.Hour)},
		taskSeed{profile: "work-id", title: "Check the results", status: "review"},
		taskSeed{profile: "work-id", title: "Shipped", status: "done"},
		taskSeed{profile: "work-id", title: "Put away", status: "archived"},
	)
	// Two writes through the store: the revision of a board that rows were written into around the store is 0.
	var fromCLI, readyFromCLI addedJSON
	mustTaskJSON(t, db, "Work", &fromCLI, "", "add", "from the cli")
	mustTaskJSON(t, db, "Work", &readyFromCLI, "", "add", "ready from the cli", "--ready")

	out, _, err := runTask(t, db, "Work", false, "", "board")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`Profile: Work (revision 2)

INBOX (2)
  #%d  from the cli
  #%d  Idea one

READY (3)
  #%d  Next up
  #%d  After that
  #%d  ready from the cli

IN PROGRESS (2)
  #%d  Fix the build  [bot until %s]
  #%d  Old claim  [stale: gone]

REVIEW (1)
  #%d  Check the results

DONE (1)
  #%d  Shipped
`, fromCLI.Task.ID, ids[0], ids[1], ids[2], readyFromCLI.Task.ID, ids[3], now.Add(time.Hour).In(zone).Format("15:04"), ids[4], ids[5], ids[6])
	if out != want {
		t.Errorf("the board as text:\n%s\nwant:\n%s", out, want)
	}
	empty, _, err := runTask(t, db, "default", false, "", "board")
	wantEmpty := "Profile: Default (revision 0)\n\nINBOX (0)\n\nREADY (0)\n\nIN PROGRESS (0)\n\nREVIEW (0)\n\nDONE (0)\n"
	if err != nil || empty != wantEmpty {
		t.Errorf("an empty board as text: %q, %v; want %q", empty, err, wantEmpty)
	}
}

func TestTaskBoardJSONHasTheFiveColumnsTheCountsAndNoArchive(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	seedTaskRows(t, db,
		taskSeed{title: "held", status: "in_progress", holder: "gone", until: time.Now().Add(-time.Hour)},
		taskSeed{title: "put away", status: "archived"},
	)
	out, _, err := runTask(t, db, "default", true, "", "board")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	// hasKeys reports whether the object has exactly these keys.
	hasKeys := func(raw json.RawMessage, want ...string) bool {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(m))
		for k := range m {
			got = append(got, k)
		}
		slices.Sort(got)
		slices.Sort(want)
		return slices.Equal(got, want)
	}
	if !hasKeys([]byte(out), "profile", "rev", "counts", "tasks") {
		t.Errorf("the board document: %s", out)
	}
	if !hasKeys(doc["counts"], "inbox", "ready", "in_progress", "review", "done", "stale") {
		t.Errorf("the counts: %s", doc["counts"])
	}
	if !hasKeys(doc["tasks"], "inbox", "ready", "in_progress", "review", "done") {
		t.Errorf("the columns are %s: the five, and never the archive", doc["tasks"])
	}
	var counts struct {
		InProgress int `json:"in_progress"`
		Stale      int `json:"stale"`
	}
	if err := json.Unmarshal(doc["counts"], &counts); err != nil || counts.InProgress != 1 || counts.Stale != 1 {
		t.Errorf("counts %+v, %v: one card in progress, and its claim is stale", counts, err)
	}
}

// The profile is asked for by its name and the hint under the cut column names it by its id:
// the hint is a command to paste, and the id is what a command can rely on.
func TestTaskBoardCutsTheDoneColumnButCountsEveryCard(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	addTaskProfile(t, db, "work-id", "Work")
	rows := make([]taskSeed, 0, 55)
	for i := 1; i <= 55; i++ {
		rows = append(rows, taskSeed{profile: "work-id", title: fmt.Sprintf("done %02d", i), status: "done"})
	}
	seedTaskRows(t, db, rows...)
	for _, c := range []struct {
		args  []string
		shown int
		more  string // the line that says what was left out, "" for none
	}{
		{[]string{"board"}, 50, "  ... 5 more (monoagentcli --profile work-id task list --status done)"},
		{[]string{"board", "--done-limit", "3"}, 3, "  ... 52 more (monoagentcli --profile work-id task list --status done)"},
		{[]string{"board", "--done-limit", "54"}, 54, "  ... 1 more (monoagentcli --profile work-id task list --status done)"},
		{[]string{"board", "--done-limit", "55"}, 55, ""},
		{[]string{"board", "--done-limit", "0"}, 55, ""},
	} {
		var board struct {
			Counts map[string]int        `json:"counts"`
			Tasks  map[string][]taskJSON `json:"tasks"`
		}
		mustTaskJSON(t, db, "Work", &board, "", c.args...)
		if got := len(board.Tasks["done"]); got != c.shown || board.Counts["done"] != 55 {
			t.Errorf("task %s: %d Done cards, counted %d; want %d shown of 55", strings.Join(c.args, " "), got, board.Counts["done"], c.shown)
		}
		text, _, err := runTask(t, db, "Work", false, "", c.args...)
		after := text[strings.Index(text, "DONE (55)")+len("DONE (55)"):]
		if err != nil || !strings.Contains(text, "DONE (55)") || strings.Count(after, "\n  #") != c.shown {
			t.Errorf("task %s as text: %v, %d Done lines, want the heading DONE (55) and %d lines", strings.Join(c.args, " "), err, strings.Count(after, "\n  #"), c.shown)
		}
		// The cut is said in a line of its own after the cards, and only when something was cut.
		var said []string
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "...") {
				said = append(said, line)
			}
		}
		wantSaid := []string(nil)
		if c.more != "" {
			wantSaid = []string{c.more}
		}
		if !slices.Equal(said, wantSaid) || (c.more != "" && !strings.HasSuffix(text, c.more+"\n")) {
			t.Errorf("task %s as text says %q, want %q as its last line", strings.Join(c.args, " "), said, wantSaid)
		}
	}
	var board struct {
		Tasks map[string][]taskJSON `json:"tasks"`
	}
	mustTaskJSON(t, db, "Work", &board, "", "board")
	if done := board.Tasks["done"]; len(done) != 50 || done[0].Title != "done 01" || done[49].Title != "done 50" {
		t.Errorf("the cut keeps the top of the column: %d cards, not done 01 to done 50", len(done))
	}
}

func TestTaskListFlagsNarrowTheList(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	now := time.Now()
	seedTaskRows(t, db,
		taskSeed{title: "ready one", status: "ready"},
		taskSeed{title: "inbox one"},
		taskSeed{title: "live bot", status: "in_progress", holder: "bot", until: now.Add(time.Hour)},
		taskSeed{title: "stale bot", status: "in_progress", holder: "bot", until: now.Add(-time.Hour)},
		taskSeed{title: "stale other", status: "in_progress", holder: "other", until: now.Add(-2 * time.Hour)},
		taskSeed{title: "review one", status: "review"},
		taskSeed{title: "chrome one", source: "chrome"},
	)
	for _, c := range []struct {
		name string
		args []string
		want []string
	}{
		{"--claimed-by", []string{"--claimed-by", "bot"}, []string{"live bot", "stale bot"}},
		{"--stale", []string{"--stale"}, []string{"stale bot", "stale other"}},
		{"--claimed-by and --stale", []string{"--claimed-by", "bot", "--stale"}, []string{"stale bot"}},
		{"--status with a list", []string{"--status", "in_progress,review"}, []string{"live bot", "stale bot", "stale other", "review one"}},
		{"--status by another spelling", []string{"--status", "In-Progress"}, []string{"live bot", "stale bot", "stale other"}},
		{"--source", []string{"--source", "chrome"}, []string{"chrome one"}},
		{"--status and --source", []string{"--status", "inbox", "--source", "chrome"}, []string{"chrome one"}},
		{"--limit keeps the top of the order", []string{"--limit", "2"}, []string{"inbox one", "chrome one"}},
		{"--status archived is empty here", []string{"--status", "archived"}, []string{}},
	} {
		var got listJSON
		mustTaskJSON(t, db, "default", &got, "", append([]string{"list"}, c.args...)...)
		if !slices.Equal(titlesOf(got), c.want) {
			t.Errorf("list %s: %q, want %q", c.name, titlesOf(got), c.want)
		}
	}
	if doc := failedTaskJSON(t, db, "default", 3, "list", "--source", "pigeon"); doc["code"] != "invalid_input" {
		t.Errorf("an unknown source: %v", doc)
	}
}

// The store decides what is stale and the commands print its verdict: a lease
// that ended a while ago, one that ends this very second, and one still to run.
func TestTaskStaleClaimsAreMarkedInEveryView(t *testing.T) {
	db := newTaskTestDB(t)
	zone := localNoonZone(t)
	now := time.Now()
	ids := seedTaskRows(t, db,
		taskSeed{title: "live", status: "in_progress", holder: "bot", until: now.Add(time.Hour)},
		taskSeed{title: "long gone", status: "in_progress", holder: "gone", until: now.Add(-time.Hour)},
		taskSeed{title: "this second", status: "in_progress", holder: "edge", until: now},
	)
	var held heldJSON
	mustTaskJSON(t, db, "default", &held, "", "list")
	if len(held.Tasks) != 3 {
		t.Fatalf("the list has %d tasks, want the 3 that are held: %+v", len(held.Tasks), held.Tasks)
	}
	for i, want := range []struct {
		by    string
		stale bool
	}{{"bot", false}, {"gone", true}, {"edge", true}} {
		if c := held.Tasks[i].Claim; c == nil || c.By != want.by || c.Stale != want.stale {
			t.Errorf("claim of %q: %+v, want held by %s, stale %v", held.Tasks[i].Title, c, want.by, want.stale)
		}
	}
	var onlyStale listJSON
	mustTaskJSON(t, db, "default", &onlyStale, "", "list", "--stale")
	if !slices.Equal(titlesOf(onlyStale), []string{"long gone", "this second"}) {
		t.Errorf("--stale lists %q", titlesOf(onlyStale))
	}

	live := "bot until " + now.Add(time.Hour).In(zone).Format("15:04")
	table, _, _ := runTask(t, db, "default", false, "", "list")
	board, _, _ := runTask(t, db, "default", false, "", "board")
	for _, want := range []string{live, "stale: gone", "stale: edge"} {
		if !strings.Contains(table, want) || !strings.Contains(board, "["+want+"]") {
			t.Errorf("%q is missing from the list or the board:\n%s\n%s", want, table, board)
		}
	}
	for i, want := range []string{"Held by:  " + live, "Held by:  stale: gone", "Held by:  stale: edge"} {
		shown, _, err := runTask(t, db, "default", false, "", "show", strconv.FormatInt(ids[i], 10))
		if err != nil || !strings.Contains(shown, want+"\n") {
			t.Errorf("show #%d: %v, want the line %q in:\n%s", ids[i], err, want, shown)
		}
	}
}

func TestTaskReadCommandsRefuseAnUnknownProfileAndWrongArguments(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, args := range [][]string{{"list"}, {"board"}, {"show", "1"}} {
		if doc := failedTaskJSON(t, db, "no-such-profile", 3, args...); doc["code"] != "invalid_input" {
			t.Errorf("task %s with an unknown profile: %v", strings.Join(args, " "), doc)
		}
	}
	// A mistake in the number of arguments is invalid input like any other: exit 3, and under
	// --json the {"error","code"} document (cobra's own argument check would exit 1 with none).
	for _, c := range []struct {
		args []string
		want string // what the refusal says
	}{
		{[]string{"list", "extra"}, "task list takes no arguments"},
		{[]string{"list", "one", "two"}, "task list takes no arguments"},
		{[]string{"board", "extra"}, "task board takes no arguments"},
		{[]string{"show"}, "task show takes one task id"},
		{[]string{"show", "1", "2"}, "task show takes one task id"},
	} {
		doc := failedTaskJSON(t, db, "default", 3, c.args...)
		if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, c.want) {
			t.Errorf("task %s --json: %v, want the code invalid_input and the words %q", strings.Join(c.args, " "), doc, c.want)
		}
		out, _, err := runTask(t, db, "default", false, "", c.args...)
		if exitCode(err) != 3 || out != "" || !strings.Contains(errText(err), c.want) {
			t.Errorf("task %s as text: exit %d (%v), %q; want exit 3, the refusal and no output", strings.Join(c.args, " "), exitCode(err), err, out)
		}
	}
}

// The Archive is not a column: it is listed when it is named, by the operator and by an
// agent alike (spec 4.1 and 5.1), and the default list and the board never show it.
func TestTaskListShowsTheArchiveWhenItIsNamed(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	seedTaskRows(t, db,
		taskSeed{title: "inbox one"},
		taskSeed{title: "ready one", status: "ready"},
		taskSeed{title: "put away", status: "archived"},
		taskSeed{title: "put away too", status: "archived"},
	)
	for _, c := range []struct {
		name string
		args []string
		want []string
	}{
		{"the operator's default", nil, []string{"inbox one", "ready one"}},
		{"the operator names the archive", []string{"--status", "archived"}, []string{"put away", "put away too"}},
		{"the operator names it among others", []string{"--status", "ready,archived"}, []string{"ready one", "put away", "put away too"}},
		{"an agent's default", []string{"--as", "bot"}, []string{"ready one"}},
		{"an agent names the archive", []string{"--as", "bot", "--status", "archived"}, []string{"put away", "put away too"}},
	} {
		var got listJSON
		mustTaskJSON(t, db, "default", &got, "", append([]string{"list"}, c.args...)...)
		if !slices.Equal(titlesOf(got), c.want) {
			t.Errorf("%s: %q, want %q", c.name, titlesOf(got), c.want)
		}
	}
	var board struct {
		Counts map[string]int        `json:"counts"`
		Tasks  map[string][]taskJSON `json:"tasks"`
	}
	mustTaskJSON(t, db, "default", &board, "", "board")
	if len(board.Tasks) != 5 || board.Counts["inbox"] != 1 || board.Counts["ready"] != 1 {
		t.Errorf("the board with an archive: %+v", board)
	}
}

// What the caller sent is repeated in a refusal only in part: a huge argument must not
// make a huge message, and the cut never splits a character (the second value is 2-byte
// characters).
func TestTaskReadRefusalsRepeatOnlyAShortStretchOfWhatTheyRefuse(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, huge := range []string{strings.Repeat("x", 10<<10), strings.Repeat("\xc3\xa9", 5<<10)} {
		for _, args := range [][]string{{"list", "--status", huge}, {"list", "--source", huge}, {"show", huge}, {"list", huge}, {"board", huge}} {
			out, _, err := runTask(t, db, "default", true, "", args...)
			var doc map[string]any
			if exitCode(err) != 3 || json.Unmarshal([]byte(out), &doc) != nil {
				t.Fatalf("task %s <%d bytes>: exit %d (%v), %.80q", args[0], len(huge), exitCode(err), err, out)
			}
			msg, _ := doc["error"].(string)
			if len(msg) == 0 || len(msg) > 300 || len(out) > 400 || !utf8.ValidString(msg) {
				t.Errorf("task %s <%d bytes>: a refusal of %d bytes (document %d): %.120q", args[0], len(huge), len(msg), len(out), msg)
			}
		}
	}
}
