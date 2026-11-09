package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/capture"
	recordcli "github.com/monoes/mono-agent/internal/cli/record"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/recordanalyze"
	"github.com/monoes/mono-agent/internal/recording"
)

// Through the CLI's own wiring (recordDeps, whose Refusal is
// operator.Refusal) and a real marker in the environment, every `record
// replay`, `record verify` and `record save` refuses an agent before the
// browser or the registry (exit 3, operator_only), for CLAUDECODE and for
// another agent's marker alike; only --dry-run stays open. With the markers cleared, the user's replay reaches
// the browser and the user's verify gets past the check (to the missing
// draft). A stubbed Refusal could pass while the command was unwired.
func TestRecordReplayAndVerifyRefuseAnAgentThroughTheCLI(t *testing.T) {
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(capture.InboxEnv, filepath.Join(home, "inbox"))
	t.Cleanup(func() { recording.SetAllProfiles() })
	ingestClickRecording(t, "rec-op")

	cfg := &globalConfig{DBPath: filepath.Join(home, "monoagent.db"), JSONOutput: true}
	run := func(args ...string) (out string, err error, browser bool) {
		t.Helper()
		d := recordDeps(cfg)
		d.ReplayPage = func(context.Context, bool) (browserpkg.PageInterface, zerolog.Logger, error) {
			browser = true
			return nil, zerolog.Nop(), errors.New("browser stub")
		}
		d.VerifyExec = func(context.Context, string, bool, bool, func(string) (string, bool)) (recordanalyze.ExecFunc, error) {
			browser = true
			return nil, errors.New("browser stub")
		}
		cmd := recordcli.NewCommand(d)
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(io.Discard)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		cmd.SetArgs(args)
		err = cmd.Execute()
		return buf.String(), err, browser
	}

	for _, marker := range []string{"CLAUDECODE", "AI_AGENT"} {
		if !slices.Contains(operator.MarkerEnv(), marker) {
			t.Fatalf("%s is not an operator marker", marker)
		}
		t.Setenv(marker, "1")
		for _, args := range [][]string{
			{"replay", "rec-op"}, {"replay", "rec-op", "--yes"},
			{"verify", "nope"}, {"verify", "nope", "--full"},
			{"save", "nope"}, {"save", "nope", "--as", "workflow"},
		} {
			out, err, browser := run(args...)
			if exitCodeFor(err) != 3 || !strings.Contains(out, `"operator_only"`) || browser {
				t.Errorf("%s: %v: exit %d, browser %v, out %s; want exit 3 operator_only before the browser", marker, args, exitCodeFor(err), browser, out)
			}
		}
		if out, err, browser := run("replay", "rec-op", "--dry-run"); err != nil || browser {
			t.Errorf("%s: replay --dry-run: %v (browser %v)\n%s", marker, err, browser, out)
		}
		t.Setenv(marker, "")
	}

	if out, err, browser := run("replay", "rec-op", "--yes"); !browser || strings.Contains(out, "operator_only") {
		t.Errorf("the user's replay --yes: %v, browser %v, out %s; want it to reach the browser", err, browser, out)
	}
	for _, args := range [][]string{{"verify", "nope"}, {"save", "nope"}, {"save", "nope", "--as", "workflow"}} {
		if out, err, _ := run(args...); err == nil || strings.Contains(out, "operator_only") {
			t.Errorf("the user's %v: %v, out %s; want it past the check, to the missing draft", args, err, out)
		}
	}
}

// ingestClickRecording lands a one-click recording as id through the real
// ingest.
func ingestClickRecording(t *testing.T, id string) {
	t.Helper()
	in := &recording.Ingest{}
	frames := []recording.Frame{
		{Op: recording.OpStart, RecordingID: id, URL: "https://example.com/", Title: id, StartedAt: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC).UnixMilli()},
		{Op: recording.OpEvent, RecordingID: id, Event: &recording.Event{ID: "e1", Seq: 1, Type: recording.EvClick, URL: "https://example.com/",
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

// The extension's verify (stopping at the first page step) opens a blank
// tab, not the draft package's start URL, which is a file anyone can write.
func TestTheExtensionsVerifyOpensABlankTab(t *testing.T) {
	if got := verifyStartPlatform(recordanalyze.WithStopAtActions(context.Background()), "example-go"); got != "" {
		t.Errorf("stop-at-actions verify opens %q's start page", got)
	}
	if got := verifyStartPlatform(context.Background(), "example-go"); got != "example-go" {
		t.Errorf("the user's verify opens %q, want the draft's start page", got)
	}
}
