package chat

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	aichat "github.com/monoes/mono-agent/internal/ai/chat"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/testdb"
)

// TestParseToolsFlag covers the mechanical run-gate plumbing: --tools is a
// comma-separated member set where "runs" is only valid alongside
// "monoagent", and unknown members fail loudly.
func TestParseToolsFlag(t *testing.T) {
	cases := []struct {
		in              string
		monoagent, runs bool
		wantErr         bool
	}{
		{"", false, false, false},
		{"monoagent", true, false, false},
		{"monoagent,runs", true, true, false},
		{"runs,monoagent", true, true, false},
		{" monoagent , runs ", true, true, false},
		{"monoagent,", true, false, false},
		{"runs", false, false, true},
		{"bogus", false, false, true},
		{"monoagent,bogus", false, false, true},
	}
	for _, c := range cases {
		mono, runs, err := parseToolsFlag(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseToolsFlag(%q) = nil error, want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseToolsFlag(%q) unexpected error: %v", c.in, err)
			continue
		}
		if mono != c.monoagent || runs != c.runs {
			t.Errorf("parseToolsFlag(%q) = (%v, %v), want (%v, %v)", c.in, mono, runs, c.monoagent, c.runs)
		}
	}
}

// TestAppendNewToolSpecsDropsDuplicateNames is a regression test: when both
// CanvasTools and MonoagentTools are wired into a chat turn (the common case
// — see effectiveCanvasID's auto-wiring in the chat RunE, which activates
// canvas tools any time --tools monoagent is set, even without an explicit
// --canvas), both define a "create_workflow" tool. Sending the runtime two
// ToolSpecs with the same name isn't merely redundant, it's fatal — the
// runtime rejects the second registration outright ("Tool create_workflow
// is already registered"), which previously broke every single turn (even
// a plain "hi") once monoagent tools were enabled.
func TestAppendNewToolSpecsDropsDuplicateNames(t *testing.T) {
	existing := []monomind.ToolSpec{
		{Name: "create_workflow", Description: "canvas version"},
		{Name: "create_nodes", Description: "canvas only"},
	}
	adding := []monomind.ToolSpec{
		{Name: "create_workflow", Description: "monoagent version — must be dropped"},
		{Name: "list_workflows", Description: "monoagent only"},
	}
	got := appendNewToolSpecs(existing, adding)

	byName := make(map[string]monomind.ToolSpec, len(got))
	for _, ts := range got {
		if _, dup := byName[ts.Name]; dup {
			t.Fatalf("appendNewToolSpecs produced a duplicate %q entry: %+v", ts.Name, got)
		}
		byName[ts.Name] = ts
	}

	if len(got) != 3 {
		t.Fatalf("appendNewToolSpecs returned %d specs, want 3 (create_workflow, create_nodes, list_workflows): %+v", len(got), got)
	}
	if byName["create_workflow"].Description != "canvas version" {
		t.Fatalf("create_workflow should keep the first (canvas) definition, got: %+v", byName["create_workflow"])
	}
	if _, ok := byName["list_workflows"]; !ok {
		t.Fatal("expected list_workflows (a genuinely new name from adding) to be present")
	}
}

// TestChatWorkingDir covers the gate on real --cwd scoping: only turns with
// MonoagentTools enabled pay for it (see the no-cwd-override comment in
// RunE) — canvas-only turns must keep getting "" so they keep their
// zero-cwd performance profile.
func TestChatWorkingDir(t *testing.T) {
	if got := chatWorkingDir(true, "/profiles/work"); got != "/profiles/work" {
		t.Errorf("chatWorkingDir(true, ...) = %q, want the project root", got)
	}
	if got := chatWorkingDir(false, "/profiles/work"); got != "" {
		t.Errorf("chatWorkingDir(false, ...) = %q, want \"\" (canvas-only turns stay uncoped)", got)
	}
}

// TestWorkspaceConventionPromptListsTaxonomyAndForbidsDotFolders is a
// regression test for Change 4: the prompt text must be generated from
// profiledir.TaxonomyFolders (not hand-copied), so it can't silently drift
// from the actual taxonomy, and it must state the dot-folder prohibition.
func TestWorkspaceConventionPromptListsTaxonomyAndForbidsDotFolders(t *testing.T) {
	got := workspaceConventionPrompt("/profiles/work")
	for _, f := range profiledir.TaxonomyFolders {
		if !strings.Contains(got, f.Name) {
			t.Errorf("workspaceConventionPrompt() missing taxonomy folder %q:\n%s", f.Name, got)
		}
	}
	if !strings.Contains(got, "/profiles/work") {
		t.Errorf("workspaceConventionPrompt() should mention the actual root %q:\n%s", "/profiles/work", got)
	}
	lower := strings.ToLower(got)
	if !strings.Contains(lower, "dot") {
		t.Errorf("workspaceConventionPrompt() should state the dot-folder prohibition:\n%s", got)
	}
}

// TestMonoagentToolSpecsAdvertisesSaveDocument is a regression test for the
// one failure mode unit tests that call MonoagentTools.Execute directly
// can't catch: the tool never reaching the runtime at all. Every
// TestSaveDocument_* test in internal/ai/chat calls
// Execute("save_document", ...) directly, bypassing ToolDefs() ->
// monoagentToolSpecs -> the tools-file -> stdio -> OnToolCall path the real
// chat runtime actually uses. If save_document were ever left out of
// ToolDefs(), or its required params dropped, every one of those tests
// would keep passing while the model never saw the tool existed.
func TestMonoagentToolSpecsAdvertisesSaveDocument(t *testing.T) {
	mt := aichat.NewMonoagentTools(nil, "", "default")
	specs := monoagentToolSpecs(mt)

	var found *monomind.ToolSpec
	for i := range specs {
		if specs[i].Name == "save_document" {
			found = &specs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("monoagentToolSpecs() does not advertise save_document")
	}

	required, _ := found.Schema["required"].([]string)
	for _, want := range []string{"filename", "content"} {
		ok := false
		for _, r := range required {
			if r == want {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("save_document schema required = %v, missing %q", required, want)
		}
	}
}

// --- end-to-end `chat` command tests: --no-history and the os.Exit fix ---

// newChatCLITestDB creates a fresh migrated DB (ApplyMigrations' own
// reconcile step seeds a "default"/"Default" profile — chat's RunE
// resolves --profile via resolveProfileID, which errors on an empty
// profiles table, so this must run before chat.go touches it) and points
// $HOME at a throwaway directory so profiledir/vault resolution never
// touches the developer's real ~/.monoagent.
func newChatCLITestDB(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	// A copy of the one migrated template (internal/testdb), not 130+
	// migrations run again for every test.
	return testdb.Path(t)
}

// writeFakeMonomindForChat writes a throwaway fake monomind binary that
// answers the handshake, then for `agent exec` streams the given transcript
// body (each line already a complete NDJSON event) verbatim to stdout.
func writeFakeMonomindForChat(t *testing.T, transcript string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "monomind")
	script := "#!/bin/sh\n" +
		`if [ "$1" = "--version" ] && [ "$2" = "--json" ]; then` + "\n" +
		`  echo '{"v":1,"version":"2.10.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'` + "\n" +
		"  exit 0\nfi\n" +
		`if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then` + "\n" +
		transcript + "\n" +
		"  exit 0\nfi\n" +
		`echo "unsupported: $*" >&2` + "\nexit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// runChatCmd executes the chat command and returns everything the turn
// wrote to stdout. chat.go's emit() writes NDJSON events via a raw
// fmt.Println (mirroring monoagentcli chat's real streaming contract, not
// cobra's own output abstraction), so cmd.SetOut alone would not capture it
// — the real os.Stdout must be swapped for the duration of Execute().
func runChatCmd(t *testing.T, dbPath, monomindBin string, args ...string) (stdout string, err error) {
	t.Helper()
	t.Setenv(monomind.EnvOverride, monomindBin)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	cmd := newChatCmd(cfg)
	cmd.SetArgs(args)

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	realStdout := os.Stdout
	os.Stdout = w
	execErr := cmd.Execute()
	os.Stdout = realStdout
	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String(), execErr
}

func countChatMessages(t *testing.T, dbPath, workflowID string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db for assertion: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_chat_messages WHERE workflow_id = ?`, workflowID).Scan(&n); err != nil {
		t.Fatalf("count chat messages: %v", err)
	}
	return n
}

const successTranscript = `  echo '{"v":1,"type":"start","runtime":"fake","cwd":"/app","pid":1}'
  echo '{"v":1,"type":"session","session_id":"th_cli_test"}'
  echo '{"v":1,"type":"assistant","text":"hello from the fake runtime"}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"hello from the fake runtime"}'
  echo '{"v":1,"type":"done","exit_code":0}'`

// TestChatHistorySavedByDefault is the control case for the two suppression
// tests below: without --no-history, an ordinary CLI turn still saves both
// the user and assistant messages, exactly as before this change.
func TestChatHistorySavedByDefault(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin := writeFakeMonomindForChat(t, successTranscript)
	_, err := runChatCmd(t, dbPath, bin, "--runtime", "fake", "--history-id", "wf-default", "hi")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got := countChatMessages(t, dbPath, "wf-default"); got != 2 {
		t.Errorf("saved messages = %d, want 2 (user + assistant)", got)
	}
}

// TestChatNoHistorySuppressesSave is the plan's "explicit history...
// suppression" case: --no-history must leave zero rows in the legacy
// ai_chat_messages table, while the turn itself still runs and streams
// output normally (verified via a nil error and non-empty stdout).
func TestChatNoHistorySuppressesSave(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin := writeFakeMonomindForChat(t, successTranscript)
	out, err := runChatCmd(t, dbPath, bin, "--runtime", "fake", "--history-id", "wf-nohist", "--no-history", "hi")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got := countChatMessages(t, dbPath, "wf-nohist"); got != 0 {
		t.Errorf("saved messages = %d, want 0 (--no-history)", got)
	}
	if !strings.Contains(out, "hello from the fake runtime") {
		t.Errorf("stdout = %q, want it to still contain the streamed NDJSON output", out)
	}
}

// TestChatNoHistorySuppressesSaveEvenWithCanvas is the plan's "canvas
// fallback suppression" case: effectiveHistoryID falls back to --canvas's
// id when --history-id is unset, so --no-history must be checked
// independently of that fallback, not by clearing effectiveHistoryID
// itself (which would also change whether `store` gets initialized above).
func TestChatNoHistorySuppressesSaveEvenWithCanvas(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin := writeFakeMonomindForChat(t, successTranscript)
	// A real workflow row so CanvasTools.checkWorkflowOwnership admits it.
	{
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open db to seed workflow: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO workflows (id, name, profile_id) VALUES ('wf-canvas', 'Test', 'default')`); err != nil {
			db.Close()
			t.Fatalf("seed workflow: %v", err)
		}
		db.Close()
	}
	_, err := runChatCmd(t, dbPath, bin, "--runtime", "fake", "--canvas", "wf-canvas", "--no-history", "hi")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got := countChatMessages(t, dbPath, "wf-canvas"); got != 0 {
		t.Errorf("saved messages = %d, want 0 (--no-history must suppress even when --canvas provides the fallback history id)", got)
	}
}

// TestChatFailedTurnReturnsErrorInsteadOfExiting is the regression test for
// the os.Exit fix: a fatal turn must come back as a normal returned error
// (so cmd.Execute() returns non-nil and the test process keeps running to
// report it) rather than calling os.Exit and taking the whole test binary
// down with it. It also confirms the partial conversation is still saved —
// the save block used to be unreachable on this path entirely.
func TestChatFailedTurnReturnsErrorInsteadOfExiting(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin := writeFakeMonomindForChat(t, `  echo '{"v":1,"type":"start","runtime":"fake","cwd":"/app","pid":1}'
  echo '{"v":1,"type":"error","code":"auth","fatal":true,"message":"not logged in"}'
  echo '{"v":1,"type":"done","exit_code":1}'`)
	_, err := runChatCmd(t, dbPath, bin, "--runtime", "fake", "--history-id", "wf-failed", "hi")
	if err == nil {
		t.Fatal("chat returned nil error for a fatal turn, want the *monomind.ProtocolError")
	}
	var pe *monomind.ProtocolError
	if !errors.As(err, &pe) {
		t.Errorf("err = %v (%T), want a *monomind.ProtocolError", err, err)
	}
	if got := countChatMessages(t, dbPath, "wf-failed"); got != 1 {
		t.Errorf("saved messages = %d, want 1 (the user turn is preserved even though the turn failed before any assistant text arrived)", got)
	}
}
