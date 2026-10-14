package task

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The names and signatures the later tasks (the operator's and the agent's
// commands) print with.
var (
	_ func(io.Writer, tasks.Profile, string, tasks.Task, []tasks.Event) = printTask
	_ func(io.Writer, string)                                           = printNotes
	_ func(tasks.Profile, string, string) string                        = taskCommand
	_ func(tasks.Task) string                                           = heldNote
	_ func(string, int) string                                          = taskCut
	_ func(time.Time, time.Time) string                                 = taskAge
	_ string                                                            = untrustedNotice
)

// ellipsis ends a text that was cut.
const ellipsis = "\U00002026"

func TestTaskShowTextPrintsTheTaskItsHolderAndItsHistory(t *testing.T) {
	db := newTaskTestDB(t)
	zone := localNoonZone(t)
	addTaskProfile(t, db, "work-id", "Work")
	created := time.Date(2026, 10, 5, 9, 30, 0, 0, time.UTC)
	until := time.Now().Add(time.Hour)
	id := seedTaskRows(t, db, taskSeed{
		profile: "work-id", title: "Review the invoice", notes: "Line one\nLine two", status: "in_progress",
		source: "chrome", link: "https://example.com/p?q=1", pageTitle: "Invoice page", app: "Chrome",
		holder: "bot", until: until, created: created,
	})[0]
	at := func(minutes int) time.Time { return created.Add(time.Duration(minutes) * time.Minute) }
	seedTaskEvent(t, db, id, at(0), "chrome", "created", "", "inbox", "")
	seedTaskEvent(t, db, id, at(30), "you", "moved", "inbox", "ready", "")
	seedTaskEvent(t, db, id, at(35), "bot", "claimed", "ready", "in_progress", "")
	seedTaskEvent(t, db, id, at(40), "bot", "comment", "", "", "Looking at it.\nSecond line")
	seedTaskEvent(t, db, id, at(50), "agent:claude-code#a3f9", "comment", "", "", strings.Repeat("x", 300))
	stamp := func(minutes int) string { return at(minutes).In(zone).Format("01-02 15:04") }

	out, _, err := runTask(t, db, "Work", false, "", "show", "#"+strconv.FormatInt(id, 10))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`#%d  Review the invoice
Profile:  Work
Status:   In progress
Source:   chrome, Invoice page, in Chrome
Link:     https://example.com/p?q=1
Held by:  bot until %s
Created:  %s

%s
    Line one
    Line two
%s

History:
%s
  %s  chrome       created -> inbox
  %s  you          moved -> ready
  %s  bot          claimed -> in_progress
  %s  bot          comment
      Looking at it. Second line
  %s  agent:claude-code#a3f9 comment
      %s
(full text: monoagentcli --profile work-id task show %d --json)
`, id, until.In(zone).Format("15:04"), created.In(zone).Format("2006-01-02 15:04"), untrustedNotice, notesEnd, historyNotice,
		stamp(0), stamp(30), stamp(35), stamp(40), stamp(50), strings.Repeat("x", 199)+ellipsis, id)
	if out != want {
		t.Errorf("show as text:\n%s\nwant:\n%s", out, want)
	}
}

func TestTaskShowTextOmitsWhatTheTaskDoesNotHave(t *testing.T) {
	db := newTaskTestDB(t)
	zone := localNoonZone(t)
	created := time.Date(2026, 10, 5, 9, 30, 0, 0, time.UTC)
	id := seedTaskRows(t, db, taskSeed{title: "plain", created: created})[0]
	seedTaskEvent(t, db, id, created, "you", "created", "", "inbox", "")
	out, _, err := runTask(t, db, "default", false, "", "show", strconv.FormatInt(id, 10))
	// Events without a note carry no caution, and nothing says a note was cut.
	want := fmt.Sprintf("#%d  plain\nProfile:  Default\nStatus:   Inbox\nSource:   cli\nCreated:  %s\n\nHistory:\n  %s  you          created -> inbox\n",
		id, created.In(zone).Format("2006-01-02 15:04"), created.In(zone).Format("01-02 15:04"))
	if err != nil || out != want {
		t.Errorf("show as text: %v\n%s\nwant:\n%s", err, out, want)
	}
	// A task with no history has no History heading either.
	bare := seedTaskRows(t, db, taskSeed{title: "bare", created: created})[0]
	out, _, err = runTask(t, db, "default", false, "", "show", strconv.FormatInt(bare, 10))
	if err != nil || strings.Contains(out, "History") || strings.Contains(out, "untrusted") {
		t.Errorf("a task with no events and no notes: %v, %q", err, out)
	}
}

func TestTaskShowJSONIsTheProfileTheTaskAndItsEvents(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	ids := seedTaskRows(t, db,
		taskSeed{title: "no history"},
		taskSeed{title: "held", status: "in_progress", holder: "bot", until: time.Now().Add(time.Hour)},
	)
	out, _, err := runTask(t, db, "default", true, "", "show", strconv.FormatInt(ids[0], 10))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc) != 3 || doc["profile"] == nil || doc["task"] == nil || doc["events"] == nil {
		t.Fatalf("the document has %d keys: %s", len(doc), out)
	}
	if got := strings.TrimSpace(string(doc["events"])); got != "[]" {
		t.Errorf("the events of a task with none are %s, want []", got)
	}
	var plain, held struct {
		Task struct {
			Claim *struct {
				By    string `json:"by"`
				Stale bool   `json:"stale"`
			} `json:"claim"`
		} `json:"task"`
	}
	mustTaskJSON(t, db, "default", &plain, "", "show", strconv.FormatInt(ids[0], 10))
	mustTaskJSON(t, db, "default", &held, "", "show", strconv.FormatInt(ids[1], 10))
	if plain.Task.Claim != nil || held.Task.Claim == nil || held.Task.Claim.By != "bot" || held.Task.Claim.Stale {
		t.Errorf("claims: %+v and %+v", plain.Task.Claim, held.Task.Claim)
	}
}

// badTerminalRune reports what must never reach a terminal from a task's text:
// a control character other than a line end and a tab, and the hidden characters
// (the Unicode tag block, bidi controls and the byte order mark).
func badTerminalRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case unicode.IsControl(r):
		return true
	case r >= 0xE0000 && r <= 0xE007F, r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0xFEFF:
		return true
	}
	return false
}

// The store cleans a text as it comes in; the commands print what it kept. A text with escape
// sequences, hidden characters and bytes that are not UTF-8 reaches no terminal in any view.
func TestTaskTextOutputNeverCarriesAControlOrHiddenCharacter(t *testing.T) {
	t.Parallel()
	for _, r := range []rune{0, 0x07, 0x1b, 0x9b, 0x202e, 0x2066, 0xe0041, 0xfeff} {
		if !badTerminalRune(r) {
			t.Fatalf("the check itself does not flag %U", r)
		}
	}
	db := newTaskTestDB(t)
	hostile := "Fix the\x1b[31m thing\x00\x07\U0000202eevil\U0000202c\U000e0041\U000e0042\U0000feff\U00002066\U00002069 now\r\n" +
		"second line\x1b]0;pwned\x07\xff\xfe end"
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, hostile, "add", "--stdin", "--app", "Safari\x1b[2J", "--source-title", "Page\x1b]0;x\x07 title",
		"--url", "https://example.com/\x1b[2J")
	var held addedJSON
	mustTaskJSON(t, db, "default", &held, "", "add", "A\x1b[1mready\U0000202e one", "--ready", "--notes", "note\x1b[0m\U000e0041")
	id := strconv.FormatInt(added.Task.ID, 10)
	for _, args := range [][]string{{"list"}, {"board"}, {"show", id}, {"show", strconv.FormatInt(held.Task.ID, 10)}} {
		out, _, err := runTask(t, db, "default", false, "", args...)
		if err != nil {
			t.Fatalf("task %s: %v", strings.Join(args, " "), err)
		}
		if !utf8.ValidString(out) {
			t.Errorf("task %s printed bytes that are not UTF-8: %q", strings.Join(args, " "), out)
		}
		for _, r := range out {
			if badTerminalRune(r) {
				t.Errorf("task %s printed %U in %q", strings.Join(args, " "), r, out)
				break
			}
		}
		if !strings.Contains(out, "Fix the") && !strings.Contains(out, "ready one") {
			t.Errorf("task %s printed neither task: %q", strings.Join(args, " "), out)
		}
	}
	out, _, _ := runTask(t, db, "default", false, "", "show", id)
	for _, want := range []string{"Fix the[31m thing", "second line]0;pwned", "Safari[2J", "Page]0;x title"} {
		if !strings.Contains(out, want) {
			t.Errorf("the visible part %q is missing from:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Link:") {
		t.Errorf("a link with a control character in it was kept:\n%s", out)
	}
}

func TestTaskTextCutsTitlesAtTheirLengths(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, title := range []string{
		strings.Repeat("a", 60), strings.Repeat("b", 61), strings.Repeat("\U000000e9", 100),
		strings.Repeat("c", 70), strings.Repeat("d", 71), strings.Repeat("f", 200),
	} {
		mustTaskJSON(t, db, "default", &addedJSON{}, "", "add", title)
	}
	table, _, err := runTask(t, db, "default", false, "", "list")
	if err != nil {
		t.Fatal(err)
	}
	cells := func(out string) map[string]bool {
		found := map[string]bool{}
		for _, line := range strings.Split(out, "\n") {
			for _, cell := range strings.Fields(line) {
				found[cell] = true
			}
		}
		return found
	}
	inTable := cells(table)
	for cell, want := range map[string]bool{
		strings.Repeat("a", 60):                     true, // 60 characters fit
		strings.Repeat("b", 59) + ellipsis:          true, // 61 are cut to 59 and the mark
		strings.Repeat("\U000000e9", 59) + ellipsis: true, // a cut never splits a character
		strings.Repeat("b", 61):                     false,
		strings.Repeat("b", 60) + ellipsis:          false,
		strings.Repeat("c", 59) + ellipsis:          true,
	} {
		if inTable[cell] != want {
			t.Errorf("the list has the title cell %q: %v, want %v", cell, inTable[cell], want)
		}
	}
	board, _, err := runTask(t, db, "default", false, "", "board")
	if err != nil {
		t.Fatal(err)
	}
	inBoard := cells(board)
	for cell, want := range map[string]bool{
		strings.Repeat("c", 70):            true,
		strings.Repeat("d", 69) + ellipsis: true,
		strings.Repeat("d", 71):            false,
		strings.Repeat("b", 61):            true, // the board is wider than the list
		strings.Repeat("f", 69) + ellipsis: true,
	} {
		if inBoard[cell] != want {
			t.Errorf("the board has the title cell %q: %v, want %v", cell, inBoard[cell], want)
		}
	}
	var shown struct {
		Task taskJSON `json:"task"`
	}
	var listed listJSON
	mustTaskJSON(t, db, "default", &listed, "", "list")
	if len(listed.Tasks) != 6 {
		t.Fatalf("the list has %d tasks, want the 6 that were added", len(listed.Tasks))
	}
	long := listed.Tasks[0]
	mustTaskJSON(t, db, "default", &shown, "", "show", strconv.FormatInt(long.ID, 10))
	text, _, _ := runTask(t, db, "default", false, "", "show", strconv.FormatInt(long.ID, 10))
	if !strings.HasPrefix(text, fmt.Sprintf("#%d  %s\n", long.ID, shown.Task.Title)) || utf8.RuneCountInString(shown.Task.Title) != 200 {
		t.Errorf("show prints the title whole: %q", text[:min(len(text), 120)])
	}
}

func TestTaskCutShortensToNCharactersAndNeverSplitsOne(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"abcde", 5, "abcde"},
		{"abcdef", 5, "abcd" + ellipsis},
		{"abcdef", 6, "abcdef"},
		{"abcdefg", 6, "abcde" + ellipsis},
		{"ab", 1, ellipsis},
		{"h\U000000e9llo w\U000000f6rld", 5, "h\U000000e9ll" + ellipsis},
		{"h\U000000e9llo", 5, "h\U000000e9llo"},
		{"\U0001f600\U0001f600\U0001f600", 2, "\U0001f600" + ellipsis},
		// No room at all: nothing, and never a panic.
		{"ab", 0, ""},
		{"ab", -1, ""},
		{"", 0, ""},
		{"", -1, ""},
		{"ab", -1 << 30, ""},
	} {
		got := taskCut(c.in, c.n)
		if got != c.want || !utf8.ValidString(got) || utf8.RuneCountInString(got) > max(c.n, 0) {
			t.Errorf("taskCut(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// The cut of an argument that an error message repeats is the same cut, at the length that
// message allows: one rule for the ellipsis, not two copies of it.
func TestTaskCutArgIsTaskCutAtTheEchoLength(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"", "short", strings.Repeat("9", echoRunes-1), strings.Repeat("9", echoRunes), strings.Repeat("9", echoRunes+1),
		strings.Repeat("x", 10<<10), strings.Repeat("\U000000e9", 5<<10), strings.Repeat("\U0001f600", echoRunes+1),
		strings.Repeat("\xff\xfe", 100), "a\x1b[31mred\x00",
	} {
		if got, want := cutArg(in), taskCut(in, echoRunes); got != want {
			t.Errorf("cutArg(%.20q...) = %.40q, want what taskCut gives, %.40q", in, got, want)
		}
	}
	if got := cutArg(strings.Repeat("9", echoRunes+1)); got != strings.Repeat("9", echoRunes-1)+ellipsis {
		t.Errorf("a long argument is cut to %d characters ending in the ellipsis: %q", echoRunes, got)
	}
}

func TestTaskAgeUsesTheLargestWholeUnit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		ago  time.Duration
		want string
	}{
		{0, "now"}, {59 * time.Second, "now"}, {time.Minute, "1m"}, {59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h"}, {47*time.Hour + 59*time.Minute, "47h"}, {48 * time.Hour, "2d"}, {72 * time.Hour, "3d"},
		{400 * 24 * time.Hour, "400d"}, {-time.Hour, "now"},
	} {
		if got := taskAge(now.Add(-c.ago), now); got != c.want {
			t.Errorf("a task from %v ago is %q, want %q", c.ago, got, c.want)
		}
	}
}

func TestTaskCountForPicksTheCountOfTheColumn(t *testing.T) {
	t.Parallel()
	c := tasks.Counts{Inbox: 1, Ready: 2, InProgress: 3, Review: 4, Done: 5, Stale: 6}
	for st, want := range map[tasks.Status]int{
		tasks.StatusInbox: 1, tasks.StatusReady: 2, tasks.StatusInProgress: 3, tasks.StatusReview: 4, tasks.StatusDone: 5, tasks.StatusArchived: 0,
	} {
		if got := countFor(c, st); got != want {
			t.Errorf("countFor(%s) = %d, want %d", st, got, want)
		}
	}
}

// printTask is what the commands of the later tasks print a changed task with.
func TestTaskPrintTaskPrintsATaskWithoutEvents(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	printTask(&out, tasks.Profile{ID: "p", Name: "Work"}, "", tasks.Task{ID: 7, Title: "Alone", Status: tasks.StatusReady, Source: tasks.Source{Kind: "cli"}}, nil)
	for _, want := range []string{"#7  Alone\n", "Profile:  Work\n", "Status:   Ready\n", "Source:   cli\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("%q is missing from %q", want, out.String())
		}
	}
	for _, never := range []string{"History", "Held by", "Link:", "untrusted"} {
		if strings.Contains(out.String(), never) {
			t.Errorf("a task with no events, no claim, no link and no notes prints %q: %q", never, out.String())
		}
	}
}
