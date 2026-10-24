package monomind

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Golden tests against output recorded from a real monomind 2.24.1 sections
// org (testdata/monomind-2.24.1, see its README for which files are real).
// A fake `monomind` replays the files, so the exported client functions run
// unchanged against the real shapes. Re-record them, and re-run this, when
// the supported monomind version moves. scripts/monomind-golden-check.sh
// records a fresh set from any installed monomind into a temp folder and
// runs these same tests on it through MONOMIND_GOLDEN_DIR.

const goldenDir = "monomind-2.24.1"

func goldenPath(name string) string {
	dir := os.Getenv("MONOMIND_GOLDEN_DIR")
	if dir == "" {
		dir = filepath.Join("testdata", goldenDir)
	}
	p, _ := filepath.Abs(filepath.Join(dir, name))
	return p
}

func goldenBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(goldenPath(name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// goldenMonomind installs a fake monomind that answers like the recorded
// one: handshake, org list/status/validate, and org events from events.
func goldenMonomind(t *testing.T, list, status, events string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake monomind is a shell script")
	}
	abs := goldenPath("")
	script := `#!/bin/sh
d='` + abs + `'
if [ "$1" = "--version" ]; then cat "$d/version.json"; exit 0; fi
[ "$1" = "org" ] || exit 2
case "$2" in
  list) cat "$d/` + list + `" ;;
  status) cat "$d/` + status + `" ;;
  events) cat "$d/` + events + `" ;;
  validate)
    if [ "$3" = "bad" ]; then cat "$d/validate-invalid.txt"; exit 1; fi
    cat "$d/validate-valid.txt" ;;
  *) exit 2 ;;
esac
`
	bin := filepath.Join(t.TempDir(), "monomind")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvOverride, bin)
}

func TestGolden_Handshake(t *testing.T) {
	var vi VersionInfo
	if err := json.Unmarshal(goldenBytes(t, "version.json"), &vi); err != nil {
		t.Fatal(err)
	}
	if vi.V != ProtocolVersion || BelowKnownGood(vi.Version) {
		t.Fatalf("handshake = v%d %s, want protocol %d and monomind >= %s", vi.V, vi.Version, ProtocolVersion, KnownGoodMonomindVersion)
	}
	if !vi.HasCapability("org-json-v1") || !vi.HasCapability(CapDoctorJSON) {
		t.Errorf("capabilities %v lack org-json-v1 or doctor-json", vi.Capabilities)
	}
	if KnownGoodAdvisory(vi.Version) != "" {
		t.Error("the recorded version must not trigger the older-than-known-good advisory")
	}
}

func TestGolden_OrgListKeepsRealItems(t *testing.T) {
	type item struct {
		Name     string  `json:"name"`
		Roles    int     `json:"roles"`
		Schedule *string `json:"schedule"`
		Status   string  `json:"status"`
		Goal     string  `json:"goal"`
	}
	// list-all was recorded with three orgs in the project: one stopped,
	// one scheduled that crashed, one never run.
	for f, want := range map[string]map[string]string{
		"list-running.json": {"sec": "running"},
		"list-all.json":     {"sec": "stopped", "sched": "crashed", "bad": "never run"},
	} {
		goldenMonomind(t, f, "status-stopped.json", "events.ndjson")
		out, err := OrgList(context.Background(), t.TempDir())
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		var got struct {
			V     int    `json:"v"`
			Items []item `json:"items"`
		}
		if err := json.Unmarshal(out, &got); err != nil || got.V != 1 || len(got.Items) != len(want) {
			t.Fatalf("%s: %s (%v)", f, out, err)
		}
		for _, it := range got.Items {
			if want[it.Name] != it.Status || it.Roles != 3 || it.Goal == "" {
				t.Errorf("%s: item %+v, want status %q", f, it, want[it.Name])
			}
			if it.Name == "sched" && (it.Schedule == nil || *it.Schedule != "1m") {
				t.Errorf("%s: scheduled org lost its schedule: %+v", f, it)
			}
		}
	}
}

func TestGolden_OrgStatusShapes(t *testing.T) {
	type status struct {
		V        int      `json:"v"`
		Name     string   `json:"name"`
		Status   string   `json:"status"`
		Run      string   `json:"run"`
		PID      int      `json:"pid"`
		Paused   bool     `json:"paused"`
		Abandon  []string `json:"abandoned_roles"`
		ClosedBy string   `json:"closed_by"`
		IdleIn   *int     `json:"idle_stop_in_seconds"`
	}
	root := t.TempDir()
	// A live run: runtime.json says running with this test's own pid.
	rtDir := filepath.Join(root, ".monomind", "orgs", "sec")
	if err := os.MkdirAll(rtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	live, _ := json.Marshal(map[string]any{"status": "running", "pid": os.Getpid()})
	if err := os.WriteFile(filepath.Join(rtDir, "runtime.json"), live, 0o644); err != nil {
		t.Fatal(err)
	}
	goldenMonomind(t, "list-running.json", "status-running.json", "events.ndjson")
	out, err := OrgStatus(context.Background(), root, "sec")
	if err != nil {
		t.Fatal(err)
	}
	var st status
	if err := json.Unmarshal(out, &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "running" || st.Run == "" || st.PID == 0 || st.IdleIn == nil {
		t.Errorf("running status = %s", out)
	}

	// The recorded run's pid is long gone: a runtime.json that still says
	// running is reported stopped (#294), whatever else the payload carries.
	dead, _ := json.Marshal(map[string]any{"status": "running", "pid": st.PID})
	if err := os.WriteFile(filepath.Join(rtDir, "runtime.json"), dead, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = OrgStatus(context.Background(), root, "sec")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &st); err != nil || st.Status != "stopped" || st.Run == "" {
		t.Errorf("dead run = %s (%v)", out, err)
	}

	// A finished run: closed_by names why, and nothing section-shaped is
	// required of status (2.24.1 has no section fields in it).
	goldenMonomind(t, "list-all.json", "status-stopped.json", "events.ndjson")
	out, err = OrgStatus(context.Background(), t.TempDir(), "sec")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &st); err != nil || st.Status != "stopped" || st.ClosedBy != "org-complete" {
		t.Errorf("stopped status = %s (%v)", out, err)
	}

	// `org status` without a name: the list envelope.
	goldenMonomind(t, "list-running.json", "status-all-running.json", "events.ndjson")
	out, err = OrgStatus(context.Background(), t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	var all struct {
		Items []status `json:"items"`
	}
	if err := json.Unmarshal(out, &all); err != nil || len(all.Items) != 1 || all.Items[0].Name != "sec" {
		t.Errorf("status all = %s (%v)", out, err)
	}
}

func TestGolden_OrgValidateText(t *testing.T) {
	goldenMonomind(t, "list-all.json", "status-stopped.json", "events.ndjson")
	out, err := OrgValidate(context.Background(), t.TempDir(), "sec")
	if err != nil {
		t.Fatalf("a valid 2.24.1 org with warnings must not error: %v", err)
	}
	// The count is monomind's lint, which grows with each release (2.25.0
	// adds an older-model-pin warning to the recorded org): the summary line
	// is what the parser must find, not how many warnings precede it.
	if !regexp.MustCompile(`sec: valid \(\d+ warning\(s\)\)`).MatchString(out) {
		t.Errorf("valid output = %q", out)
	}
	_, err = OrgValidate(context.Background(), t.TempDir(), "bad")
	if err == nil {
		t.Fatal("an org with removed loops must fail validation")
	}
	// The message that tells the author what to do reaches the caller.
	for _, want := range []string{`"loops": removed`, "max_rework_rounds", "sections.review.mode: no longer needed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

// replayEvents runs OrgEvents over a recorded stream and returns the
// decoded lines. Decoding is generic on purpose: this is what a consumer
// that must survive unknown kinds and fields sees.
func replayEvents(t *testing.T, file string) []map[string]any {
	t.Helper()
	goldenMonomind(t, "list-all.json", "status-stopped.json", file)
	var evs []map[string]any
	err := OrgEvents(context.Background(), t.TempDir(), "sec", OrgEventsOptions{}, func(line []byte) {
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Errorf("line is not JSON: %s", line)
			return
		}
		evs = append(evs, m)
	})
	if err != nil {
		t.Fatalf("OrgEvents(%s): %v", file, err)
	}
	return evs
}

func TestGolden_OrgEventsRealStream(t *testing.T) {
	evs := replayEvents(t, "events.ndjson")
	if want := strings.Count(string(goldenBytes(t, "events.ndjson")), "\n"); len(evs) != want {
		t.Fatalf("delivered %d lines, recorded %d", len(evs), want)
	}
	kinds := map[string]int{}
	for _, e := range evs {
		for _, k := range []string{"id", "ts", "org", "run", "type"} {
			if _, ok := e[k]; !ok {
				t.Fatalf("event without %q: %v", k, e)
			}
		}
		kinds[e["type"].(string)]++
	}
	// The kinds a sections run emits, including the runtime's own
	// "org-docs" messages that announce documents to consumers.
	for _, k := range []string{"status", "audit", "chat", "tool", "message", "usage"} {
		if kinds[k] == 0 {
			t.Errorf("recorded stream has no %q event (kinds: %v)", k, kinds)
		}
	}
	var docMsgs int
	for _, e := range evs {
		if e["type"] == "message" && e["from"] == "org-docs" {
			docMsgs++
		}
	}
	if docMsgs != 2 {
		t.Errorf("org-docs messages = %d, want 2 (document ready, all documents available)", docMsgs)
	}
}

// Unknown event kinds and unknown fields are data, never an error: every
// line is delivered and the known ones around them are unaffected.
func TestGolden_OrgEventsUnknownKindsAreNotFatal(t *testing.T) {
	evs := replayEvents(t, "events-unknown-kind.ndjson")
	if want := strings.Count(string(goldenBytes(t, "events-unknown-kind.ndjson")), "\n"); len(evs) != want {
		t.Fatalf("delivered %d lines, fixture has %d", len(evs), want)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e["type"].(string)] = true
	}
	for _, k := range []string{"section_status", "hologram-from-the-future", "status", "message"} {
		if !seen[k] {
			t.Errorf("kind %q not delivered", k)
		}
	}
	var withExtra bool
	for _, e := range evs {
		if _, ok := e["brand_new_field"]; ok {
			withExtra = true
		}
	}
	if !withExtra {
		t.Error("a known kind carrying an unknown field was dropped")
	}
}

// The document store's own log (docs/<run>/events.jsonl) is a hash chain:
// replaying it is how a reader trusts it. The recorded one must chain.
func TestGolden_DocumentEventsChain(t *testing.T) {
	f, err := os.Open(goldenPath("doc-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	prev, seq := strings.Repeat("0", 64), 0
	var types []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e struct {
			Seq  int    `json:"seq"`
			Prev string `json:"prev"`
			At   string `json:"at"`
			Type string `json:"type"`
			Doc  string `json:"doc"`
			By   string `json:"by"`
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		seq++
		if e.Seq != seq || e.Prev != prev || e.At == "" || e.Doc != "note-1" || e.By == "" {
			t.Fatalf("event %d does not chain or lacks fields: %s", seq, sc.Bytes())
		}
		sum := sha256.Sum256(sc.Bytes())
		prev = hex.EncodeToString(sum[:])
		types = append(types, e.Type)
	}
	if strings.Join(types, ",") != "published,read,decided" {
		t.Errorf("document event types = %v", types)
	}
}

// The schedule audit file and its bus echo share one event name.
func TestGolden_ScheduleAudit(t *testing.T) {
	var file struct {
		TS    int64  `json:"ts"`
		Event string `json:"event"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(goldenBytes(t, "schedule-audit.jsonl"), &file); err != nil {
		t.Fatal(err)
	}
	var bus struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(goldenBytes(t, "schedule-audit-bus.ndjson"), &bus); err != nil {
		t.Fatal(err)
	}
	if file.Event != "scheduled-tick-deferred" || bus.Type != "audit" || bus.Reason != file.Event || bus.Msg != file.Msg || file.TS == 0 {
		t.Errorf("file %+v, bus %+v", file, bus)
	}
}

func TestKnownGoodAdvisory(t *testing.T) {
	for _, c := range []struct {
		version string
		below   bool
	}{
		{"2.24.1", false}, {"v2.24.1", false}, {"2.24.2", false}, {"2.25.0", false}, {"3.0.0", false},
		{"2.24.0", true}, {"2.23.9", true}, {"2.16.0", true}, {"2.10.0", true},
		{"2.24.1-beta.1", false}, // pre-release tags compare on their numeric core
		{"", false}, {"unknown", false},
	} {
		if got := BelowKnownGood(c.version); got != c.below {
			t.Errorf("BelowKnownGood(%q) = %v, want %v", c.version, got, c.below)
		}
		if got := KnownGoodAdvisory(c.version) != ""; got != c.below {
			t.Errorf("KnownGoodAdvisory(%q) set = %v, want %v", c.version, got, c.below)
		}
	}
	adv := KnownGoodAdvisory("2.20.0")
	for _, want := range []string{"2.20.0", KnownGoodMonomindVersion, "sections", "schedule audit", "npm install -g"} {
		if !strings.Contains(adv, want) {
			t.Errorf("advisory lacks %q: %s", want, adv)
		}
	}
}
