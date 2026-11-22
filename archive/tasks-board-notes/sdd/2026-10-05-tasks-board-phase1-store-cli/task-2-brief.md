### Task 2: Types, errors and text cleaning

**Files:**
- Create: `internal/tasks/model.go`, `internal/tasks/clean.go`
- Test: `internal/tasks/clean_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces (every later task uses these names exactly): `Status` (`StatusInbox`, `StatusReady`, `StatusInProgress`, `StatusReview`, `StatusDone`, `StatusArchived`), `BoardStatuses`, `Status.IsBoard()`, `ParseStatus(string) (Status, error)`; source constants `SourceCLI`, `SourceApp`, `SourceChrome`, `SourceOS`, `SourceAgent`; `ActorKind` (`Human`, `Agent`, `Capture`), `Actor{Kind, Name}` with `Label()`; the types `Source`, `Claim`, `LastEvent`, `Task`, `Event`, `Profile`, `Counts`, `Board`, `Filter`, `AddInput`, `Edit`, `Placement`, `Outcome`; the limit constants; the errors `ErrNotFound`, `ErrInvalid`, `ErrOperatorOnly`, `ErrNotReady`, `ErrClaimed`, `ErrNotClaimant`, `ErrLimit` and `*ClaimedError{By, Until}`; the unexported helpers `invalid`, `operatorOnly`, `notReady`, `notClaimant`, `limit`, `cleanText`, `oneLine`, `cutRunes`, `cutBytes`, `cleanTitle`, `deriveTitleNotes`, `cleanURL`, `nameRE`, `clientIDRE`, `validSource`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tasks/clean_test.go`:

```go
package tasks

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"line ends", "a\r\nb\rc", "a\nb\nc"},
		{"escape byte", "red \x1b[31mtext\x1b[0m", "red [31mtext[0m"},
		{"tab and newline stay", "a\tb\nc", "a\tb\nc"},
		{"other controls", "a\x00b\x07c\x7fd", "abcd"},
		{"unicode tag characters", "visible\U000E0049\U000E0067hidden", "visiblehidden"},
		{"bidi overrides", "a\u202eb\u2066c", "abc"},
		{"byte order mark", "\ufefftext", "text"},
		{"invalid utf-8", "a\xffb", "a\ufffdb"},
		{"trimmed", "  \n text \t\n", "text"},
		{"only controls and space", "\x00\x1b \t\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cleanText(c.in)
			if got != c.want {
				t.Errorf("cleanText(%q) = %q, want %q", c.in, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("cleanText(%q) is not valid UTF-8", c.in)
			}
		})
	}
}

func TestCutRunesNeverSplitsACharacter(t *testing.T) {
	for _, s := range []string{"héllo wörld, this is long", strings.Repeat("\U0001F468\u200d\U0001F469\u200d\U0001F467", 40), strings.Repeat("日本語", 30)} {
		got := cutRunes(s, 10)
		if !utf8.ValidString(got) || utf8.RuneCountInString(got) > 10 || !strings.HasSuffix(got, "…") {
			t.Errorf("cutRunes(%q, 10) = %q", s, got)
		}
	}
	if got := cutRunes("short", 10); got != "short" {
		t.Errorf("a short string changed: %q", got)
	}
}

func TestCutBytesCutsOnACharacterAndSaysSo(t *testing.T) {
	long := strings.Repeat("é", 70<<10) // two bytes each
	got := cutBytes(long, MaxNotesBytes)
	if !utf8.ValidString(got) {
		t.Fatal("the cut text is not valid UTF-8")
	}
	i := strings.Index(got, "\n[truncated: ")
	if i < 0 || i > MaxNotesBytes {
		t.Fatalf("no truncation marker within the limit (marker at %d)", i)
	}
	if !strings.HasSuffix(got, "71680 characters in the original]") {
		t.Errorf("the marker must carry the original length: %q", got[i:])
	}
	if short := cutBytes("fits", MaxNotesBytes); short != "fits" {
		t.Errorf("a short text changed: %q", short)
	}
}

func TestDeriveTitleNotes(t *testing.T) {
	cases := []struct {
		name                 string
		title, notes, text   string
		wantTitle, wantNotes string
		wantErr              bool
	}{
		{name: "title only", title: "Fix it", wantTitle: "Fix it"},
		{name: "title and notes", title: "Fix it", notes: "In api.go", wantTitle: "Fix it", wantNotes: "In api.go"},
		{name: "title and text: the text is the notes", title: "Fix it", text: "details", wantTitle: "Fix it", wantNotes: "details"},
		{name: "one line of text is the title", text: "Reply to Sam", wantTitle: "Reply to Sam"},
		{name: "several lines", text: "Reply to Sam\nabout the invoice", wantTitle: "Reply to Sam", wantNotes: "Reply to Sam\nabout the invoice"},
		{name: "spaces collapse in the title only", text: "Fix   the\nbug", wantTitle: "Fix the", wantNotes: "Fix   the\nbug"},
		{name: "nothing", wantErr: true},
		{name: "only spaces and controls", text: " \x1b\x00\t\n ", wantErr: true},
		{name: "a title of controls only", title: "\x07 \x1b", wantErr: true},
		{name: "notes without a title", notes: "orphan", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			title, notes, err := deriveTitleNotes(c.title, c.notes, c.text)
			if c.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("err %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil || title != c.wantTitle || notes != c.wantNotes {
				t.Errorf("got (%q, %q, %v), want (%q, %q)", title, notes, err, c.wantTitle, c.wantNotes)
			}
		})
	}
}

func TestADerivedTitleIsCutAtOneHundredTwentyCharacters(t *testing.T) {
	text := strings.Repeat("word ", 80)
	title, notes, err := deriveTitleNotes("", "", text)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(title) > DerivedTitleRunes || !strings.HasSuffix(title, "…") {
		t.Errorf("title %q (%d runes)", title, utf8.RuneCountInString(title))
	}
	if notes != strings.TrimSpace(text) {
		t.Error("the notes must keep the whole text")
	}
}

func TestCleanURL(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", MaxURLBytes)
	cases := []struct{ in, want string }{
		{"https://example.com/a?b=1#c", "https://example.com/a?b=1#c"},
		{"  http://example.com  ", "http://example.com"},
		{"https://user:pass@example.com/x", "https://example.com/x"},
		{"ftp://example.com/x", ""},
		{"javascript:alert(1)", ""},
		{"https:///nohost", ""},
		{"not a url", ""},
		{"https://example.com/\x00", ""},
		{long, ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := cleanURL(c.in); got != c.want {
			t.Errorf("cleanURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNameAndClientIDShapes(t *testing.T) {
	for _, ok := range []string{"claude-7f3a", "agent:claude-code#a3f9", "a.b_c@d"} {
		if !nameRE.MatchString(ok) {
			t.Errorf("name %q should be accepted", ok)
		}
	}
	for _, bad := range []string{"", "has space", "semi;colon", strings.Repeat("a", 65), "tab\t"} {
		if nameRE.MatchString(bad) {
			t.Errorf("name %q should be refused", bad)
		}
	}
	if !clientIDRE.MatchString("550e8400-e29b-41d4-a716-446655440000") || clientIDRE.MatchString("a b") || clientIDRE.MatchString("") {
		t.Error("client id shape")
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/tasks/ -run 'TestClean|TestCut|TestDerive|TestADerived|TestName' -count=1`
Expected: FAIL to compile (`undefined: cleanText` and the others).

- [ ] **Step 3: Write `internal/tasks/model.go`**

```go
package tasks

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// timeFmt is the one fixed-width UTC format every stored time uses, so that
// comparing the text compares the times.
const timeFmt = "2006-01-02T15:04:05Z"

// Status is a task's column.
type Status string

const (
	StatusInbox      Status = "inbox"
	StatusReady      Status = "ready"
	StatusInProgress Status = "in_progress"
	StatusReview     Status = "review"
	StatusDone       Status = "done"
	StatusArchived   Status = "archived"
)

// BoardStatuses are the five columns, in board order. Archived is hidden.
var BoardStatuses = []Status{StatusInbox, StatusReady, StatusInProgress, StatusReview, StatusDone}

// IsBoard reports whether s is one of the five columns.
func (s Status) IsBoard() bool {
	for _, b := range BoardStatuses {
		if s == b {
			return true
		}
	}
	return false
}

// ParseStatus reads a status name; "progress" and "in-progress" mean in_progress.
func ParseStatus(name string) (Status, error) {
	n := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "-", "_")
	if n == "progress" {
		n = string(StatusInProgress)
	}
	switch st := Status(n); st {
	case StatusInbox, StatusReady, StatusInProgress, StatusReview, StatusDone, StatusArchived:
		return st, nil
	}
	return "", invalid("unknown status %q (use inbox, ready, in_progress, review, done or archived)", name)
}

// Where a task came from.
const (
	SourceCLI    = "cli"
	SourceApp    = "app"
	SourceChrome = "chrome"
	SourceOS     = "os"
	SourceAgent  = "agent"
)

// validSource reports whether kind is one of the stored source kinds.
func validSource(kind string) bool {
	switch kind {
	case SourceCLI, SourceApp, SourceChrome, SourceOS, SourceAgent:
		return true
	}
	return false
}

// ActorKind says what is acting: the operator, an agent, or a capture surface.
type ActorKind int

const (
	Human ActorKind = iota + 1
	Agent
	Capture
)

// Actor is who does something to a board. A Capture's Name is its surface
// ("chrome", "os"); an Agent's Name is the name it holds claims under.
type Actor struct {
	Kind ActorKind
	Name string
}

// Label is how the actor is written in an event and in a claim.
func (a Actor) Label() string {
	switch {
	case a.Kind == Human:
		return "you"
	case a.Name != "":
		return a.Name
	case a.Kind == Agent:
		return "agent"
	}
	return "capture"
}

// Limits (spec 4.6 and 5.2).
const (
	MaxTitleRunes       = 200
	DerivedTitleRunes   = 120
	MaxNotesBytes       = 64 << 10
	MaxCommentBytes     = 8 << 10
	MaxURLBytes         = 2048
	MaxSourceTitleRunes = 200
	MaxAppRunes         = 100
	MaxNameLen          = 64
	MaxClientIDLen      = 64
	MaxEventsPerTask    = 500
	MaxOpenTasks        = 2000
	AgentTasksPerHour   = 20
	DefaultListLimit    = 500
	MaxListLimit        = 2000
	DefaultLease        = 30 * time.Minute
	MaxLease            = 24 * time.Hour
	positionGap         = 1024
)

// Source says where a task came from.
type Source struct {
	Kind  string `json:"kind"`
	URL   string `json:"url"`
	Title string `json:"title"`
	App   string `json:"app"`
}

// Claim is an agent's hold on an in-progress task.
type Claim struct {
	By    string    `json:"by"`
	Until time.Time `json:"until"`
	Stale bool      `json:"stale"`
}

// LastEvent is the newest event of a task.
type LastEvent struct {
	Actor string    `json:"actor"`
	Kind  string    `json:"kind"`
	At    time.Time `json:"at"`
}

// Task is a card on a profile's board.
type Task struct {
	ID        int64      `json:"id"`
	ProfileID string     `json:"profile_id"`
	Title     string     `json:"title"`
	Notes     string     `json:"notes"`
	Status    Status     `json:"status"`
	Position  int64      `json:"position"`
	Source    Source     `json:"source"`
	Claim     *Claim     `json:"claim"`
	LastEvent *LastEvent `json:"last_event"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Event is one change of a task.
type Event struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Kind       string    `json:"kind"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	Note       string    `json:"note"`
}

// Profile is the profile a board belongs to.
type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Counts are a board's cards per column; Stale counts claims past their lease.
type Counts struct {
	Inbox      int `json:"inbox"`
	Ready      int `json:"ready"`
	InProgress int `json:"in_progress"`
	Review     int `json:"review"`
	Done       int `json:"done"`
	Stale      int `json:"stale"`
}

// Board is a whole profile's board, read in one snapshot.
type Board struct {
	Profile Profile           `json:"profile"`
	Rev     int64             `json:"rev"`
	Counts  Counts            `json:"counts"`
	Tasks   map[Status][]Task `json:"tasks"`
}

// Filter narrows List. No statuses means the caller's default (see List).
type Filter struct {
	Statuses  []Status
	Source    string
	ClaimedBy string
	Stale     bool
	Limit     int
}

// AddInput is a new task. Title and Text are alternatives: with no Title the
// first line of Text is the title (spec 4.6).
type AddInput struct {
	Title       string
	Notes       string // only with a Title
	Text        string // captured or typed text
	Ready       bool   // straight to Ready: the operator only
	SourceKind  string // "", cli, app, chrome or os: the actor decides what is allowed
	SourceURL   string
	SourceTitle string
	SourceApp   string
	ClientID    string // an idempotency key from a capture surface
}

// Edit changes a task's text. A nil field stays as it is.
type Edit struct {
	Title *string
	Notes *string
}

// Placement says where a moved card goes in its new column. At most one field
// may be set; none means the column's default (the top of Inbox, Review and
// Done, the bottom of Ready and In progress).
type Placement struct {
	Before, After int64 // the id of a card in the target column
	Top, Bottom   bool
}

func (p Placement) set() int {
	n := 0
	for _, b := range []bool{p.Before != 0, p.After != 0, p.Top, p.Bottom} {
		if b {
			n++
		}
	}
	return n
}

// Outcome is how an agent hands a task back: a result, or a question.
type Outcome struct {
	Result   string
	Question string
}

var (
	ErrNotFound     = errors.New("task not found")
	ErrInvalid      = errors.New("invalid input")
	ErrOperatorOnly = errors.New("only the operator can do that")
	ErrNotReady     = errors.New("task cannot be claimed")
	ErrClaimed      = errors.New("task is claimed")
	ErrNotClaimant  = errors.New("you do not hold this task")
	ErrLimit        = errors.New("limit reached")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func operatorOnly(what string) error { return fmt.Errorf("%w: %s", ErrOperatorOnly, what) }

func notReady(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotReady, fmt.Sprintf(format, a...))
}

func notClaimant(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotClaimant, fmt.Sprintf(format, a...))
}

func limit(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrLimit, fmt.Sprintf(format, a...))
}

// ClaimedError says who holds a task and until when. It matches ErrClaimed.
type ClaimedError struct {
	By    string
	Until time.Time
}

func (e *ClaimedError) Error() string {
	return fmt.Sprintf("task is claimed by %s until %s", e.By, e.Until.UTC().Format(time.RFC3339))
}

func (e *ClaimedError) Is(target error) bool { return target == ErrClaimed }
```

- [ ] **Step 4: Write `internal/tasks/clean.go`**

```go
package tasks

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	nameRE     = regexp.MustCompile(fmt.Sprintf(`^[A-Za-z0-9._#@:-]{1,%d}$`, MaxNameLen))
	clientIDRE = regexp.MustCompile(fmt.Sprintf(`^[A-Za-z0-9_-]{1,%d}$`, MaxClientIDLen))
)

// hidden reports characters that show nothing but can carry text to a reader:
// the Unicode tag block (invisible "ASCII smuggling"), bidi overrides and the
// byte order mark.
func hidden(r rune) bool {
	switch {
	case r >= 0xE0000 && r <= 0xE007F:
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return r == 0xFEFF
}

// cleanText makes text safe to store and to print: invalid UTF-8 becomes
// U+FFFD, line ends become \n, control characters other than \n and \t and the
// hidden characters are dropped (an escape byte would otherwise reach a
// terminal), and the ends are trimmed.
func cleanText(s string) string {
	s = strings.ToValidUTF8(s, "\ufffd")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r), hidden(r):
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// oneLine is cleanText with every run of white space collapsed to one space.
func oneLine(s string) string { return strings.Join(strings.Fields(cleanText(s)), " ") }

// cutRunes cuts s to at most n runes (n >= 2), ending in an ellipsis when it cut.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimRightFunc(string(r[:n-1]), unicode.IsSpace) + "…"
}

// cutBytes cuts s (valid UTF-8) to at most max bytes on a character boundary
// and says how long the original was.
func cutBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + fmt.Sprintf("\n[truncated: %d characters in the original]", utf8.RuneCountInString(s))
}

// cleanTitle is a title as stored: one line, at most MaxTitleRunes.
func cleanTitle(s string) string { return cutRunes(oneLine(s), MaxTitleRunes) }

// deriveTitleNotes turns the three ways a task's words arrive into the stored
// title and notes (spec 4.6). It refuses words that clean to nothing.
func deriveTitleNotes(title, notes, text string) (string, string, error) {
	title, notes, text = cleanTitle(title), cutBytes(cleanText(notes), MaxNotesBytes), cleanText(text)
	switch {
	case title != "":
		if notes == "" {
			notes = cutBytes(text, MaxNotesBytes)
		}
		return title, notes, nil
	case notes != "":
		return "", "", invalid("notes need a title: give a title, or send the text alone and its first line becomes the title")
	case text == "":
		return "", "", invalid("a task needs a title or some text")
	}
	first := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first = text[:i]
	}
	derived := cutRunes(strings.Join(strings.Fields(first), " "), DerivedTitleRunes)
	if derived == text {
		return derived, "", nil
	}
	return derived, cutBytes(text, MaxNotesBytes), nil
}

// cleanURL returns the URL when it is a plain http or https address, without
// user-info and within the limit; otherwise "".
func cleanURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxURLBytes {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	u.User = nil
	out := u.String()
	if len(out) > MaxURLBytes {
		return ""
	}
	return out
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test ./internal/tasks/ -run 'TestClean|TestCut|TestDerive|TestADerived|TestName' -count=1` then `gofmt -l internal/tasks`
Expected: PASS, and `gofmt` prints nothing.

- [ ] **Step 6: Commit**

```
git add internal/tasks/model.go internal/tasks/clean.go internal/tasks/clean_test.go
```
then
```
git commit -m "feat(tasks): types, errors and text cleaning" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

