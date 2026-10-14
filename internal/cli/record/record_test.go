package record

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/recording"
)

// recordTestHome isolates HOME and the capture inbox, lands the named
// recordings in the unprofiled recording store through the real ingest, and
// returns that store.
func recordTestHome(t *testing.T, ids ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// The capture inbox is isolated too, though recordings never go there.
	t.Setenv(capture.InboxEnv, filepath.Join(home, "inbox"))
	t.Cleanup(func() { _ = recording.SetProfile("") })
	store, err := recording.StoreDir("")
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		in := &recording.Ingest{}
		started := time.Date(2026, 9, 25, 8+i, 0, 0, 0, time.UTC).UnixMilli()
		frames := []recording.Frame{
			{Op: recording.OpStart, RecordingID: id, URL: "https://example.com/" + id, Title: id, Goal: "goal " + id, StartedAt: started},
			{Op: recording.OpEvent, RecordingID: id, Event: &recording.Event{ID: "e1", Seq: 1, Type: recording.EvClick, URL: "https://example.com/" + id,
				Target: &recording.Fingerprint{Tag: "button", Text: "Go", Candidates: []recording.Candidate{}}}},
			{Op: recording.OpSnapshot, RecordingID: id, EventID: "e1", Data: "<button>Go</button>"},
		}
		for _, f := range frames {
			f := f
			if _, err := in.Handle(&f); err != nil {
				t.Fatal(err)
			}
		}
		out, err := in.Handle(&recording.Frame{Op: recording.OpStop, RecordingID: id, Reason: "user"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := in.Finalize(out.Stopped); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func runRecordCLI(t *testing.T, jsonOut bool, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(newTestDeps(t, jsonOut))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestRecordListShowDeleteJSON(t *testing.T) {
	recordTestHome(t, "rec-a", "rec-b")

	out, err := runRecordCLI(t, true, "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	var list struct {
		Recordings []recording.Summary `json:"recordings"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("list JSON: %v\n%s", err, out)
	}
	if len(list.Recordings) != 2 || list.Recordings[0].Title != "rec-b" || list.Recordings[0].Goal != "goal rec-b" ||
		list.Recordings[0].Events != 1 || !list.Recordings[0].Complete {
		t.Fatalf("list = %+v", list.Recordings)
	}

	out, err = runRecordCLI(t, true, "show", "rec-a")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	var show struct {
		Summary   recording.Summary `json:"summary"`
		Events    []recording.Event `json:"events"`
		Artifacts []string          `json:"artifacts"`
	}
	if err := json.Unmarshal([]byte(out), &show); err != nil {
		t.Fatalf("show JSON: %v\n%s", err, out)
	}
	if show.Summary.Title != "rec-a" || len(show.Events) != 1 || len(show.Artifacts) != 2 {
		t.Fatalf("show = %+v", show)
	}

	out, err = runRecordCLI(t, true, "delete", "rec-a")
	if err != nil || out != "{\n  \"ok\": true\n}\n" {
		t.Fatalf("delete: %v %q", err, out)
	}
	if left, _ := recording.List(); len(left) != 1 {
		t.Fatalf("after delete: %+v", left)
	}
}

func TestRecordJSONErrors(t *testing.T) {
	recordTestHome(t)
	out, err := runRecordCLI(t, true, "show", "missing")
	if err == nil {
		t.Fatal("show of a missing recording succeeded")
	}
	var body map[string]string
	if json.Unmarshal([]byte(out), &body) != nil || body["error"] == "" {
		t.Fatalf("error JSON = %q", out)
	}
	out, err = runRecordCLI(t, true, "list")
	if err != nil || out != "{\n  \"recordings\": []\n}\n" {
		t.Fatalf("empty list: %v %q", err, out)
	}
}

func TestRecordHumanOutput(t *testing.T) {
	recordTestHome(t, "rec-a")
	out, err := runRecordCLI(t, false, "list")
	if err != nil || !bytes.Contains([]byte(out), []byte("rec-a")) {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = runRecordCLI(t, false, "show", "rec-a")
	if err != nil || !bytes.Contains([]byte(out), []byte(`<button> "Go"`)) {
		t.Fatalf("show: %v\n%s", err, out)
	}
	if _, err := runRecordCLI(t, false, "delete", "rec-a", "--yes"); err != nil {
		t.Fatal(err)
	}
}

func TestRecordShowMissingPrintsOneJSONError(t *testing.T) {
	recordTestHome(t)
	out, err := runRecordCLI(t, true, "show", "nope")
	if err == nil {
		t.Fatal("show nope succeeded")
	}
	if want := "{\"error\":\"recording not found: nope\"}\n"; out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
}

func TestRecordJSONErrorsWrapIsIdempotent(t *testing.T) {
	d := newTestDeps(t, true)
	cmd := NewCommand(d)
	for _, sub := range cmd.Commands() {
		if sub.RunE == nil {
			continue
		}
		if sub.Annotations[recordJSONErrorsAnnotation] == "" {
			t.Errorf("record %s is not wrapped", sub.Name())
		}
		withRecordJSONErrors(d, sub) // a second pass must not wrap again
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	t.Setenv("HOME", t.TempDir())
	t.Setenv(capture.InboxEnv, t.TempDir())
	cmd.SetArgs([]string{"show", "nope"})
	_ = cmd.Execute()
	if n := strings.Count(out.String(), `"error"`); n != 1 {
		t.Fatalf("error printed %d times: %q", n, out.String())
	}
}
