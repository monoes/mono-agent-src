package recording

import (
	"encoding/json"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
)

func TestSensitiveTarget(t *testing.T) {
	yes := []*Fingerprint{
		{Sensitive: true, InputType: "text"},
		{InputType: "PASSWORD"},
		{InputType: "hidden"},
		{Autocomplete: "billing cc-number"},
		{Autocomplete: "one-time-code"},
		{Name: "user_password"},
		{Label: "PIN"},
		{ID: "cvv"},
		{AriaName: "SSN"},
		{Placeholder: "API token"},
	}
	for _, fp := range yes {
		if !SensitiveTarget(fp) {
			t.Errorf("%+v not sensitive", fp)
		}
	}
	for _, fp := range []*Fingerprint{nil, {Name: "email"}, {Label: "Shipping address"}, {Name: "spinner"}} {
		if SensitiveTarget(fp) {
			t.Errorf("%+v sensitive", fp)
		}
	}
}

func TestSanitizeHonoursRecorderFlag(t *testing.T) {
	ev := &Event{ID: "e1", Type: EvType, Value: "1234", URL: "https://x.test/a#access_token=abc",
		Target: &Fingerprint{Tag: "input", Sensitive: true}}
	Sanitize(ev)
	if ev.Value != "" || !ev.Masked || ev.URL != "https://x.test/a" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestSanitizeURL(t *testing.T) {
	cases := map[string]string{
		"https://x.test/cb?code=abc&state=xyz&page=2":        "https://x.test/cb?code=REDACTED&state=REDACTED&page=2",
		"https://x.test/a?X-Amz-Signature=f00&id=7":          "https://x.test/a?X-Amz-Signature=REDACTED&id=7",
		"https://x.test/reset?reset_token=t&client_secret=s": "https://x.test/reset?reset_token=REDACTED&client_secret=REDACTED",
		"https://x.test/a#access_token=abc":                  "https://x.test/a",
		"https://x.test/a#section-2":                         "https://x.test/a",
		"https://x.test/app#/inbox/42":                       "https://x.test/app#/inbox/42",
		"https://x.test/app#/cb?token=1":                     "https://x.test/app",
		"https://x.test/q?q=hello+world&magic=m&password=p":  "https://x.test/q?q=hello+world&magic=REDACTED&password=REDACTED",
		"https://x.test/plain":                               "https://x.test/plain",
		"":                                                   "",
	}
	for in, want := range cases {
		if got := SanitizeURL(in); got != want {
			t.Errorf("SanitizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScrubHTMLTokenizer(t *testing.T) {
	src := `<div data-token="t0k" data-user-id=9>` +
		`<INPUT Value='hunter2' TYPE = "Password">` +
		`<input value=keep name=q>` +
		`<input style="-webkit-text-security: disc" value="masked1">` +
		`<input aria-label="One-time PIN" value="998877">` +
		`<textarea name="api_token">tok-in-text</textarea>` +
		`<a href="https://x.test/l?token=abc&x=1#frag">link</a>` +
		`<script>var secret="s3"</script>` +
		`<form action="/login?session=zz"></form></div>`
	got := ScrubHTML(src, false)
	for _, leak := range []string{"hunter2", "masked1", "998877", "tok-in-text", "t0k", "data-user-id", "token=abc", "#frag", "s3", "session=zz"} {
		if strings.Contains(got, leak) {
			t.Errorf("scrubbed snippet still has %q: %s", leak, got)
		}
	}
	for _, kept := range []string{`value="keep"`, "token=REDACTED", "x=1", "link"} {
		if !strings.Contains(got, kept) {
			t.Errorf("scrubbed snippet lost %q: %s", kept, got)
		}
	}
	if all := ScrubHTML(`<input value="a"><textarea>b</textarea>`, true); strings.Contains(all, `"a"`) || strings.Contains(all, ">b<") {
		t.Errorf("allValues kept a value: %s", all)
	}
}

func TestStartFrameCapsAndSanitises(t *testing.T) {
	in, _, _ := newIngest(t)
	long := strings.Repeat("g", MaxGoalRunes+50)
	mustHandle(t, in, Frame{Op: OpStart, RecordingID: "r", URL: "https://x.test/cb?code=abc#tok", Title: strings.Repeat("t", MaxTitleRunes+9), Goal: long})
	mustHandle(t, in, Frame{Op: OpNetwork, RecordingID: "r", Net: &NetEntry{Method: "GET", URL: "https://api.x.test/v1?access_token=zzz"}})
	mustHandle(t, in, Frame{Op: OpEvent, RecordingID: "r", Event: ev("e1", 1, EvClick)})
	out := mustHandle(t, in, Frame{Op: OpStop, RecordingID: "r"})
	res, err := in.Finalize(out.Stopped)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := capture.ReadMeta(res.Path)
	if meta.URL != "https://x.test/cb?code=REDACTED" || len([]rune(meta.Title)) != MaxTitleRunes || len([]rune(extraString(meta, ExtraGoal))) != MaxGoalRunes {
		t.Fatalf("meta url %q title %d goal %d", meta.URL, len(meta.Title), len(extraString(meta, ExtraGoal)))
	}
	net, _ := os.ReadFile(filepath.Join(res.Path, NetworkArtifact))
	if strings.Contains(string(net), "zzz") {
		t.Fatalf("network.jsonl leaks a token: %s", net)
	}
	// Envelopes are private to the user.
	if fi, _ := os.Stat(res.Path); fi.Mode().Perm() != 0o700 {
		t.Fatalf("envelope dir mode %o", fi.Mode().Perm())
	}
	for _, name := range []string{capture.MetaFile, EventsArtifact, NetworkArtifact} {
		if fi, err := os.Stat(filepath.Join(res.Path, name)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode wrong: %v", name, err)
		}
	}
}

func TestFinalizePrunesOldestRecordings(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	inbox := filepath.Join(t.TempDir(), "inbox")
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var dirs []string
	for i := 0; i < 4; i++ {
		dirs = append(dirs, writeRecording(t, inbox, "rec-"+string(rune('a'+i)), base.Add(time.Duration(i)*time.Hour), 1))
	}
	// The oldest is linked to an automation: an unlinked one goes first.
	if err := SetAutomation(dirs[0], "acme"); err != nil {
		t.Fatal(err)
	}
	in := &Ingest{Writer: &capture.Writer{Inbox: inbox}, MaxRecordings: 3}
	mustHandle(t, in, Frame{Op: OpStart, RecordingID: "rec-new", URL: "https://example.com/n", StartedAt: base.Add(9 * time.Hour).UnixMilli()})
	mustHandle(t, in, Frame{Op: OpEvent, RecordingID: "rec-new", Event: ev("e1", 1, EvClick)})
	out := mustHandle(t, in, Frame{Op: OpStop, RecordingID: "rec-new"})
	res, err := in.Finalize(out.Stopped)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 2 || !strings.Contains(strings.Join(res.Warnings, "|"), "keeps at most 3") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	for i, want := range []bool{true, false, false, true} {
		_, err := os.Stat(dirs[i])
		if exists := err == nil; exists != want {
			t.Errorf("recording %d exists=%v, want %v", i, exists, want)
		}
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("the new recording was pruned: %v", err)
	}
}

func TestZeroEventRecordingIsDiscarded(t *testing.T) {
	in, inbox, c := newIngest(t)
	mustHandle(t, in, Frame{Op: OpStart, RecordingID: "r", URL: "https://example.com/"})
	out := mustHandle(t, in, Frame{Op: OpStop, RecordingID: "r"})
	if res, err := in.Finalize(out.Stopped); !errors.Is(err, ErrNoEvents) || res != nil {
		t.Fatalf("Finalize(empty) = %v, %v", res, err)
	}
	// Idle-reaped with nothing recorded: also discarded.
	mustHandle(t, in, Frame{Op: OpStart, RecordingID: "r2", URL: "https://example.com/"})
	c.t = c.t.Add(time.Hour)
	for _, st := range in.Reap() {
		if _, err := in.Finalize(st); !errors.Is(err, ErrNoEvents) {
			t.Fatalf("reaped empty recording: %v", err)
		}
	}
	if entries, _ := capture.List(inbox); len(entries) != 0 {
		t.Fatalf("empty recordings landed: %+v", entries)
	}
	if spools, _ := filepath.Glob(filepath.Join(inbox, spoolPrefix+"*")); len(spools) != 0 {
		t.Fatalf("spools left: %v", spools)
	}
}

func TestListHidesIncompleteEmptyRecordings(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	inbox := filepath.Join(t.TempDir(), "inbox")
	SetInboxes(inbox)
	t.Cleanup(func() { SetInboxes() })
	writeRecording(t, inbox, "rec-ok", time.Now(), 1)
	// An old-style empty, incomplete envelope written before discarding.
	if _, err := (&capture.Writer{Inbox: inbox}).Write(&capture.Envelope{
		Meta: capture.Meta{URL: "https://example.com/empty", Source: SourceRecording, Extra: map[string]json.RawMessage{
			ExtraRecordingID: json.RawMessage(`"rec-empty"`), ExtraEventCount: json.RawMessage(`0`), ExtraComplete: json.RawMessage(`false`)}},
		Artifacts: map[string]capture.Artifact{EventsArtifact: capture.Inline(nil)}}); err != nil {
		t.Fatal(err)
	}
	list, err := List()
	if err != nil || len(list) != 1 || list[0].Title != "rec-ok" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if _, err := Find("rec-empty"); err != nil {
		t.Fatalf("hidden recording not findable for delete: %v", err)
	}
}

func TestScrubHTMLDropsHandlersAndScriptURLs(t *testing.T) {
	src := `<div><button type="button" onclick="fetch('/api/items',{headers:{Authorization:'Bearer sekrit-hdr'}})" ONMOUSEOVER='x("sekrit-hover")'>Load</button>` +
		`<a href="java&#9;script:void('sekrit-link')">Run</a><a href=" JavaScript:alert(1)">x</a>` +
		`<form action="vbscript:msgbox('sekrit-vb')"></form><svg><a xlink:href="javascript:alert('sekrit-svg')"></a></svg>` +
		`<a href="https://ok.test/a">Ok</a></div>`
	got := ScrubHTML(src, false)
	for _, gone := range []string{"sekrit", "onclick", "ONMOUSEOVER", "onmouseover", "script:", "Authorization"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q left in %s", gone, got)
		}
	}
	for _, kept := range []string{`<button type="button">Load</button>`, `href="https://ok.test/a"`, ">Run</a>"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q missing from %s", kept, got)
		}
	}
}
