package recording

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
)

// clock is a settable time source for the idle reaper.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newIngest(t *testing.T) (*Ingest, string, *clock) {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	inbox := filepath.Join(t.TempDir(), "inbox")
	c := &clock{t: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)}
	return &Ingest{Writer: &capture.Writer{Inbox: inbox, Now: c.now}, Now: c.now}, inbox, c
}

func mustHandle(t *testing.T, in *Ingest, f Frame) Outcome {
	t.Helper()
	f.Kind = "recording"
	out, err := in.Handle(&f)
	if err != nil {
		t.Fatalf("Handle(%s): %v", f.Op, err)
	}
	return out
}

func ev(id string, seq int, typ string) *Event {
	return &Event{ID: id, Seq: seq, Type: typ, URL: "https://example.com/a"}
}

func TestIngestStartEventSnapshotStopWritesEnvelope(t *testing.T) {
	in, inbox, _ := newIngest(t)
	mustHandle(t, in, Frame{Op: OpStart, RecordingID: "rec-1", TabID: 7, URL: "https://example.com/a", Title: "Example", Goal: "make a contact", StartedAt: time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC).UnixMilli()})
	// Out of order and duplicated: the envelope sorts by seq and dedups by id.
	mustHandle(t, in, Frame{Op: OpEvent, RecordingID: "rec-1", Event: ev("e2", 2, EvType)})
	mustHandle(t, in, Frame{Op: OpEvent, RecordingID: "rec-1", Event: ev("e1", 1, EvClick)})
	if out := mustHandle(t, in, Frame{Op: OpEvent, RecordingID: "rec-1", Event: ev("e1", 1, EvClick)}); out.Dropped == "" {
		t.Fatalf("duplicate event not reported as dropped")
	}
	mustHandle(t, in, Frame{Op: OpSnapshot, RecordingID: "rec-1", EventID: "e1", Name: "dom-e1.html", Data: "<button>Go</button>"})
	mustHandle(t, in, Frame{Op: OpNetwork, RecordingID: "rec-1", Net: &NetEntry{Method: "GET", URL: "https://example.com/api", Status: 200}})
	out := mustHandle(t, in, Frame{Op: OpStop, RecordingID: "rec-1", Reason: "user"})
	if out.Stopped == nil {
		t.Fatal("stop did not detach the recording")
	}
	res, err := in.Finalize(out.Stopped)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if filepath.Dir(res.Path) != inbox {
		t.Fatalf("envelope at %s, want inside %s", res.Path, inbox)
	}
	meta, err := capture.ReadMeta(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Source != SourceRecording || meta.URL != "https://example.com/a" || meta.Title != "Example" {
		t.Fatalf("meta = %+v", meta)
	}
	if extraString(meta, ExtraGoal) != "make a contact" || extraInt(meta, ExtraTabID) != 7 ||
		extraString(meta, ExtraStopReason) != "user" || extraInt(meta, ExtraEventCount) != 2 ||
		!extraBool(meta, ExtraComplete) || extraString(meta, ExtraRecordingID) != "rec-1" {
		t.Fatalf("meta extra = %v", meta.Extra)
	}
	if meta.CapturedAt != "2026-09-25T09:00:00Z" {
		t.Fatalf("capturedAt = %s", meta.CapturedAt)
	}
	events, err := readEvents(filepath.Join(res.Path, EventsArtifact))
	if err != nil || len(events) != 2 || events[0].ID != "e1" || events[1].ID != "e2" {
		t.Fatalf("events = %+v, %v", events, err)
	}
	for _, name := range []string{"dom-e1.html", NetworkArtifact} {
		if _, err := os.Stat(filepath.Join(res.Path, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if spools, _ := filepath.Glob(filepath.Join(inbox, spoolPrefix+"*")); len(spools) != 0 {
		t.Fatalf("spool left behind: %v", spools)
	}
	// A late frame for a finished recording is refused, not a new recording.
	if _, err := in.Handle(&Frame{Op: OpEvent, RecordingID: "rec-1", Event: ev("e3", 3, EvClick)}); err == nil {
		t.Fatal("late frame accepted")
	}
}

func TestIngestRejectsBadIdentifiers(t *testing.T) {
	in, _, _ := newIngest(t)
	bad := []Frame{
		{Op: OpStart, RecordingID: "../x"},
		{Op: OpStart, RecordingID: ""},
		{Op: "explode", RecordingID: "r"},
		{Op: OpEvent, RecordingID: "r", Event: &Event{ID: "../../e", Seq: 1}},
		{Op: OpEvent, RecordingID: "r"},
		{Op: OpSnapshot, RecordingID: "r", EventID: "e1", Name: "../dom-e1.html", Data: "x"},
		{Op: OpSnapshot, RecordingID: "r", EventID: "e1/x", Data: "x"},
		{Op: OpSnapshot, RecordingID: "r", EventID: "e1", Name: "meta.json", Data: "x"},
		{Op: OpNetwork, RecordingID: "r"},
	}
	for _, f := range bad {
		f := f
		if _, err := in.Handle(&f); err == nil {
			t.Errorf("frame %+v accepted", f)
		}
	}
}

func TestIngestReenforcesPrivacy(t *testing.T) {
	in, _, _ := newIngest(t)
	mustHandle(t, in, Frame{Op: OpStart, RecordingID: "r", URL: "https://example.com/login"})
	pw := ev("e1", 1, EvType)
	pw.Value = "hunter2"
	pw.Target = &Fingerprint{Tag: "input", InputType: "password"}
	cc := ev("e2", 2, EvType)
	cc.Value = "4111111111111111"
	cc.Target = &Fingerprint{Tag: "input", InputType: "text", Autocomplete: "billing cc-number"}
	plain := ev("e3", 3, EvType)
	plain.Value = "alice"
	plain.Target = &Fingerprint{Tag: "input", InputType: "text"}
	for _, e := range []*Event{pw, cc, plain} {
		mustHandle(t, in, Frame{Op: OpEvent, RecordingID: "r", Event: e})
	}
	form := `<form><input type="password" value="hunter2" name=p><input name="u" value="alice"></form>`
	mustHandle(t, in, Frame{Op: OpSnapshot, RecordingID: "r", EventID: "e1", Data: form})
	mustHandle(t, in, Frame{Op: OpSnapshot, RecordingID: "r", EventID: "e3", Data: form})
	out := mustHandle(t, in, Frame{Op: OpStop, RecordingID: "r"})
	res, err := in.Finalize(out.Stopped)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(res.Path, EventsArtifact))
	if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "4111") {
		t.Fatalf("secret reached events.jsonl: %s", raw)
	}
	events, _ := readEvents(filepath.Join(res.Path, EventsArtifact))
	if !events[0].Masked || !events[1].Masked || events[2].Masked || events[2].Value != "alice" {
		t.Fatalf("masking wrong: %+v", events)
	}
	// The snippet of an event on a sensitive field loses every value; any
	// other snippet loses only the sensitive inputs' values.
	if dom, _ := DOMSnippet(res.Path, "e1"); strings.Contains(dom, "hunter2") || strings.Contains(dom, "alice") {
		t.Fatalf("sensitive event's snippet kept a value: %s", dom)
	}
	if dom, _ := DOMSnippet(res.Path, "e3"); strings.Contains(dom, "hunter2") || !strings.Contains(dom, `value="alice"`) {
		t.Fatalf("plain event's snippet scrubbed wrong: %s", dom)
	}
}

func TestIngestCapsDropWithWarning(t *testing.T) {
	in, _, _ := newIngest(t)
	mustHandle(t, in, Frame{Op: OpStart, RecordingID: "r", URL: "https://example.com/"})
	for i := 1; i <= MaxEvents+3; i++ {
		id := "e" + strconv.Itoa(i)
		mustHandle(t, in, Frame{Op: OpEvent, RecordingID: "r", Event: ev(id, i, EvClick)})
	}
	big := strings.Repeat("x", MaxSnapshotBytes+1)
	if out := mustHandle(t, in, Frame{Op: OpSnapshot, RecordingID: "r", EventID: "e1", Data: big}); out.Dropped == "" {
		t.Fatal("oversize snapshot not dropped")
	}
	out := mustHandle(t, in, Frame{Op: OpStop, RecordingID: "r"})
	res, err := in.Finalize(out.Stopped)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := capture.ReadMeta(res.Path)
	if extraInt(meta, ExtraEventCount) != MaxEvents {
		t.Fatalf("eventCount = %d", extraInt(meta, ExtraEventCount))
	}
	var warnings []string
	_ = json.Unmarshal(meta.Extra[ExtraWarnings], &warnings)
	joined := strings.Join(warnings, "|")
	if !strings.Contains(joined, "dropped 3 event") || !strings.Contains(joined, "dropped 1 DOM snapshot") {
		t.Fatalf("warnings = %v", warnings)
	}
	if _, err := os.Stat(filepath.Join(res.Path, "dom-e1.html")); !os.IsNotExist(err) {
		t.Fatalf("oversize snapshot was written")
	}
}

func TestIngestIdleReaperFinalisesIncomplete(t *testing.T) {
	in, _, c := newIngest(t)
	mustHandle(t, in, Frame{Op: OpStart, RecordingID: "r", URL: "https://example.com/"})
	mustHandle(t, in, Frame{Op: OpEvent, RecordingID: "r", Event: ev("e1", 1, EvClick)})
	c.t = c.t.Add(29 * time.Minute)
	if got := in.Reap(); len(got) != 0 {
		t.Fatalf("reaped too early: %d", len(got))
	}
	c.t = c.t.Add(2 * time.Minute)
	got := in.Reap()
	if len(got) != 1 || in.Active() != 0 {
		t.Fatalf("reap = %d, active = %d", len(got), in.Active())
	}
	res, err := in.Finalize(got[0])
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := capture.ReadMeta(res.Path)
	if extraBool(meta, ExtraComplete) || extraString(meta, ExtraStopReason) != StopIdle {
		t.Fatalf("meta extra = %v", meta.Extra)
	}
}

func TestIngestRecoverAndAdoptSpool(t *testing.T) {
	in, inbox, c := newIngest(t)
	mustHandle(t, in, Frame{Op: OpStart, RecordingID: "r", URL: "https://example.com/", Goal: "g"})
	mustHandle(t, in, Frame{Op: OpEvent, RecordingID: "r", Event: ev("e1", 1, EvClick)})

	// A new process: the spool is adopted, the event already there counts.
	in2 := &Ingest{Writer: in.Writer, Now: c.now}
	mustHandle(t, in2, Frame{Op: OpEvent, RecordingID: "r", Event: ev("e1", 1, EvClick)})
	mustHandle(t, in2, Frame{Op: OpEvent, RecordingID: "r", Event: ev("e2", 2, EvClick)})

	// A third process after a long gap: Recover finalises it.
	in3 := &Ingest{Writer: in.Writer, Now: func() time.Time { return time.Now().Add(time.Hour) }}
	stopped := in3.Recover(inbox)
	if len(stopped) != 1 {
		t.Fatalf("recovered %d", len(stopped))
	}
	res, err := in3.Finalize(stopped[0])
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := capture.ReadMeta(res.Path)
	if extraInt(meta, ExtraEventCount) != 2 || extraString(meta, ExtraStopReason) != StopRecovered || extraString(meta, ExtraGoal) != "g" {
		t.Fatalf("meta extra = %v", meta.Extra)
	}
}

func TestIngestFrameWithoutStartStillLands(t *testing.T) {
	in, _, _ := newIngest(t)
	mustHandle(t, in, Frame{Op: OpEvent, RecordingID: "r", Event: ev("e1", 1, EvClick)})
	out := mustHandle(t, in, Frame{Op: OpStop, RecordingID: "r"})
	res, err := in.Finalize(out.Stopped)
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta.URL != "https://example.com/a" {
		t.Fatalf("url = %s", res.Meta.URL)
	}
}
