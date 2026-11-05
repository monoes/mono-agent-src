package mcp

// What a task tool returns is a view of the store's task in which every text a person, an agent
// or a capture wrote has a name ending in _untrusted (spec 4.3 and 8), and what it refuses names
// its code first.

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/tasks"
)

// docKeys are a JSON object's keys, sorted.
func docKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// asDoc is a value as the JSON object a tool would return.
func asDoc(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return m
}

func TestATaskViewNamesEveryTextUntrusted(t *testing.T) {
	// Four different times, so that a view that mixes two of them up is seen.
	created := time.Date(2026, 10, 5, 8, 15, 0, 0, time.UTC)
	changed := time.Date(2026, 10, 6, 9, 50, 0, 0, time.UTC)
	happened := time.Date(2026, 10, 6, 9, 45, 0, 0, time.UTC)
	until := time.Date(2026, 10, 6, 10, 30, 0, 0, time.UTC)
	v := asDoc(t, viewOf(tasks.Task{
		ID: 7, ProfileID: "default", Title: "the title", Notes: "the notes", Status: tasks.StatusInProgress, Position: 2048,
		Source:    tasks.Source{Kind: "chrome", URL: "https://example.com/a", Title: "the page", App: "the app"},
		Author:    "agent:z#0003",
		Claim:     &tasks.Claim{By: "agent:x#0001", Until: until, Stale: true},
		LastEvent: &tasks.LastEvent{Actor: "agent:y#0002", Kind: "claimed", At: happened},
		CreatedAt: created, UpdatedAt: changed,
	}))
	want := []string{"author", "claim", "created_at", "id", "last_event", "notes_untrusted", "position", "profile_id",
		"source_app_untrusted", "source_kind", "source_title_untrusted", "source_url_untrusted", "status", "title_untrusted", "updated_at"}
	if got := docKeys(v); !equalStrings(got, want) {
		t.Errorf("a task's fields are %v, want exactly %v", got, want)
	}
	for k, want := range map[string]any{
		"title_untrusted": "the title", "notes_untrusted": "the notes", "source_url_untrusted": "https://example.com/a",
		"source_title_untrusted": "the page", "source_app_untrusted": "the app", "source_kind": "chrome",
		"status": "in_progress", "id": float64(7), "profile_id": "default", "position": float64(2048),
		"created_at": "2026-10-05T08:15:00Z", "updated_at": "2026-10-06T09:50:00Z",
		// The author is a name, as the claim's holder and an event's actor are: plain.
		"author": "agent:z#0003",
	} {
		if v[k] != want {
			t.Errorf("%s = %v, want %v", k, v[k], want)
		}
	}
	// The claim and the last event go out as the store's own structs, under plain names: a text
	// field added to either one would reach an agent unmarked. So their fields are pinned too, and
	// a new one fails here until someone decides what it is called.
	claim, _ := v["claim"].(map[string]any)
	if got, want := docKeys(claim), []string{"by", "stale", "until"}; !equalStrings(got, want) {
		t.Errorf("a claim's fields are %v, want exactly %v", got, want)
	}
	if claim["by"] != "agent:x#0001" || claim["until"] != "2026-10-06T10:30:00Z" || claim["stale"] != true {
		t.Errorf("claim = %v", claim)
	}
	last, _ := v["last_event"].(map[string]any)
	if got, want := docKeys(last), []string{"actor", "at", "kind"}; !equalStrings(got, want) {
		t.Errorf("a last event's fields are %v, want exactly %v", got, want)
	}
	if last["actor"] != "agent:y#0002" || last["kind"] != "claimed" || last["at"] != "2026-10-06T09:45:00Z" {
		t.Errorf("last_event = %v", last)
	}
}

func TestAViewThroughAPointerIsTheViewOrNothing(t *testing.T) {
	task := tasks.Task{ID: 7, Title: "the title", Status: tasks.StatusReady, Claim: &tasks.Claim{By: "agent:x#0001"}}
	if viewPtr(nil) != nil {
		t.Error("a task that is not there must have no view")
	}
	if p := viewPtr(&task); p == nil || !reflect.DeepEqual(*p, viewOf(task)) {
		t.Errorf("a view through a pointer is %+v, want %+v", p, viewOf(task))
	}
}

func TestAnEventViewNamesItsNoteUntrusted(t *testing.T) {
	at := time.Date(2026, 10, 6, 10, 30, 0, 0, time.UTC)
	later := time.Date(2026, 10, 6, 10, 35, 0, 0, time.UTC)
	views := eventViews([]tasks.Event{
		{ID: 3, At: at, Actor: "you", Kind: "comment", Note: "look here"},
		{ID: 4, At: later, Actor: "agent:x#0001", Kind: "claimed", FromStatus: "ready", ToStatus: "in_progress", Note: "taking it"},
	})
	if len(views) != 2 {
		t.Fatalf("two events came back as %d views", len(views))
	}
	e := asDoc(t, views[0])
	if got, want := docKeys(e), []string{"actor", "at", "from_status", "id", "kind", "note_untrusted", "to_status"}; !equalStrings(got, want) {
		t.Errorf("an event's fields are %v, want exactly %v", got, want)
	}
	for k, want := range map[string]any{
		"id": float64(3), "at": "2026-10-06T10:30:00Z", "actor": "you", "kind": "comment",
		"from_status": "", "to_status": "", "note_untrusted": "look here",
	} {
		if e[k] != want {
			t.Errorf("event %s = %v, want %v", k, e[k], want)
		}
	}
	moved := asDoc(t, views[1])
	for k, want := range map[string]any{
		"id": float64(4), "at": "2026-10-06T10:35:00Z", "actor": "agent:x#0001", "kind": "claimed",
		"from_status": "ready", "to_status": "in_progress", "note_untrusted": "taking it",
	} {
		if moved[k] != want {
			t.Errorf("event %s = %v, want %v", k, moved[k], want)
		}
	}
	for name, v := range map[string]any{"no events": eventViews(nil), "no tasks": listViews(nil)} {
		if b, _ := json.Marshal(v); string(b) != "[]" {
			t.Errorf("%s: %s, want [] (arrays are never null)", name, b)
		}
	}
}

func TestAListCutsLongNotesAndSaysWhere(t *testing.T) {
	exact := strings.Repeat("\U000000e9", listNotesRunes) // two bytes each: a cut must not split one
	if got := cutListNotes(exact); got != exact {
		t.Errorf("notes of exactly %d characters were changed", listNotesRunes)
	}
	got := cutListNotes(exact + "x")
	if !strings.HasPrefix(got, exact+"\n[cut at 1000 characters") || !strings.Contains(got, "task_get") || !utf8.ValidString(got) {
		t.Errorf("notes one character over the limit: %q", got[len(exact):])
	}
	long := tasks.Task{ID: 1, Notes: exact + "tail"}
	if v := listViews([]tasks.Task{long}); strings.Contains(v[0].NotesUntrusted, "tail") {
		t.Error("a list's notes were not cut")
	}
	if v := listView(long); strings.Contains(v.NotesUntrusted, "tail") {
		t.Error("a verb's result must cut the notes as a list does")
	}
	if v := viewOf(long); v.NotesUntrusted != long.Notes {
		t.Error("one task's notes must be whole")
	}
}

func TestTaskToolErrNamesTheCodeFirst(t *testing.T) {
	until := time.Date(2026, 10, 6, 10, 30, 0, 0, time.UTC)
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: #3", tasks.ErrNotFound), "not_found: task not found: #3"},
		{fmt.Errorf("%w: a task needs a title", tasks.ErrInvalid), "invalid_input: "},
		{fmt.Errorf("%w: add to Ready", tasks.ErrOperatorOnly), "operator_only: "},
		{fmt.Errorf("%w: task #3 is inbox", tasks.ErrNotReady), "not_ready: "},
		{fmt.Errorf("%w: claim it first", tasks.ErrNotClaimant), "not_claimant: "},
		{fmt.Errorf("%w: 20 an hour", tasks.ErrLimit), "limit: "},
		{fmt.Errorf("%w: runs are live", tasks.ErrAccountingMode), "accounting_mode: "},
		{&tasks.ClaimedError{By: "agent:b#0002", Until: until}, "claimed: task is claimed by agent:b#0002 until 2026-10-06T10:30:00Z"},
	} {
		got := taskToolErr(c.err)
		if got == nil || !strings.HasPrefix(got.Error(), c.want) || !errors.Is(got, c.err) {
			t.Errorf("%v: %v, want it to start %q and wrap the store's error", c.err, got, c.want)
		}
	}
	for err, want := range map[error]string{
		fmt.Errorf("%w: a task needs a title", tasks.ErrInvalid): "invalid_input: a task needs a title",
		fmt.Errorf("%w: 20 an hour", tasks.ErrLimit):             "limit: 20 an hour",
		fmt.Errorf("%w: runs are live", tasks.ErrAccountingMode): "accounting_mode: runs are live",
	} {
		if got := taskToolErr(err).Error(); got != want {
			t.Errorf("%q, want %q: the sentinel's words only repeat the code", got, want)
		}
	}
	if taskToolErr(nil) != nil {
		t.Error("no error must stay no error")
	}
	other := errors.New("tasks: reading the profile: disk I/O error")
	if got := taskToolErr(other); got != other {
		t.Errorf("an error that is not a refusal must pass as it is: %v", got)
	}
}

func TestDecodeTaskArgsRefusesWhatATaskToolDoesNotTake(t *testing.T) {
	type args struct {
		ID    taskIDArg `json:"id"`
		Title string    `json:"title"`
	}
	for _, raw := range []string{"", "null", " {} "} {
		var a args
		if err := decodeTaskArgs(json.RawMessage(raw), &a); err != nil || a.ID != 0 {
			t.Errorf("%q: %v", raw, err)
		}
	}
	var a args
	if err := decodeTaskArgs(json.RawMessage(`{"id": 3, "title": "T"}`), &a); err != nil || a.ID != 3 || a.Title != "T" {
		t.Errorf("the arguments it takes: %+v, %v", a, err)
	}
	for _, raw := range []string{`{"id": 3, "profile": "work"}`, `{"as": "you"}`, `{"ready": true}`, `{"title": 5}`, `[1]`, `"x"`} {
		var a args
		if err := decodeTaskArgs(json.RawMessage(raw), &a); err == nil || !strings.HasPrefix(err.Error(), "invalid_input: ") {
			t.Errorf("%s: %v, want an invalid_input refusal", raw, err)
		}
	}
	err := decodeTaskArgs(json.RawMessage(`{"profile": "work"}`), &args{})
	if err == nil || !strings.Contains(err.Error(), `"profile"`) || !strings.Contains(err.Error(), "one profile") {
		t.Errorf("a profile argument must be refused, saying the server serves one profile: %v", err)
	}
}

// A struct with an unexported field that carries a json tag must not panic the decoder: the field is
// not an argument, so naming it is refused like any other unknown one.
func TestDecodeTaskArgsSkipsUnexportedFields(t *testing.T) {
	// Built with reflect, since go vet refuses a json tag on an unexported field in source.
	typ := reflect.StructOf([]reflect.StructField{
		{Name: "ID", Type: reflect.TypeOf(taskIDArg(0)), Tag: `json:"id"`},
		{Name: "hidden", PkgPath: "mcp", Type: reflect.TypeOf(""), Tag: `json:"hidden"`},
	})
	dst := reflect.New(typ)
	if err := decodeTaskArgs(json.RawMessage(`{"id": 3}`), dst.Interface()); err != nil || dst.Elem().Field(0).Int() != 3 {
		t.Fatalf("the exported argument: %v, %v", dst.Elem(), err)
	}
	err := decodeTaskArgs(json.RawMessage(`{"hidden": "x"}`), dst.Interface())
	if err == nil || !strings.HasPrefix(err.Error(), "invalid_input: ") || !strings.Contains(err.Error(), `"hidden"`) {
		t.Errorf("an unexported field is not an argument: %v", err)
	}
}

func TestATaskIDIsANumberAsAModelWritesIt(t *testing.T) {
	for _, raw := range []string{`12`, `"12"`, `"#12"`, `" #12 "`} {
		var id taskIDArg
		if err := json.Unmarshal([]byte(raw), &id); err != nil || id != 12 {
			t.Errorf("%s: %d, %v", raw, id, err)
		}
	}
	for _, raw := range []string{`0`, `-1`, `"x"`, `1.5`, `true`, `"#"`} {
		var id taskIDArg
		if err := json.Unmarshal([]byte(raw), &id); err == nil {
			t.Errorf("%s: accepted as %d", raw, id)
		}
	}
	var none taskIDArg
	if err := json.Unmarshal([]byte(`null`), &none); err != nil {
		t.Fatal(err)
	}
	if _, err := none.need(); err == nil || !strings.HasPrefix(err.Error(), "invalid_input: id is required") {
		t.Errorf("no id: %v", err)
	}
}

func TestANumberArgumentMayBeAString(t *testing.T) {
	for raw, want := range map[string]numberArg{`20`: 20, `"20"`: 20, `" 7 "`: 7, `-5`: -5, `null`: 0} {
		var n numberArg
		if err := json.Unmarshal([]byte(raw), &n); err != nil || n != want {
			t.Errorf("%s: %d, %v; want %d", raw, n, err, want)
		}
	}
	for _, raw := range []string{`"x"`, `1.5`, `true`, `"20 minutes"`} {
		var n numberArg
		if err := json.Unmarshal([]byte(raw), &n); err == nil || !strings.Contains(err.Error(), "whole number") {
			t.Errorf("%s: %v, want a refusal that asks for a whole number", raw, err)
		}
	}
}

func TestAnOversizedIntegerIsRefusedWithItsRange(t *testing.T) {
	for _, raw := range []string{`99999999999999999999`, `"99999999999999999999"`, `-99999999999999999999`} {
		var n numberArg
		if err := json.Unmarshal([]byte(raw), &n); err == nil || !strings.Contains(err.Error(), "supported range") {
			t.Errorf("limit %s: %v, want a refusal that names the supported range", raw, err)
		}
		var id taskIDArg
		if err := json.Unmarshal([]byte(raw), &id); err == nil || !strings.Contains(err.Error(), "supported range") {
			t.Errorf("id %s: %v, want a refusal that names the supported range", raw, err)
		}
	}
}

// The pinned field lists above are checked against a populated fixture, which cannot show an
// omitempty field that is empty there. So the lists are also checked against the struct tags.
func TestThePinnedFieldListsMatchTheStructTags(t *testing.T) {
	tagKeys := func(v any) []string {
		rt := reflect.TypeOf(v)
		var keys []string
		for i := 0; i < rt.NumField(); i++ {
			tag := rt.Field(i).Tag.Get("json")
			if strings.Contains(tag, "omitempty") {
				t.Errorf("%s.%s is omitempty: a pinned list cannot see it when empty", rt.Name(), rt.Field(i).Name)
			}
			if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
				keys = append(keys, name)
			}
		}
		sort.Strings(keys)
		return keys
	}
	for _, c := range []struct {
		v    any
		want []string
	}{
		{taskView{}, []string{"author", "claim", "created_at", "id", "last_event", "notes_untrusted", "position", "profile_id",
			"source_app_untrusted", "source_kind", "source_title_untrusted", "source_url_untrusted", "status", "title_untrusted", "updated_at"}},
		{eventView{}, []string{"actor", "at", "from_status", "id", "kind", "note_untrusted", "to_status"}},
		{tasks.Claim{}, []string{"by", "stale", "until"}},
		{tasks.LastEvent{}, []string{"actor", "at", "kind"}},
	} {
		if got := tagKeys(c.v); !equalStrings(got, c.want) {
			t.Errorf("%T has json fields %v, want %v", c.v, got, c.want)
		}
	}
}

func TestAStatusArgumentIsOneColumnOrSeveral(t *testing.T) {
	for raw, want := range map[string]string{
		`"ready"`:                 "ready",
		`"ready, review"`:         "ready review",
		`"  ready ,\treview  ,"`:  "ready review",
		`[" ready", " done "]`:    "ready done",
		`["in-progress", "done"]`: "in_progress done",
		`"progress"`:              "in_progress",
		`""`:                      "",
		`[]`:                      "",
	} {
		var s statusArg
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		var names []string
		for _, st := range s {
			names = append(names, string(st))
		}
		if got := strings.Join(names, " "); got != want {
			t.Errorf("%s = %q, want %q", raw, got, want)
		}
	}
	for _, raw := range []string{`"someday"`, `5`, `["ready", 3]`} {
		var s statusArg
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			t.Errorf("%s: accepted as %v", raw, s)
		}
	}
}

// A refused value names its argument and is said in a model's words, not Go's: no "json: cannot
// unmarshal number into Go struct field", so the model gets one refusal it can act on.
func TestARefusedArgumentIsNamedInTheModelsWords(t *testing.T) {
	type args struct {
		ID     taskIDArg `json:"id"`
		Limit  numberArg `json:"limit"`
		Status statusArg `json:"status"`
		Text   string    `json:"text"`
		Next   bool      `json:"next"`
		Count  int       `json:"count"`
		Tags   []string  `json:"tags"`
	}
	for raw, want := range map[string]string{
		`{"limit": "x"}`:       "invalid_input: limit: expected a whole number, such as 20",
		`{"limit": true}`:      "invalid_input: limit: expected a whole number, such as 20",
		`{"limit": 20.5}`:      "invalid_input: limit: expected a whole number, such as 20",
		`{"id": "x"}`:          "invalid_input: id: expected a task's number, such as 12",
		`{"id": 0}`:            "invalid_input: id: expected a task's number, such as 12",
		`{"status": 5}`:        "invalid_input: status: expected a column name or a list of them",
		`{"text": 5}`:          "invalid_input: text: expected a string",
		`{"text": ["a"]}`:      "invalid_input: text: expected a string",
		`{"next": "yes"}`:      "invalid_input: next: expected true or false",
		`{"count": "x"}`:       "invalid_input: count: expected a whole number",
		`{"tags": 5}`:          "invalid_input: tags: expected another kind of value",
		`{"id": 3, "text": 5}`: "invalid_input: text: expected a string",
	} {
		if err := decodeTaskArgs(json.RawMessage(raw), &args{}); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", raw, err, want)
		}
	}
	// With several bad values the same one is named every time, the first by name, not whichever a
	// map gives first.
	for i := 0; i < 40; i++ {
		err := decodeTaskArgs(json.RawMessage(`{"text": 5, "next": "no", "limit": "x"}`), &args{})
		if want := "invalid_input: limit: expected a whole number, such as 20"; err == nil || err.Error() != want {
			t.Fatalf("three bad values: %v, want %q", err, want)
		}
	}
}

// The store's own refusal of a status reads the same whichever way it reaches the model: through
// the arguments of task_list or as an error of the store.
func TestAStatusRefusalReadsTheSameThroughEveryPath(t *testing.T) {
	type args struct {
		Status statusArg `json:"status"`
	}
	_, refusal := tasks.ParseStatus("someday")
	want := taskToolErr(refusal).Error()
	if !strings.HasPrefix(want, `invalid_input: unknown status "someday"`) || strings.Contains(want, "invalid input") {
		t.Fatalf("the store's refusal as a tool returns it: %q", want)
	}
	for _, raw := range []string{`{"status": "someday"}`, `{"status": "ready,someday"}`, `{"status": ["ready", "someday"]}`} {
		if err := decodeTaskArgs(json.RawMessage(raw), &args{}); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want the store's words once: %q", raw, err, want)
		}
	}
}

// A model that sends several arguments a tool does not take is told about all of them, in order,
// and what the tool does take.
func TestEveryUnknownArgumentIsNamedWithThoseTheToolTakes(t *testing.T) {
	type args struct {
		Title string    `json:"title"`
		ID    taskIDArg `json:"id"`
	}
	const why = " (this server serves one profile and names its caller itself)"
	for name, c := range map[string]struct{ raw, want string }{
		"one": {`{"profile": "work"}`,
			`invalid_input: "profile" is not an argument of this tool: it takes only id, title` + why},
		"every one, in order": {`{"ready": true, "as": "you", "profile": "work", "id": 3, "Title": "T"}`,
			`invalid_input: "Title", "as", "profile", "ready" are not arguments of this tool: it takes only id, title` + why},
		"a name in the wrong case": {`{"ID": 3}`,
			`invalid_input: "ID" is not an argument of this tool: it takes only id, title` + why},
	} {
		for i := 0; i < 40; i++ { // a map's order is random: one try would prove nothing
			if err := decodeTaskArgs(json.RawMessage(c.raw), &args{}); err == nil || err.Error() != c.want {
				t.Fatalf("%s: %v, want %q", name, err, c.want)
			}
		}
	}
	err := decodeTaskArgs(json.RawMessage(`{"profile": "work"}`), &struct{}{})
	if want := `invalid_input: "profile" is not an argument of this tool: it takes no arguments` + why; err == nil || err.Error() != want {
		t.Errorf("a tool that takes none: %v, want %q", err, want)
	}
}

// Some clients keep every number as a float, so 12 arrives as 12.0 or 1.2e1. That is still the
// whole number 12, exactly (read as a float, 9007199254740993.0 would be 9007199254740992); a
// fraction is not a whole number.
func TestAWholeNumberMayBeWrittenAsAFloat(t *testing.T) {
	for _, raw := range []string{`12.0`, `1.2e1`, `1.2E+1`, `120e-1`, `"12.0"`, `"1.2e1"`, `" 12.0 "`, `"+12.0"`} {
		var n numberArg
		if err := json.Unmarshal([]byte(raw), &n); err != nil || n != 12 {
			t.Errorf("a number of %s: %d, %v; want 12", raw, n, err)
		}
	}
	for _, raw := range []string{`12.0`, `1.2e1`, `"12.0"`, `"#12.0"`, `" #1.2e1 "`} {
		var id taskIDArg
		if err := json.Unmarshal([]byte(raw), &id); err != nil || id != 12 {
			t.Errorf("an id of %s: %d, %v; want 12", raw, id, err)
		}
	}
	for raw, want := range map[string]numberArg{
		`-5.0`: -5, `-0.0`: 0, `9007199254740993.0`: 9007199254740993, `1e18`: 1000000000000000000,
		`9223372036854775807`: 9223372036854775807, `9223372036854775807.0`: 9223372036854775807, `-9223372036854775808`: -9223372036854775808,
	} {
		var n numberArg
		if err := json.Unmarshal([]byte(raw), &n); err != nil || n != want {
			t.Errorf("a number of %s: %d, %v; want %d", raw, n, err, want)
		}
	}
	for _, raw := range []string{`12.5`, `"12.5"`, `0.1`, `1e-1`, `1e-400`, `20.000000000000000001`, `9223372036854775808`, `1e19`, `1e30`, `1e999`, `"1e999"`,
		`"000000000000000000012"`, `"12.000000000000000000000"`, `"0e1000"`, `"0e999999"`, `"1_2"`, `"0x1p4"`, `"4/2"`, `"Inf"`, `"NaN"`, `".5"`, `"5."`, `"1e"`, `"12.0.0"`} {
		var n numberArg
		if err := json.Unmarshal([]byte(raw), &n); err == nil || !strings.Contains(err.Error(), "whole number") {
			t.Errorf("a number of %s: %d, %v; want a refusal that asks for a whole number", raw, n, err)
		}
		var id taskIDArg
		if err := json.Unmarshal([]byte(raw), &id); err == nil {
			t.Errorf("an id of %s was taken as %d", raw, id)
		}
	}
	for _, raw := range []string{`0.0`, `-0.0`, `-1.0`, `"#0.0"`} { // a float is no way round an id being positive
		var id taskIDArg
		if err := json.Unmarshal([]byte(raw), &id); err == nil {
			t.Errorf("an id of %s was taken as %d", raw, id)
		}
	}
}

// What names an argument is the json tag of a field, up to its options: a field with no name or
// with "-" there takes none.
func TestOnlyTheTagsOfADstAreArguments(t *testing.T) {
	type args struct {
		ID      taskIDArg `json:"id"`
		Note    string    `json:"note,omitempty"`
		Plain   string
		Skipped string `json:"-"`
	}
	var a args
	if err := decodeTaskArgs(json.RawMessage(`{"id": 4, "note": "n"}`), &a); err != nil || a.ID != 4 || a.Note != "n" {
		t.Errorf("the arguments it takes: %+v, %v", a, err)
	}
	for _, name := range []string{"", "-", "Plain", "Skipped", "note,omitempty", "ID"} {
		raw, _ := json.Marshal(map[string]string{name: "x"})
		want := fmt.Sprintf("invalid_input: %q is not an argument of this tool: it takes only id, note", name)
		if err := decodeTaskArgs(raw, &args{}); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: %v, want it to start %q", raw, err, want)
		}
	}
}

// Models often send null for an argument they do not use: it is no argument, whatever its kind.
func TestANullArgumentIsNoArgument(t *testing.T) {
	type args struct {
		ID     taskIDArg `json:"id"`
		Limit  numberArg `json:"limit"`
		Status statusArg `json:"status"`
		Text   string    `json:"text"`
		Next   bool      `json:"next"`
	}
	a := args{ID: 3, Limit: 4, Text: "keep", Next: true}
	if err := decodeTaskArgs(json.RawMessage(`{"id": null, "limit": null, "status": null, "text": null, "next": null}`), &a); err != nil {
		t.Fatalf("null arguments: %v", err)
	}
	if a.ID != 3 || a.Limit != 4 || len(a.Status) != 0 || a.Text != "keep" || !a.Next {
		t.Errorf("null arguments changed what was there: %+v", a)
	}
}

// A dst that is no pointer to a struct is the caller's mistake: it is returned as one, never
// panicked on, and never blamed on the model.
func TestADstThatIsNotAPointerToAStructIsRefused(t *testing.T) {
	type args struct {
		ID taskIDArg `json:"id"`
	}
	var n int
	var absent *args
	for name, dst := range map[string]any{"nil": nil, "a struct, not its address": args{}, "a pointer to an int": &n, "a nil pointer": absent} {
		for _, raw := range []string{`{"id": 3}`, ``, `null`} {
			err := decodeTaskArgs(json.RawMessage(raw), dst)
			if err == nil || !strings.Contains(err.Error(), "pointer to a struct") || strings.HasPrefix(err.Error(), "invalid_input") {
				t.Errorf("%s with %q: %v, want a refusal that blames the caller, not the model", name, raw, err)
			}
		}
	}
}
