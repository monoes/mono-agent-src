package chat

import (
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// The extension's page-read chat may suggest a task, never add one (#451):
// propose_task answers with the proposal and writes nothing. The side panel
// shows the person a card, and only their click sends the task.

// rowCount is every row of every table: a write anywhere changes it.
func rowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	total := 0
	for _, n := range names {
		var c int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + n + `"`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		total += c
	}
	return total
}

func proposalOf(t *testing.T, out string) (map[string]interface{}, map[string]interface{}) {
	t.Helper()
	var res map[string]interface{}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result is not JSON: %v: %s", err, out)
	}
	p, ok := res["proposal"].(map[string]interface{})
	if !ok {
		t.Fatalf("no proposal in %s", out)
	}
	return res, p
}

func TestProposeTaskProposesAndWritesNothing(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "/bin/monoagentcli", "default")
	mt.SetPageReadOnly()
	if !strings.Contains(","+strings.Join(toolNames(mt), ",")+",", ",propose_task,") {
		t.Fatalf("propose_task is not offered in page-read mode: %v", toolNames(mt))
	}

	before := rowCount(t, db.DB)
	out, err := mt.Execute("propose_task", `{"title":"  Book the\nflights ","notes":" Before May.\nWindow seat. "}`)
	if err != nil {
		t.Fatal(err)
	}
	res, p := proposalOf(t, out)
	if p["kind"] != "task" || p["title"] != "Book the flights" || p["notes"] != "Before May.\nWindow seat." {
		t.Fatalf("proposal = %v", p)
	}
	if res["added"] != false || !strings.Contains(res["note"].(string), "confirm") {
		t.Fatalf("the answer must say nothing was added: %s", out)
	}
	if after := rowCount(t, db.DB); after != before {
		t.Fatalf("propose_task wrote %d rows", after-before)
	}
}

func TestProposalsAreOnlyOfferedToThePageReadChat(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "", "default")
	for _, tool := range []string{"propose_task", "propose_recording", "propose_vault_save", "propose_page_action", "propose_coder_handoff"} {
		for _, n := range toolNames(mt) {
			if n == tool {
				t.Fatalf("%s is offered outside page-read mode", tool)
			}
		}
		if _, err := mt.Execute(tool, `{"title":"x","goal":"x"}`); err == nil || !strings.Contains(err.Error(), "unknown tool") {
			t.Fatalf("%s outside page-read mode: %v", tool, err)
		}
	}
}

func TestProposeTaskNeedsATitleAndCutsWhatIsTooLong(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "", "default")
	mt.SetPageReadOnly()
	for _, args := range []string{`{}`, `{"title":"   \n "}`, `not json`} {
		if _, err := mt.Execute("propose_task", args); err == nil {
			t.Errorf("%s: want an error", args)
		}
	}
	long := strings.Repeat("é", 500)
	notes := strings.Repeat("ü", 6000)
	out, err := mt.Execute("propose_task", `{"title":"`+long+`","notes":"`+notes+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	_, p := proposalOf(t, out)
	title, _ := p["title"].(string)
	body, _ := p["notes"].(string)
	if utf8.RuneCountInString(title) != proposalTitleRunes {
		t.Errorf("title is %d runes, want %d", utf8.RuneCountInString(title), proposalTitleRunes)
	}
	if len(body) > proposalNotesBytes || !utf8.ValidString(body) {
		t.Errorf("notes are %d bytes (valid UTF-8: %v), want at most %d", len(body), utf8.ValidString(body), proposalNotesBytes)
	}
	if len(out) > 16*1024 {
		t.Errorf("the answer is %d bytes: it must fit the tool preview the side panel reads", len(out))
	}
}

// A recording is the person's to start (#451, phase 2): propose_recording
// answers with the goal and the steps for the person to perform, and starts
// nothing. Recording starts only on their click on the card.
func TestProposeRecordingProposesAndWritesNothing(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "/bin/monoagentcli", "default")
	mt.SetPageReadOnly()
	if !strings.Contains(","+strings.Join(toolNames(mt), ",")+",", ",propose_recording,") {
		t.Fatalf("propose_recording is not offered in page-read mode: %v", toolNames(mt))
	}

	before := rowCount(t, db.DB)
	out, err := mt.Execute("propose_recording", `{"goal":"  Export the\nweekly report ","steps":" Open Reports.\nPress Export. "}`)
	if err != nil {
		t.Fatal(err)
	}
	res, p := proposalOf(t, out)
	if p["kind"] != "recording" || p["goal"] != "Export the weekly report" || p["steps"] != "Open Reports.\nPress Export." {
		t.Fatalf("proposal = %v", p)
	}
	if _, ok := p["title"]; ok {
		t.Fatalf("a recording proposal carries a task's title: %v", p)
	}
	if res["added"] != false || !strings.Contains(res["note"].(string), "confirm") {
		t.Fatalf("the answer must say nothing was started: %s", out)
	}
	if after := rowCount(t, db.DB); after != before {
		t.Fatalf("propose_recording wrote %d rows", after-before)
	}
}

func TestProposeRecordingNeedsAGoalAndCutsWhatIsTooLong(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "", "default")
	mt.SetPageReadOnly()
	for _, args := range []string{`{}`, `{"goal":"   \n "}`, `{"title":"a task's title"}`, `not json`} {
		if _, err := mt.Execute("propose_recording", args); err == nil {
			t.Errorf("%s: want an error", args)
		}
	}
	out, err := mt.Execute("propose_recording", `{"goal":"`+strings.Repeat("é", 500)+`","steps":"`+strings.Repeat("ü", 6000)+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	_, p := proposalOf(t, out)
	goal, _ := p["goal"].(string)
	steps, _ := p["steps"].(string)
	if utf8.RuneCountInString(goal) != proposalTitleRunes {
		t.Errorf("goal is %d runes, want %d", utf8.RuneCountInString(goal), proposalTitleRunes)
	}
	if len(steps) > proposalNotesBytes || !utf8.ValidString(steps) {
		t.Errorf("steps are %d bytes (valid UTF-8: %v), want at most %d", len(steps), utf8.ValidString(steps), proposalNotesBytes)
	}
	if len(out) > 16*1024 {
		t.Errorf("the answer is %d bytes: it must fit the tool preview the side panel reads", len(out))
	}
}

// The chat may suggest saving a file from the page to the vault (#451), never
// save one: propose_vault_save names the item (an address, or the person's
// selection) and passes no bytes. The side panel fetches the item after the
// person's click, and only if it is on the page they are on.
func TestProposeVaultSaveProposesAndWritesNothing(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "/bin/monoagentcli", "default")
	mt.SetPageReadOnly()
	if !strings.Contains(","+strings.Join(toolNames(mt), ",")+",", ",propose_vault_save,") {
		t.Fatalf("propose_vault_save is not offered in page-read mode: %v", toolNames(mt))
	}
	before := rowCount(t, db.DB)
	out, err := mt.Execute("propose_vault_save", `{"url":" https://example.com/files/report.pdf ","name":"  Q3\nreport.pdf "}`)
	if err != nil {
		t.Fatal(err)
	}
	res, p := proposalOf(t, out)
	if p["kind"] != "vault" || p["url"] != "https://example.com/files/report.pdf" || p["name"] != "Q3 report.pdf" {
		t.Fatalf("proposal = %v", p)
	}
	if _, ok := p["selection"]; ok {
		t.Fatalf("an address proposal is not a selection: %v", p)
	}
	if res["added"] != false {
		t.Fatalf("the answer must say nothing was saved: %s", out)
	}
	out, err = mt.Execute("propose_vault_save", `{"selection":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, p = proposalOf(t, out); p["kind"] != "vault" || p["selection"] != true || p["url"] != nil {
		t.Fatalf("selection proposal = %v", p)
	}
	if after := rowCount(t, db.DB); after != before {
		t.Fatalf("propose_vault_save wrote %d rows", after-before)
	}
}

func TestProposeVaultSaveNamesOneItemAndCarriesNoBytes(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "", "default")
	mt.SetPageReadOnly()
	for _, args := range []string{
		`{}`, `{"name":"x.png"}`, `not json`,
		`{"url":"https://a.example/x.png","selection":true}`,
		`{"url":"file:///etc/passwd"}`, `{"url":"javascript:alert(1)"}`, `{"url":"/relative.png"}`, `{"url":"ftp://a.example/x.png"}`,
		`{"url":"data:image/png;base64,iVBORw0KGgo="}`, `{"url":"https://a.example/` + strings.Repeat("x", 2100) + `"}`,
		`{"url":"https://a.example/a b.png"}`,
	} {
		if _, err := mt.Execute("propose_vault_save", args); err == nil {
			t.Errorf("%s: want an error", args)
		}
	}
	out, err := mt.Execute("propose_vault_save", `{"url":"https://a.example/x.png","name":"`+strings.Repeat("é", 300)+`","data":"iVBORw0KGgo=","mime":"image/png"}`)
	if err != nil {
		t.Fatal(err)
	}
	_, p := proposalOf(t, out)
	if name, _ := p["name"].(string); len(name) > proposalNameBytes || !utf8.ValidString(name) {
		t.Errorf("name is %d bytes, want at most %d", len(name), proposalNameBytes)
	}
	for _, k := range []string{"data", "mime"} {
		if _, ok := p[k]; ok {
			t.Errorf("the proposal carries %q: the side panel fetches the item itself", k)
		}
	}
}

// Every proposal tool is offered in a page-read session, the one chat whose
// replies the side panel turns into cards.
func TestProposalToolsAreInThePageReadSet(t *testing.T) {
	for _, n := range proposalToolNames {
		if !slices.Contains(PageReadToolNames, n) {
			t.Errorf("proposal tool %s is not in PageReadToolNames", n)
		}
	}
}
