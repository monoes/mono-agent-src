package monomind

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeBin(t *testing.T, name string) string {
	t.Helper()
	bin := filepath.Join("testdata", name)
	abs, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return abs
	}
	if err := os.Chmod(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		got, min string
		want     bool
	}{
		{"2.10.0", "2.10.0", true},
		{"2.11.3", "2.10.0", true},
		{"2.9.25", "2.10.0", false},
		{"3.0.0", "2.10.0", true},
		{"2.10.0-beta.1", "2.10.0", true},
		{"v2.10.0", "2.10.0", true},
		{"10.0.0", "9.9.9", true},
		{"2.10", "2.10.0", true},
	}
	for _, c := range cases {
		if got := versionAtLeast(c.got, c.min); got != c.want {
			t.Errorf("versionAtLeast(%q, %q) = %v, want %v", c.got, c.min, got, c.want)
		}
	}
}

func TestHandshakeAgainstFake(t *testing.T) {
	vi, err := Handshake(context.Background(), fakeBin(t, "fake-monomind.sh"))
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if vi.Version != "2.10.0" {
		t.Errorf("version = %q", vi.Version)
	}
	for _, cap := range RequiredCapabilities {
		if !vi.HasCapability(cap) {
			t.Errorf("missing capability %q", cap)
		}
	}
}

func TestScanAgainstFake(t *testing.T) {
	old := os.Getenv(EnvOverride)
	os.Setenv(EnvOverride, fakeBin(t, "fake-monomind.sh"))
	defer os.Setenv(EnvOverride, old)

	res, err := Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(res.Agents))
	}
	if e := res.Find("claude"); e == nil || !e.Installed {
		t.Errorf("claude entry = %+v", e)
	}
}

// TestScanFailsFastOnIncompatibleMonomind guards against a regression where
// Scan spawned `agent scan --json` without handshaking first: against a
// too-old/incompatible monomind that answers unknown subcommands with exit-0
// human help text instead of JSON, the old code surfaced a confusing
// "unparseable output" JSON error instead of the actionable handshake
// message every other entry point (Exec, agent.ask, agentgen) already gives.
func TestScanFailsFastOnIncompatibleMonomind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monomind")
	script := "#!/bin/sh\necho 'Agent Management Commands'\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv(EnvOverride)
	os.Setenv(EnvOverride, path)
	defer os.Setenv(EnvOverride, old)

	_, err := Scan(context.Background())
	if err == nil {
		t.Fatal("Scan() = nil error, want a handshake error against an incompatible monomind")
	}
	if !strings.Contains(err.Error(), "handshake") {
		t.Errorf("Scan() error = %q, want it to mention the handshake failure, not a JSON-parse error", err.Error())
	}
}

// TestExecToolBridgeRoundTrip drives the full bidirectional path through a
// real subprocess: events in, tool_call bridged to Go, tool_result frame
// written back on stdin, turn completes.
func TestExecToolBridgeRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	var mu atomic.Value // []string of event types
	mu.Store([]string{})
	var toolArgs json.RawMessage

	res, err := Exec(context.Background(), ExecOptions{
		Bin:     fakeBin(t, "fake-monomind.sh"),
		Runtime: "claude",
		Prompt:  "build it",
		Tools: []ToolSpec{{
			Name:        "create_nodes",
			Description: "Create workflow nodes",
			Schema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"count": map[string]interface{}{"type": "number"}},
				"required":   []string{"count"},
			},
		}},
		OnToolCall: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			if name != "create_nodes" {
				t.Errorf("tool name = %q", name)
			}
			toolArgs = args
			return "created 2 nodes", nil
		},
	}, func(ev Event) {
		prev, _ := mu.Load().([]string)
		mu.Store(append(prev, ev.Type))
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("turn error: %+v", res.Err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d", res.ExitCode)
	}
	if res.SessionID != "th_fake" {
		t.Errorf("session id = %q", res.SessionID)
	}
	var gotCount float64
	var direct map[string]interface{}
	if err := json.Unmarshal(toolArgs, &direct); err != nil {
		t.Fatalf("tool args unparseable: %v (raw %s)", err, toolArgs)
	}
	if c, ok := direct["count"].(float64); ok {
		gotCount = c
	}
	if gotCount != 2 {
		t.Errorf("tool args count = %v, want 2 (raw: %s)", gotCount, toolArgs)
	}
	types, _ := mu.Load().([]string)
	wantOrder := []string{EventStart, EventSession, EventAssistant, EventToolCall, EventToolResult, EventAssistant, EventUsage, EventResult, EventDone}
	if len(types) != len(wantOrder) {
		t.Fatalf("event order %v, want %v", types, wantOrder)
	}
	for i := range wantOrder {
		if types[i] != wantOrder[i] {
			t.Errorf("event[%d] = %s, want %s (%v)", i, types[i], wantOrder[i], types)
		}
	}
}

// TestExecCancelGroupKill verifies the plan's Phase-1 gate: cancelling a
// hung monomind leaves ZERO orphan processes — monomind AND its spawned
// grandchild are reaped by the group kill.
func TestExecCancelGroupKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("group kill is unix-only in this build")
	}
	oldGrace := KillGrace
	KillGrace = 500 * time.Millisecond
	defer func() { KillGrace = oldGrace }()

	bin := fakeBin(t, "fake-monolith.sh")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resCh := make(chan *TurnResult, 1)
	go func() {
		res, _ := Exec(ctx, ExecOptions{
			Bin:     bin,
			Runtime: "claude",
			Prompt:  "hang forever",
		}, nil)
		resCh <- res
	}()

	// Wait for the monolith (and its sleep child) to exist.
	var pids []int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(pids) < 2 {
		pids = pids[:0]
		out, _ := exec.Command("pgrep", "-f", "fake-monolith").Output()
		for _, s := range fields(string(out)) {
			pids = append(pids, atoi(s))
		}
		if len(pids) > 0 {
			sleepOut, _ := exec.Command("sh", "-c", "pgrep -P "+itoa(pids[0])).Output()
			for _, s := range fields(string(sleepOut)) {
				pids = append(pids, atoi(s))
			}
		}
		if len(pids) < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if len(pids) < 2 {
		t.Fatalf("expected monolith + child, found pids %v", pids)
	}

	cancel()
	select {
	case res := <-resCh:
		if res == nil || res.Err == nil || res.Err.Code != ErrCancelled {
			t.Fatalf("turn result = %+v, want cancelled", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Exec did not return after cancel")
	}

	// The gate: zero orphaned processes from the group.
	assertGroupReaped(t, pids)
}

// TestExecErrorTurnMapsProtocolError checks fatal error turns surface as
// TurnResult.Err with the protocol error code.
// TestExecCapturesTextFromAssistantEventWhenResultHasNone guards a real
// bug found via an actual end-to-end run against the currently-installed
// monomind binary, not caught by any existing fixture: its own "result"
// event carries no "text" field at all for a plain conversational turn
// (no tool calls) -- only the "assistant" event does. Exec must not
// return an empty ResultText just because EventResult itself is silent.
func TestExecCapturesTextFromAssistantEventWhenResultHasNone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	t.Setenv("FAKE_MODE", "no_result_text")

	res, err := Exec(context.Background(), ExecOptions{
		Bin:     fakeBin(t, "fake-monomind.sh"),
		Runtime: "claude",
		Prompt:  "reply with json",
	}, func(ev Event) {})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("turn error: %+v", res.Err)
	}
	if res.ResultText != `{"ok":true}` {
		t.Fatalf("ResultText = %q, want the assistant event's text (the result event carries none in this scenario)", res.ResultText)
	}
}

func TestExecJoinsIncrementalAssistantDeltas(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	t.Setenv("FAKE_MODE", "streamed_deltas")

	res, err := Exec(context.Background(), ExecOptions{
		Bin:     fakeBin(t, "fake-monomind.sh"),
		Runtime: "claude",
		Prompt:  "reply with json",
	}, func(ev Event) {})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("turn error: %+v", res.Err)
	}
	if res.ResultText != `{"ok": true}` {
		t.Fatalf("ResultText = %q, want the joined deltas", res.ResultText)
	}
}

func TestApplyEventToResultAssistantText(t *testing.T) {
	start := func(incremental bool) Event { return Event{Type: EventStart, StreamsIncrementally: incremental} }
	say := func(text string) Event { return Event{Type: EventAssistant, Text: text} }
	cases := []struct {
		name   string
		events []Event
		want   string
	}{
		{"whole messages keep the latest", []Event{start(false), say("Checking."), say("The answer is 42.")}, "The answer is 42."},
		{"no start event keeps the latest", []Event{say("one"), say("two")}, "two"},
		{"deltas are joined", []Event{start(true), say("The ans"), say("wer is "), say("42.")}, "The answer is 42."},
		{"deltas restart after a tool call", []Event{start(true), say("Let me "), say("check."), {Type: EventToolCall}, {Type: EventToolResult}, say("It is "), say("42.")}, "It is 42."},
		{"text before an unanswered tool call is kept", []Event{start(true), say("Let me check."), {Type: EventToolCall}}, "Let me check."},
		{"result text wins", []Event{start(true), say("fragment"), {Type: EventResult, Text: "full reply"}, say(" late")}, "full reply"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &TurnResult{}
			for _, ev := range tc.events {
				ApplyEventToResult(res, ev)
			}
			if res.ResultText != tc.want {
				t.Fatalf("ResultText = %q, want %q", res.ResultText, tc.want)
			}
		})
	}
}

func TestExecErrorTurnMapsProtocolError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	// Reuse fake-monomind's exec path but force an auth failure through a
	// tiny inline script.
	dir := t.TempDir()
	bin := filepath.Join(dir, "monomind")
	script := "#!/bin/sh\ncase \"$1 $2\" in \"--version --json\") echo '{\"v\":1,\"version\":\"2.10.0\",\"min_caller\":\"1.0.0\",\"capabilities\":[\"agent-exec\",\"agent-scan\"]}'; exit 0;; esac\n" +
		"echo '{\"v\":1,\"type\":\"start\",\"runtime\":\"codex\",\"cwd\":\"/app\",\"pid\":1}'\n" +
		"echo '{\"v\":1,\"type\":\"error\",\"code\":\"auth\",\"fatal\":true,\"message\":\"not logged in\"}'\n" +
		"echo '{\"v\":1,\"type\":\"done\",\"exit_code\":1}'\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "codex", Prompt: "hi"}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Err == nil {
		t.Fatal("expected protocol error")
	}
	if res.Err.Code != ErrAuth || !res.Err.Fatal {
		t.Errorf("err = %+v", res.Err)
	}
	if res.ExitCode != 1 {
		t.Errorf("exit = %d", res.ExitCode)
	}
}

// fakeBinCatFixture writes a throwaway fake monomind binary that ignores
// its arguments and streams one golden testdata/fixtures/<name>.ndjson
// transcript to stdout, for driving Exec() end-to-end against a fixture
// (rather than just parsing it, as fixtures_test.go's fixtureEvents does).
func fakeBinCatFixture(t *testing.T, fixtureName string) string {
	t.Helper()
	fixturePath, err := filepath.Abs(filepath.Join("testdata", "fixtures", fixtureName+".ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "monomind")
	script := "#!/bin/sh\ncat \"" + fixturePath + "\"\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestExecNonFatalErrorDoesNotFailTheTurn is the regression test for the
// bug where Exec unconditionally set res.Err for ANY error event, even a
// documented non-fatal (fatal:false) one that the stream fully recovers
// from. The golden fixture testdata/fixtures/bad-frame.ndjson contains
// exactly that: a mid-stream non-fatal error followed by tool_result,
// session, assistant text, a success result, and done exit_code:0 —
// TestFixtureExitCodesMatchContract already asserts exit code 0 is correct
// for this fixture. Both real callers (chat.go, agent.go) treat any
// non-nil res.Err as a hard failure, so a non-nil res.Err here would
// discard a successful turn's ResultText.
func TestExecNonFatalErrorDoesNotFailTheTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	// The fixture also contains a tool_call (bridged normally here via
	// Tools/OnToolCall) ahead of the non-fatal error — configuring a
	// handler keeps this test isolated to the non-fatal-error behavior,
	// as opposed to TestExecNilOnToolCallReportsProtocolErrorNotPanic below
	// which deliberately leaves OnToolCall unset against the same fixture.
	res, err := Exec(context.Background(), ExecOptions{
		Bin:     fakeBinCatFixture(t, "bad-frame"),
		Runtime: "codex",
		Prompt:  "hi",
		Tools: []ToolSpec{{
			Name:        "create_nodes",
			Description: "Create workflow nodes",
			Schema:      map[string]interface{}{"type": "object"},
		}},
		OnToolCall: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			return "recovered", nil
		},
	}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("res.Err = %+v, want nil: a non-fatal error that the stream recovers from must not fail the turn", res.Err)
	}
	if res.ExitCode != 0 {
		t.Errorf("res.ExitCode = %d, want 0", res.ExitCode)
	}
	if res.ResultText != "result: recovered" {
		t.Errorf("res.ResultText = %q, want %q (the fixture's assistant text)", res.ResultText, "result: recovered")
	}
	if res.SessionID != "th_b" {
		t.Errorf("res.SessionID = %q, want %q", res.SessionID, "th_b")
	}
}

// TestExecNilOnToolCallReportsProtocolErrorNotPanic is the regression test
// for the bug where a tool_call event arriving with opts.OnToolCall nil
// (the common no-tools case, since OnToolCall is only required when Tools
// is set) caused Exec to call a nil function in a background goroutine,
// panicking with an unrecovered SIGSEGV that crashes the entire hosting
// process, not just the current turn. testdata/fixtures/bad-frame.ndjson
// already contains a tool_call event, so it's reused here without Tools/
// OnToolCall configured — a protocol mismatch (misbehaving/mismatched
// binary). Exec must degrade gracefully: report a protocol error, not
// crash the test binary. (A pre-fix run of this test would not merely
// fail — it would take down the whole `go test` process; the -race build
// getting a clean run here corroborates no goroutine ever panicked.)
func TestExecNilOnToolCallReportsProtocolErrorNotPanic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	res, err := Exec(context.Background(), ExecOptions{
		Bin:     fakeBinCatFixture(t, "bad-frame"),
		Runtime: "codex",
		Prompt:  "hi",
		// Tools/OnToolCall deliberately left unset.
	}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res == nil {
		t.Fatal("Exec returned a nil *TurnResult")
	}
	if res.Err == nil {
		t.Fatal("res.Err = nil, want a protocol error reported for the unhandled tool_call")
	}
	if !res.Err.Fatal {
		t.Errorf("res.Err.Fatal = false, want true for an unhandleable protocol mismatch: %+v", res.Err)
	}
}

// TestExecFlagMappingSandboxing verifies the argv Exec builds for
// `agent exec`: the prompt travels via --prompt-file (never --prompt argv),
// and an empty Tools list passes --tools none explicitly instead of
// letting monomind apply its own default toolset.
func TestExecFlagMappingSandboxing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "monomind")
	record := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ] && [ \"$2\" = \"--json\" ]; then echo '{\"v\":1,\"version\":\"2.10.0\",\"min_caller\":\"1.0.0\",\"capabilities\":[\"agent-exec\",\"agent-scan\",\"org-json-v1\"]}'; exit 0; fi\n" +
		"if [ \"$1\" = \"agent\" ] && [ \"$2\" = \"exec\" ]; then\n" +
		"  printf '%s\\n' \"$@\" > \"" + record + "\"\n" +
		"  pf=\"\"; prev=\"\"\n" +
		"  for a in \"$@\"; do if [ \"$prev\" = \"--prompt-file\" ]; then pf=\"$a\"; fi; prev=\"$a\"; done\n" +
		"  if [ -n \"$pf\" ] && grep -q 'GENERATE_MARKER_HTML' \"$pf\"; then echo 'PROMPT_FILE_HAS_PROMPT yes' >> \"" + record + "\"; else echo 'PROMPT_FILE_HAS_PROMPT no' >> \"" + record + "\"; fi\n" +
		"  echo '{\"v\":1,\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"stop_reason\":\"end_turn\",\"text\":\"ok\"}'\n" +
		"  echo '{\"v\":1,\"type\":\"done\",\"exit_code\":0}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'unsupported' >&2; exit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Exec(context.Background(), ExecOptions{
		Bin:     bin,
		Runtime: "claude",
		Prompt:  "GENERATE_MARKER_HTML <html></html>",
	}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("turn result = %+v, want clean success", res)
	}

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
	has := func(want string) bool {
		for _, a := range argv {
			if a == want {
				return true
			}
		}
		return false
	}

	if has("--prompt") {
		t.Errorf("argv passed --prompt (prompt must go via --prompt-file): %v", argv)
	}
	if !has("--prompt-file") {
		t.Errorf("argv missing --prompt-file: %v", argv)
	}
	if !has("--tools") || !has("none") {
		t.Errorf("argv missing explicit '--tools none': %v", argv)
	}

	// The prompt file (probed by the fake binary; the temp file is removed
	// once Exec returns) must have carried the prompt text.
	if !strings.Contains(string(raw), "PROMPT_FILE_HAS_PROMPT yes") {
		t.Errorf("prompt file did not contain the prompt:\n%s", raw)
	}
}

// TestExecWritesItsFilesInTempDir: a caller whose turn runs in a sandbox that can
// write the system temp directory gives Exec a private one, so another turn
// cannot rewrite the prompt files between their creation and monomind reading
// them.
func TestExecWritesItsFilesInTempDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "monomind")
	record := filepath.Join(dir, "files.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ] && [ \"$2\" = \"--json\" ]; then echo '{\"v\":1,\"version\":\"2.10.0\",\"min_caller\":\"1.0.0\",\"capabilities\":[\"agent-exec\",\"agent-scan\",\"org-json-v1\"]}'; exit 0; fi\n" +
		"if [ \"$1\" = \"agent\" ] && [ \"$2\" = \"exec\" ]; then\n" +
		"  prev=\"\"\n" +
		"  for a in \"$@\"; do\n" +
		"    if [ \"$prev\" = \"--prompt-file\" ] || [ \"$prev\" = \"--system-file\" ]; then echo \"$a\" >> \"" + record + "\"; fi\n" +
		"    prev=\"$a\"\n" +
		"  done\n" +
		"  echo '{\"v\":1,\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"stop_reason\":\"end_turn\",\"text\":\"ok\"}'\n" +
		"  echo '{\"v\":1,\"type\":\"done\",\"exit_code\":0}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'unsupported' >&2; exit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(dir, "private")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "claude", Prompt: "p", SystemPrompt: "s", TempDir: private}, nil); err != nil {
		t.Fatalf("exec: %v", err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	files := strings.Fields(string(raw))
	if len(files) != 2 {
		t.Fatalf("want the prompt file and the system file, got %q", files)
	}
	for _, f := range files {
		if filepath.Dir(f) != private {
			t.Errorf("%s is not in TempDir %s", f, private)
		}
	}
	if left, _ := os.ReadDir(private); len(left) != 0 {
		t.Errorf("Exec must remove its files when the turn ends: %d left", len(left))
	}
}

// TestFilteredEnvironStripsMonomindOverrides guards the env-injection fix:
// ambient MONOMIND_* values (and Claude session markers) must never reach
// a spawned monomind child, where a duplicate entry could shadow the
// explicit per-profile MONOMIND_CWD set by the caller.
func TestFilteredEnvironStripsMonomindOverrides(t *testing.T) {
	t.Setenv("MONOMIND_CWD", "/attacker/controlled")
	t.Setenv("MONOMIND_HOME", "/attacker/controlled")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION", "abc")
	t.Setenv("MONOTOOL_BENIGN_VAR", "keep")

	env := FilteredEnviron()
	benignSeen := false
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "MONOMIND_") {
			t.Errorf("MONOMIND_* override %q leaked through FilteredEnviron", key)
		}
		if strings.HasPrefix(key, "CLAUDE_CODE_") || key == "CLAUDECODE" {
			t.Errorf("Claude session marker %q leaked through FilteredEnviron", key)
		}
		if key == "MONOTOOL_BENIGN_VAR" {
			benignSeen = true
		}
	}
	if !benignSeen {
		t.Error("benign environment variable disappeared from FilteredEnviron")
	}
}

// A mission token (and its labels) in the ambient environment is not passed on: only opts.Env,
// which the conductor fills for the one worker the token was minted for, carries it.
func TestFilteredEnvironDropsAmbientMissionTokens(t *testing.T) {
	for _, k := range []string{"MONOAGENT_TASK_TOKEN", "MONOAGENT_TASK_SESSION", "MONOAGENT_TASK_ID", "MONOAGENT_TASK_ROLE"} {
		t.Setenv(k, "inherited")
	}
	t.Setenv("MONOAGENT_OTHER", "keep")
	var other bool
	for _, kv := range FilteredEnviron() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "MONOAGENT_TASK_") {
			t.Errorf("%s was passed on to a child", key)
		}
		other = other || key == "MONOAGENT_OTHER"
	}
	if !other {
		t.Error("an unrelated MONOAGENT_ variable was dropped")
	}
	// the explicit value for the worker the token was minted for still reaches it
	got := append(FilteredEnviron(), envSlice(map[string]string{"MONOAGENT_TASK_TOKEN": "mine"})...)
	if last := got[len(got)-1]; last != "MONOAGENT_TASK_TOKEN=mine" {
		t.Errorf("the worker's own token is not last: %q", last)
	}
	for _, kv := range got[:len(got)-1] {
		if strings.HasPrefix(kv, "MONOAGENT_TASK_") {
			t.Errorf("an inherited label precedes the worker's own: %q", kv)
		}
	}
}

func fields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// writeInlineFakeBin writes a throwaway fake monomind binary running the
// given shell script body (only the `agent exec` case needs handling; the
// handshake case some tests also need is added by the caller when needed).
func writeInlineFakeBin(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "monomind")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestExecResultOnlyText is the plan's "result-only text" case: a runtime
// that never puts prose on the assistant event, only on result.text (the
// mirror image of the currently-real no_result_text scenario already
// covered by fake-monomind.sh, where assistant carries text and result
// doesn't). ResultText must still come through.
func TestExecResultOnlyText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	bin := writeInlineFakeBin(t, `echo '{"v":1,"type":"start","runtime":"codex","cwd":"/app","pid":1}'
echo '{"v":1,"type":"session","session_id":"th_result_only"}'
echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"the answer"}'
echo '{"v":1,"type":"done","exit_code":0}'
exit 0
`)
	res, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "codex", Prompt: "hi"}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ResultText != "the answer" {
		t.Errorf("ResultText = %q, want %q", res.ResultText, "the answer")
	}
	if res.Err != nil {
		t.Errorf("Err = %+v, want nil", res.Err)
	}
}

// TestExecUsageMetrics_ExplicitZeroVsOmitted is the plan's "explicit zero
// versus omitted cost" case: a result reporting cost_usd:0 must come back
// as HasCostUSD=true/CostUSD=0 (genuinely free), while a result omitting
// cost_usd entirely must come back as HasCostUSD=false (unavailable) — the
// two must be distinguishable, not both collapse to a zero value.
func TestExecUsageMetrics_ExplicitZeroVsOmitted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	t.Run("explicit zero", func(t *testing.T) {
		bin := writeInlineFakeBin(t, `echo '{"v":1,"type":"start","runtime":"codex","cwd":"/app","pid":1}'
echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok","input_tokens":10,"output_tokens":5,"cost_usd":0}'
echo '{"v":1,"type":"done","exit_code":0}'
exit 0
`)
		res, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "codex", Prompt: "hi"}, nil)
		if err != nil {
			t.Fatalf("exec: %v", err)
		}
		if !res.HasCostUSD || res.CostUSD != 0 {
			t.Errorf("HasCostUSD=%v CostUSD=%v, want true/0 (explicitly reported as free)", res.HasCostUSD, res.CostUSD)
		}
		if !res.HasInputTokens || res.InputTokens != 10 {
			t.Errorf("HasInputTokens=%v InputTokens=%v, want true/10", res.HasInputTokens, res.InputTokens)
		}
	})
	t.Run("omitted", func(t *testing.T) {
		bin := writeInlineFakeBin(t, `echo '{"v":1,"type":"start","runtime":"codex","cwd":"/app","pid":1}'
echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok"}'
echo '{"v":1,"type":"done","exit_code":0}'
exit 0
`)
		res, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "codex", Prompt: "hi"}, nil)
		if err != nil {
			t.Fatalf("exec: %v", err)
		}
		if res.HasCostUSD {
			t.Errorf("HasCostUSD = true (CostUSD=%v), want false — cost was never reported", res.CostUSD)
		}
		if res.HasInputTokens {
			t.Errorf("HasInputTokens = true, want false — tokens were never reported")
		}
	})
	// Protocol rev 28 (agent-exec-cost-null): an unknown cost is null, never
	// 0 — it must read as unavailable, and must not wipe a cost an earlier
	// event of the turn reported.
	t.Run("null", func(t *testing.T) {
		bin := writeInlineFakeBin(t, `echo '{"v":1,"type":"start","runtime":"codex","cwd":"/app","pid":1}'
echo '{"v":1,"type":"usage","input_tokens":10,"output_tokens":5,"cost_usd":null}'
echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok","input_tokens":10,"output_tokens":5,"cost_usd":null}'
echo '{"v":1,"type":"done","exit_code":0}'
exit 0
`)
		res, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "codex", Prompt: "hi"}, nil)
		if err != nil {
			t.Fatalf("exec: %v", err)
		}
		if res.HasCostUSD || res.CostUSD != 0 {
			t.Errorf("HasCostUSD=%v CostUSD=%v, want false/0 — a null cost is unknown", res.HasCostUSD, res.CostUSD)
		}
		if !res.HasInputTokens || res.InputTokens != 10 {
			t.Errorf("HasInputTokens=%v InputTokens=%v, want true/10", res.HasInputTokens, res.InputTokens)
		}

		bin = writeInlineFakeBin(t, `echo '{"v":1,"type":"start","runtime":"codex","cwd":"/app","pid":1}'
echo '{"v":1,"type":"usage","input_tokens":10,"output_tokens":5,"cost_usd":0.004}'
echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok","cost_usd":null}'
echo '{"v":1,"type":"done","exit_code":0}'
exit 0
`)
		res, err = Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "codex", Prompt: "hi"}, nil)
		if err != nil {
			t.Fatalf("exec: %v", err)
		}
		if !res.HasCostUSD || res.CostUSD != 0.004 {
			t.Errorf("HasCostUSD=%v CostUSD=%v, want the earlier 0.004 kept", res.HasCostUSD, res.CostUSD)
		}
	})
}

// TestExecNonzeroDoneWithZeroProcessExit is the plan's "nonzero done with a
// zero process exit" case: the protocol's own done.exit_code can disagree
// with the OS process exit status. A caller that only checked cmd.Wait()'s
// error (as the pre-fix code effectively did) would call this success.
func TestExecNonzeroDoneWithZeroProcessExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	bin := writeInlineFakeBin(t, `echo '{"v":1,"type":"start","runtime":"codex","cwd":"/app","pid":1}'
echo '{"v":1,"type":"done","exit_code":5}'
exit 0
`)
	res, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "codex", Prompt: "hi"}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 5 {
		t.Errorf("ExitCode = %d, want 5 (protocol done.exit_code, not the OS process exit)", res.ExitCode)
	}
	if res.Err == nil {
		t.Fatal("Err = nil, want a protocol error for the nonzero done.exit_code despite a clean process exit")
	}
	if res.Err.ExitCode != 5 {
		t.Errorf("Err.ExitCode = %d, want 5", res.Err.ExitCode)
	}
}

// TestExecResultIsErrorMapsToFailure covers precedence rule 2 (§227-232):
// a result explicitly marked is_error is a failure even with a clean done
// exit_code:0 and a clean process exit — there is no fatal `error` event at
// all in this transcript, only the result's own is_error flag.
func TestExecResultIsErrorMapsToFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	bin := writeInlineFakeBin(t, `echo '{"v":1,"type":"start","runtime":"codex","cwd":"/app","pid":1}'
echo '{"v":1,"type":"result","subtype":"error_during_execution","is_error":true,"stop_reason":"end_turn","text":"tool exploded"}'
echo '{"v":1,"type":"done","exit_code":0}'
exit 0
`)
	res, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "codex", Prompt: "hi"}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Err == nil {
		t.Fatal("Err = nil, want a protocol error for a result marked is_error")
	}
}

// TestExecMissingDoneEventMeansNotSawDone covers precedence rule 3
// (§227-232): a clean process exit that never sent a terminal `done` event
// at all must be distinguishable from a genuinely completed turn. Exec's
// job is only to report this honestly via SawDone — mapping it to a
// user-visible "interrupted" status is the supervisor's job (see
// internal/ai/chatevents/normalize.go), not Exec's.
func TestExecMissingDoneEventMeansNotSawDone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	bin := writeInlineFakeBin(t, `echo '{"v":1,"type":"start","runtime":"codex","cwd":"/app","pid":1}'
echo '{"v":1,"type":"assistant","text":"partial answer before the pipe just closed"}'
exit 0
`)
	res, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "codex", Prompt: "hi"}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.SawDone {
		t.Error("SawDone = true, want false — no done event was ever sent")
	}
	if res.Err != nil {
		t.Errorf("Err = %+v, want nil — a missing done is not itself a protocol error, just missing evidence", res.Err)
	}
}

// TestFilteredEnvironKeepsOperatorSettings: MONOMIND_CLAUDE_PATH and the
// coder agent caps are operator choices, not per-call scoping.
func TestFilteredEnvironKeepsOperatorSettings(t *testing.T) {
	t.Setenv("MONOMIND_CLAUDE_PATH", "/usr/bin/claude")
	t.Setenv("MONOMIND_CODER_MAX_AGENTS", "80")
	t.Setenv("MONOMIND_CWD", "/elsewhere")
	got := map[string]bool{}
	for _, kv := range FilteredEnviron() {
		k, _, _ := strings.Cut(kv, "=")
		got[k] = true
	}
	if !got["MONOMIND_CLAUDE_PATH"] || !got["MONOMIND_CODER_MAX_AGENTS"] {
		t.Errorf("operator settings dropped: %v", got)
	}
	if got["MONOMIND_CWD"] {
		t.Error("MONOMIND_CWD must still be stripped")
	}
}
