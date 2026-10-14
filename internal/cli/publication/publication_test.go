package publication

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"

	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// `publication`: the store is internal/publication's (tested there); these tests are about the
// commands. The CLI around them, including who the caller is, is faked through Deps, so each test
// has a database of its own, sets no environment and runs in parallel.

// cliError is the fake of the CLI's exit-code errors.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

// code is the exit code the CLI gives err: 2 not found, 3 invalid input, 5 refused, 1 any other.
func code(err error) int {
	var ce *cliError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ce):
		return ce.code
	default:
		return 1
	}
}

func errorOf(code int) func(format string, a ...interface{}) error {
	return func(format string, a ...interface{}) error {
		return &cliError{code: code, msg: fmt.Sprintf(format, a...)}
	}
}

// fixture is one test's database and caller: agent makes the caller an agent, which the
// operator-only commands refuse, and approval is what an agent's register is told.
type fixture struct {
	db       string
	agent    atomic.Bool
	approval atomic.Value // error, or nil
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{db: testdb.Path(t)}
}

func (f *fixture) deps(jsonOut bool) Deps {
	return Deps{
		JSONOutput:   func() bool { return jsonOut },
		ProfileID:    func() string { return "default" },
		OpenDB:       func() (*storage.Database, error) { return storage.NewDatabase(f.db) },
		NotFound:     errorOf(2),
		InvalidInput: errorOf(3),
		WriteJSON: func(w io.Writer, v any) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		},
		NewTable: func(w io.Writer, headers []string) *tablewriter.Table {
			cellFmt := tw.CellFormatting{AutoWrap: tw.WrapNone}
			table := tablewriter.NewTable(w,
				tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{Borders: tw.BorderNone})),
				tablewriter.WithConfig(tablewriter.Config{
					Header: tw.CellConfig{Formatting: cellFmt},
					Row:    tw.CellConfig{Formatting: cellFmt, Alignment: tw.CellAlignment{Global: tw.AlignLeft}},
				}),
			)
			table.Header(headers)
			return table
		},
		Operator: func(what string) error {
			if f.agent.Load() {
				return &cliError{code: 5, msg: "only the operator may " + what}
			}
			return nil
		},
		RequireApproval: func(string) error {
			if err, _ := f.approval.Load().(error); err != nil {
				return err
			}
			return nil
		},
	}
}

// run runs `publication <args>` with input on stdin and returns stdout.
func (f *fixture) run(jsonOut bool, input string, args ...string) (string, error) {
	cmd := NewCommand(f.deps(jsonOut))
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestPublicationCLI(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	run := func(input string, args ...string) (string, error) { return f.run(true, input, args...) }
	out, err := run(`{"platform":"x","kind":"post","body":"Published text","idempotency_key":"once"}`, "register", "--stdin-json")
	if err != nil {
		t.Fatal(err)
	}
	var entry publication.Entry
	if err := json.Unmarshal([]byte(out), &entry); err != nil || entry.ID == "" || entry.ProfileID != "default" {
		t.Fatalf("register JSON: %s %v", out, err)
	}
	out, err = run("", "list", "--search", "Published", "--platform", "x", "--limit", "1")
	if err != nil || !strings.Contains(out, entry.ID) {
		t.Fatalf("list JSON: %s %v", out, err)
	}
	out, err = run("", "get", entry.ID)
	if err != nil || !strings.Contains(out, "Published text") {
		t.Fatalf("get: %s %v", out, err)
	}
	out, err = run("", "stats")
	if err != nil || !strings.Contains(out, `"total": 1`) {
		t.Fatalf("stats: %s %v", out, err)
	}
	out, err = run("", "list", "--search", "missing")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty list: %s %v", out, err)
	}
	for _, args := range [][]string{{"list", "--limit", "0"}, {"list", "--since", "invalid"}, {"register"}} {
		if _, err := run("", args...); code(err) != 3 {
			t.Errorf("expected invalid input (exit 3) for %v, got exit %d (%v)", args, code(err), err)
		}
	}
	_, err = run("", "get", "missing")
	if code(err) != 2 || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing: exit %d, %v", code(err), err)
	}
}

func TestPublicationCLIDeleteAndCursor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	run := func(input string, args ...string) (string, error) { return f.run(true, input, args...) }
	var ids []string
	for _, key := range []string{"a", "b", "c"} {
		out, err := run(`{"platform":"x","kind":"post","body":"text `+key+`","idempotency_key":"`+key+`"}`, "register", "--stdin-json")
		var e publication.Entry
		if err != nil || json.Unmarshal([]byte(out), &e) != nil {
			t.Fatalf("register: %s %v", out, err)
		}
		ids = append(ids, e.ID)
	}
	out, err := run("", "list", "--limit", "2", "--cursor", "")
	var page struct {
		Publications []publication.Entry `json:"publications"`
		NextCursor   string              `json:"next_cursor"`
	}
	if err != nil || json.Unmarshal([]byte(out), &page) != nil || len(page.Publications) != 2 || page.NextCursor == "" {
		t.Fatalf("first page: %s %v", out, err)
	}
	out, err = run("", "list", "--limit", "2", "--cursor", page.NextCursor)
	if err != nil || json.Unmarshal([]byte(out), &page) != nil || len(page.Publications) != 1 || page.NextCursor != "" {
		t.Fatalf("last page: %s %v", out, err)
	}
	f.agent.Store(true)
	if _, err := run("", "delete", ids[0]); err == nil || !strings.Contains(err.Error(), "only the operator") {
		t.Fatalf("agent delete should be refused: %v", err)
	}
	f.agent.Store(false)
	if out, err = run("", "delete", ids[0]); err != nil || !strings.Contains(out, ids[0]) {
		t.Fatalf("delete: %s %v", out, err)
	}
	if _, err = run("", "get", ids[0]); code(err) != 2 {
		t.Fatalf("deleted publication still readable: exit %d (%v)", code(err), err)
	}
	if _, err = run("", "delete", ids[0]); code(err) != 2 {
		t.Errorf("deleting twice: exit %d (%v), want 2", code(err), err)
	}
}

func TestPublicationRedactCLIOperatorOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	run := func(input string, args ...string) (string, error) { return f.run(true, input, args...) }
	out, err := run(`{"platform":"x","kind":"post","title":"T","body":"private text","url":"https://example.com/1"}`, "register", "--stdin-json")
	if err != nil {
		t.Fatal(err)
	}
	id := out[strings.Index(out, `"id": "`)+7:]
	id = id[:strings.Index(id, `"`)]
	f.agent.Store(true)
	if _, err = run("", "redact", id); err == nil {
		t.Fatal("agent redacted history")
	}
	f.agent.Store(false)
	out, err = run("", "redact", id, "--title")
	if err != nil || strings.Contains(out, "private text") || strings.Contains(out, `"T"`) || !strings.Contains(out, "[redacted]") || !strings.Contains(out, "https://example.com/1") {
		t.Fatalf("redact: %s %v", out, err)
	}
	if _, err = run("", "redact", "missing"); err == nil {
		t.Fatal("missing id accepted")
	}
}

// register checks the input before the approval, and records nothing the approval refuses.
func TestPublicationRegisterNeedsTheApproval(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.approval.Store(error(&cliError{code: 5, msg: "needs an approved send"}))
	if _, err := f.run(true, `{"platform":"x"`, "register", "--stdin-json"); code(err) != 3 {
		t.Errorf("bad JSON is invalid input before any approval: exit %d (%v)", code(err), err)
	}
	if _, err := f.run(true, strings.Repeat(" ", 4*1024*1024+1), "register", "--stdin-json"); code(err) != 3 || !strings.Contains(err.Error(), "4 MiB") {
		t.Errorf("input over 4 MiB: exit %d (%v), want 3", code(err), err)
	}
	if _, err := f.run(true, `{"platform":"x","kind":"post","body":"b"}`, "register", "--stdin-json", "--approval", "h1"); code(err) != 5 {
		t.Fatalf("refused approval: exit %d (%v), want the refusal", code(err), err)
	}
	if out, err := f.run(true, "", "list"); err != nil || strings.TrimSpace(out) != "[]" {
		t.Errorf("a refused register recorded something: %s %v", out, err)
	}
}

// Without --json: the record, the table, paging and the counts as text.
func TestPublicationText(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if out, err := f.run(false, "", "list"); err != nil || out != "No publications.\n" {
		t.Fatalf("empty list: %q %v", out, err)
	}
	long := strings.Repeat("word ", 20)
	out, err := f.run(false, `{"platform":"x","kind":"post","body":"`+long+`","url":"https://example.com/1"}`, "register", "--stdin-json")
	if err != nil || !strings.HasPrefix(out, "ID: ") || !strings.Contains(out, "\nPlatform: x\nKind: post\n") || !strings.HasSuffix(out, "\nURL: https://example.com/1\n") {
		t.Fatalf("register: %q %v", out, err)
	}
	if _, err := f.run(false, `{"platform":"y","kind":"comment","body":"short"}`, "register", "--stdin-json"); err != nil {
		t.Fatal(err)
	}
	out, err = f.run(false, "", "list", "--limit", "1", "--cursor", "")
	if err != nil || !strings.Contains(out, "PLATFORM") || !strings.Contains(out, " short ") || !strings.Contains(out, "\nNext page: --cursor ") {
		t.Fatalf("first page as a table: %q %v", out, err)
	}
	out, err = f.run(false, "", "list")
	if err != nil || strings.Contains(out, long) || !strings.Contains(out, "...") || strings.Contains(out, "Next page") {
		t.Fatalf("the whole list, long content cut: %q %v", out, err)
	}
	if out, err := f.run(false, "", "stats"); err != nil || out != "2 publications\n" {
		t.Errorf("stats: %q %v", out, err)
	}
}
